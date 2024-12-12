package gtshred

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"hash"
	"sync"
	"time"

	"github.com/gordian-engine/gordian/gerasure"
	"github.com/gordian-engine/gordian/gerasure/gereedsolomon"
	"github.com/gordian-engine/gordian/gturbine"
)

// Constants for error checking
const (
	minChunkSize = 1024              // 1KB minimum
	maxChunkSize = 1 << 20           // 1MB maximum chunk size
	maxBlockSize = 128 * 1024 * 1024 // 128MB maximum block size (matches Solana)
)

// ReconstructorWithTimestamp is a Reconstructor with a timestamp for tracking when the first shred was received.
type ReconstructorWithTimestamp struct {
	*gereedsolomon.Reconstructor
	Metadata  *gturbine.ShredMetadata
	Timestamp time.Time

	mu sync.Mutex
}

type Processor struct {
	// cb is the callback to call when a block is fully reassembled
	cb ProcessorCallback

	// groups is a cache of shred groups currently being processed.
	groups   map[string]*ReconstructorWithTimestamp
	groupsMu sync.RWMutex

	// completedBlocks is a cache of block hashes that have been fully reassembled and should no longer be processed.
	completedBlocks   map[string]time.Time
	completedBlocksMu sync.RWMutex

	shredHasher func() hash.Hash
	blockHasher func() hash.Hash

	// cleanupInterval is the interval at which stale groups are cleaned up and completed blocks are removed
	cleanupInterval time.Duration
}

// ProcessorCallback is the interface for processor callbacks.
type ProcessorCallback interface {
	ProcessBlock(height uint64, blockHash []byte, block []byte) error
}

// NewProcessor creates a new Processor with the given callback and cleanup interval.
func NewProcessor(cb ProcessorCallback, shredHasher func() hash.Hash, blockHasher func() hash.Hash, cleanupInterval time.Duration) *Processor {
	return &Processor{
		cb:              cb,
		shredHasher:     shredHasher,
		blockHasher:     blockHasher,
		groups:          make(map[string]*ReconstructorWithTimestamp),
		completedBlocks: make(map[string]time.Time),
		cleanupInterval: cleanupInterval,
	}
}

// RunBackgroundCleanup starts a cleanup loop that runs at the cleanup interval.
// This should be run as a goroutine.
func (p *Processor) RunBackgroundCleanup(ctx context.Context) {
	ticker := time.NewTicker(p.cleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			p.cleanupStaleGroups(now)
		}
	}
}

// CollectShred processes an incoming data shred.
func (p *Processor) CollectShred(shred *gturbine.Shred) error {
	if shred == nil {
		return fmt.Errorf("nil shred")
	}

	// Skip shreds from already processed blocks
	if p.isCompleted(shred.Metadata.BlockHash) {
		return nil
	}

	h := p.shredHasher()
	h.Write(shred.Data)
	hash := h.Sum(nil)

	if !bytes.Equal(hash, shred.Hash) {
		return fmt.Errorf("shred hash mismatch: got %x want %x", hash, shred.Hash)
	}

	group, ok := p.getGroup(shred.Metadata.GroupID)
	if !ok {
		// If the group doesn't exist, create it and add the shred
		return p.initGroup(shred)
	}

	group.mu.Lock()
	defer group.mu.Unlock()

	m := group.Metadata

	// After locking the group, check if the block has already been completed.
	if p.isCompleted(m.BlockHash) {
		return nil
	}

	if err := group.Reconstructor.ReconstructData(nil, shred.Index, shred.Data); err != nil {
		if !errors.Is(err, gerasure.ErrIncompleteSet) {
			return err
		}
		return nil
	}

	// The block is now full, reconstruct it and process it.
	block, err := group.Reconstructor.Data(make([]byte, 0, m.FullDataSize), m.FullDataSize)
	if err != nil {
		return fmt.Errorf("failed to reconstruct block: %w", err)
	}

	// Verify the block hash
	h = p.blockHasher()
	h.Write(block)
	blockHash := h.Sum(nil)

	if !bytes.Equal(blockHash, m.BlockHash) {
		return fmt.Errorf("block hash mismatch: got %x want %x", blockHash, m.BlockHash)
	}

	if err := p.cb.ProcessBlock(m.Height, m.BlockHash, block); err != nil {
		return fmt.Errorf("failed to process block: %w", err)
	}

	p.deleteGroup(m.GroupID)
	// then mark the block as completed at time.Now()
	p.setCompleted(m.BlockHash)

	return nil
}

