package gtshred

import (
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

type Processor struct {
	groups          map[string]*ShredGroup
	mu              sync.Mutex
	cb              ProcessorCallback
	completedBlocks map[string]time.Time
	cleanupInterval time.Duration
}

type ProcessorCallback interface {
	ProcessBlock(height uint64, blockHash []byte, block []byte) error
}

func NewProcessor(cb ProcessorCallback, cleanupInterval time.Duration) *Processor {
	p := &Processor{
		cb:              cb,
		groups:          make(map[string]*ShredGroup),
		completedBlocks: make(map[string]time.Time),
		cleanupInterval: cleanupInterval,
	}

	// Start cleanup goroutine
	go func() {
		ticker := time.NewTicker(cleanupInterval)
		defer ticker.Stop()

		for {
			select {
			case now := <-ticker.C:
				p.cleanupStaleGroups(now)
			}
		}
	}()

	return p
}

// CollectDataShred processes an incoming data shred
func (p *Processor) CollectShred(shred *gturbine.Shred) error {
	if shred == nil {
		return fmt.Errorf("nil shred")
	}

	// Skip shreds from already processed blocks
	if _, completed := p.completedBlocks[string(shred.BlockHash)]; completed {
		return nil
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	group, ok := p.groups[shred.GroupID]
	if !ok {
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

		p.groups[shred.GroupID] = group
		return nil
	}

	full, err := group.CollectShred(shred)
	if err != nil {
		return fmt.Errorf("failed to collect data shred: %w", err)
	}
	if full {
		encoder, err := gtencoding.NewEncoder(group.TotalDataShreds, group.TotalRecoveryShreds)
		if err != nil {
			return fmt.Errorf("failed to create encoder: %w", err)
		}
		block, err := group.ReconstructBlock(encoder)
		if err != nil {
			return fmt.Errorf("failed to reconstruct block: %w", err)
		}
		if err := p.cb.ProcessBlock(shred.Height, shred.BlockHash, block); err != nil {
			return fmt.Errorf("failed to process block: %w", err)
		}
		// remove the group from the map
		delete(p.groups, group.GroupID)

		// then mark the block as completed at time.Now()
		p.completedBlocks[string(shred.BlockHash)] = time.Now()
	}
	return nil
}

// In Processor
func (p *Processor) cleanupStaleGroups(now time.Time) {
	for hash, completedAt := range p.completedBlocks {
		if now.Sub(completedAt) > p.cleanupInterval {
			delete(p.completedBlocks, hash)
		}
	}
}
