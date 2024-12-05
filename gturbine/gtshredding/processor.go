package gtshredding

import (
	"fmt"
	"sync"

	"github.com/gordian-engine/gordian/gturbine"
	"github.com/gordian-engine/gordian/gturbine/erasure"
)

// Constants for error checking
const (
	minChunkSize = 1024              // 1KB minimum
	maxChunkSize = 1 << 20           // 1MB maximum chunk size
	maxBlockSize = 128 * 1024 * 1024 // 128MB maximum block size (matches Solana)
)

type Processor struct {
	groups map[string]*ShredGroup
	mu     sync.Mutex
	cb     ProcessorCallback
}

type ProcessorCallback interface {
	ProcessBlock(height uint64, block []byte) error
}

func NewProcessor(cb ProcessorCallback) *Processor {
	return &Processor{
		cb: cb,
	}
}

// CollectDataShred processes an incoming data shred
func (p *Processor) CollectDataShred(shred *gturbine.Shred) error {
	if shred == nil {
		return fmt.Errorf("nil shred")
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

	full, err := group.CollectDataShred(shred)
	if err != nil {
		return fmt.Errorf("failed to collect data shred: %w", err)
	}
	if full {
		encoder, err := erasure.NewEncoder(group.TotalDataShreds, group.TotalRecoveryShreds)
		if err != nil {
			return fmt.Errorf("failed to create encoder: %w", err)
		}
		block, err := group.ReconstructBlock(encoder)
		if err != nil {
			return fmt.Errorf("failed to reconstruct block: %w", err)
		}
		if err := p.cb.ProcessBlock(shred.Height, block); err != nil {
			return fmt.Errorf("failed to process block: %w", err)
		}
		delete(p.groups, group.GroupID)
	}
	return nil
}

// CollectRecoveryShred processes an incoming recovery shred
func (p *Processor) CollectRecoveryShred(shred *gturbine.Shred) error {
	if shred == nil {
		return fmt.Errorf("nil shred")
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
		group.RecoveryShreds[shred.Index] = shred
		p.groups[shred.GroupID] = group
		return nil
	}

	full, err := group.CollectRecoveryShred(shred)
	if err != nil {
		return fmt.Errorf("failed to collect recovery shred: %w", err)
	}
	if full {
		encoder, err := erasure.NewEncoder(group.TotalDataShreds, group.TotalRecoveryShreds)
		if err != nil {
			return fmt.Errorf("failed to create encoder: %w", err)
		}
		block, err := group.ReconstructBlock(encoder)
		if err != nil {
			return fmt.Errorf("failed to reconstruct block: %w", err)
		}
		if err := p.cb.ProcessBlock(shred.Height, block); err != nil {
			return fmt.Errorf("failed to process block: %w", err)
		}
		delete(p.groups, group.GroupID)
	}
	return err
}