// cleanupStaleGroups removes groups that have been inactive for longer than the cleanup interval.
func (p *Processor) cleanupStaleGroups(now time.Time) {
	var deleteHashes []string

	p.completedBlocksMu.RLock()
	for hash, completedAt := range p.completedBlocks {
		if now.Sub(completedAt) > p.cleanupInterval {
			deleteHashes = append(deleteHashes, hash)
		}
	}
	p.completedBlocksMu.RUnlock()

	if len(deleteHashes) != 0 {
		// Take write lock once for all deletions
		p.completedBlocksMu.Lock()
		for _, hash := range deleteHashes {
			delete(p.completedBlocks, hash)
		}
		p.completedBlocksMu.Unlock()
	}

	var deleteGroups []string

	// Take read lock on groups to check for groups to delete (stale or duplicate blockhash)
	p.groupsMu.RLock()
	for id, group := range p.groups {
		for _, hash := range deleteHashes {
			// Check if group is associated with a completed block
			if string(group.Metadata.BlockHash) == hash {
				deleteGroups = append(deleteGroups, id)
			}
		}

		// Check if group is stale
		if now.Sub(group.Timestamp) > p.cleanupInterval {
			deleteGroups = append(deleteGroups, id)
		}
	}
	p.groupsMu.RUnlock()

	if len(deleteGroups) != 0 {
		// Take write lock once for all deletions
		p.groupsMu.Lock()
		for _, id := range deleteGroups {
			delete(p.groups, id)
		}
		p.groupsMu.Unlock()
	}
}

// initGroup creates a new group and adds the first shred to it.
func (p *Processor) initGroup(shred *gturbine.Shred) error {
	now := time.Now()

	m := shred.Metadata

	rcons, err := gereedsolomon.NewReconstructor(m.TotalDataShreds, m.TotalRecoveryShreds, len(shred.Data))
	if err != nil {
		return fmt.Errorf("failed to create reconstructor: %w", err)
	}

	p.groupsMu.Lock()

	if _, ok := p.groups[shred.Metadata.GroupID]; ok {
		// If a group already exists, return early to avoid overwriting
		p.groupsMu.Unlock()

		// Collect the shred into the existing group
		return p.CollectShred(shred)
	}

	defer p.groupsMu.Unlock()

	group := &ReconstructorWithTimestamp{
		Reconstructor: rcons,
		Metadata:      m,
		Timestamp:     now,
	}

	group.Reconstructor.ReconstructData(nil, shred.Index, shred.Data)

	p.groups[m.GroupID] = group

	return nil
}

// getGroup returns the group with the given ID, if it exists.
func (p *Processor) getGroup(groupID string) (*ReconstructorWithTimestamp, bool) {
	p.groupsMu.RLock()
	defer p.groupsMu.RUnlock()
	group, ok := p.groups[groupID]
	return group, ok
}

// deleteGroup removes the group with the given ID from the processor.
func (p *Processor) deleteGroup(groupID string) {
	p.groupsMu.Lock()
	defer p.groupsMu.Unlock()
	delete(p.groups, groupID)
}

// setCompleted marks a block as completed.
func (p *Processor) setCompleted(blockHash []byte) {
	p.completedBlocksMu.Lock()
	defer p.completedBlocksMu.Unlock()
	p.completedBlocks[string(blockHash)] = time.Now()
}

// isCompleted checks if a block has been marked as completed.
func (p *Processor) isCompleted(blockHash []byte) bool {
	p.completedBlocksMu.RLock()
	defer p.completedBlocksMu.RUnlock()
	_, ok := p.completedBlocks[string(blockHash)]
	return ok
}
