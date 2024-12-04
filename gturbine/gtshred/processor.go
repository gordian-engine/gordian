package gtshred

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/gordian-engine/gordian/gturbine"
	"github.com/gordian-engine/gordian/gturbine/gtencoding"
)

// Constants for error checking
const (
	minChunkSize = 1024              // 1KB minimum
	maxChunkSize = 1 << 20           // 1MB maximum chunk size
	maxBlockSize = 128 * 1024 * 1024 // 128MB maximum block size (matches Solana)
)

// ShredGroupWithTimestamp is a ShredGroup with a timestamp for tracking when the group was created (when the first shred was received).
type ShredGroupWithTimestamp struct {
	*ShredGroup
	Timestamp time.Time
}

type Processor struct {
	// cb is the callback to call when a block is fully reassembled
	cb ProcessorCallback

	// groups is a cache of shred groups currently being processed.
	groups   map[string]*ShredGroupWithTimestamp
	groupsMu sync.RWMutex

	// completedBlocks is a cache of block hashes that have been fully reassembled and should no longer be processed.
	completedBlocks   map[string]time.Time
	completedBlocksMu sync.RWMutex

	// cleanupInterval is the interval at which stale groups are cleaned up and completed blocks are removed
	cleanupInterval time.Duration
}

// ProcessorCallback is the interface for processor callbacks.
type ProcessorCallback interface {
	ProcessBlock(height uint64, blockHash []byte, block []byte) error
}

// NewProcessor creates a new Processor with the given callback and cleanup interval.
func NewProcessor(cb ProcessorCallback, cleanupInterval time.Duration) *Processor {
	return &Processor{
		cb:              cb,
		groups:          make(map[string]*ShredGroupWithTimestamp),
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
	if p.isCompleted(shred.BlockHash) {
		return nil
	}

	group, ok := p.getGroup(shred.GroupID)
	if !ok {
		// If the group doesn't exist, create it and add the shred
		return p.initGroup(shred)
	}

	group.mu.Lock()
	defer group.mu.Unlock()

	// After locking the group, check if the block has already been completed.
	if p.isCompleted(group.BlockHash) {
		return nil
	}

	full, err := group.collectShred(shred)
	if err != nil {
		return fmt.Errorf("failed to collect data shred: %w", err)
	}
	if full {
		encoder, err := gtencoding.NewEncoder(group.TotalDataShreds, group.TotalRecoveryShreds)
		if err != nil {
			return fmt.Errorf("failed to create encoder: %w", err)
		}

		block, err := group.reconstructBlock(encoder)
		if err != nil {
			return fmt.Errorf("failed to reconstruct block: %w", err)
		}

		if err := p.cb.ProcessBlock(shred.Height, shred.BlockHash, block); err != nil {
			return fmt.Errorf("failed to process block: %w", err)
		}

		p.deleteGroup(shred.GroupID)
		// then mark the block as completed at time.Now()
		p.setCompleted(shred.BlockHash)
	}
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
			if string(group.BlockHash) == hash {
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
	group := &ShredGroup{
		DataShreds:          make([]*gturbine.Shred, shred.TotalDataShreds),
		RecoveryShreds:      make([]*gturbine.Shred, shred.TotalRecoveryShreds),
		TotalDataShreds:     shred.TotalDataShreds,
		TotalRecoveryShreds: shred.TotalRecoveryShreds,
		GroupID:             shred.GroupID,
		BlockHash:           shred.BlockHash,
		Height:              shred.Height,
		OriginalSize:        shred.FullDataSize,
	}

	group.DataShreds[shred.Index] = shred

	p.groupsMu.Lock()

	if _, ok := p.groups[shred.GroupID]; ok {
		// If a group already exists, return early to avoid overwriting
		p.groupsMu.Unlock()

		// Collect the shred into the existing group
		return p.CollectShred(shred)
	}

	defer p.groupsMu.Unlock()

	p.groups[shred.GroupID] = &ShredGroupWithTimestamp{
		ShredGroup: group,
		Timestamp:  now,
	}

	return nil
}

// getGroup returns the group with the given ID, if it exists.
func (p *Processor) getGroup(groupID string) (*ShredGroupWithTimestamp, bool) {
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
