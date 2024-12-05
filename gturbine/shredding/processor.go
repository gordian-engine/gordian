package shredding

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

type ShredGroup struct {
	DataShreds     []*gturbine.Shred
	RecoveryShreds []*gturbine.Shred
	GroupID        string // Changed to string for UUID
	BlockHash      []byte
	Height         uint64 // Added to struct level
	OriginalSize   int
}

type Processor struct {
	encoder     *erasure.Encoder
	dataShreds  int
	totalShreds int
	chunkSize   uint32
	groups      sync.Map // string -> *ShredGroup
	cb          ProcessorCallback
}

type ProcessorCallback interface {
	ProcessBlock(height uint64, block []byte) error
}

func NewProcessor(chunkSize uint32, dataShreds, recoveryShreds int, cb ProcessorCallback) (*Processor, error) {
	if chunkSize < minChunkSize || chunkSize > maxChunkSize {
		return nil, fmt.Errorf("invalid chunk size %d: must be between %d and %d", chunkSize, minChunkSize, maxChunkSize)
	}
	if dataShreds <= 0 {
		return nil, fmt.Errorf("dataShreds must be positive, got %d", dataShreds)
	}
	if recoveryShreds <= 0 {
		return nil, fmt.Errorf("recoveryShreds must be positive, got %d", recoveryShreds)
	}

	// Validate maximum block size
	maxPossibleBlockSize := int(chunkSize) * dataShreds
	if maxPossibleBlockSize > maxBlockSize {
		return nil, fmt.Errorf("chunk size and data shreds would allow blocks larger than %d bytes", maxBlockSize)
	}

	encoder, err := erasure.NewEncoder(dataShreds, recoveryShreds)
	if err != nil {
		return nil, fmt.Errorf("failed to create encoder: %w", err)
	}

	return &Processor{
		encoder:     encoder,
		dataShreds:  dataShreds,
		totalShreds: dataShreds + recoveryShreds,
		chunkSize:   chunkSize,
		cb:          cb,
	}, nil
}

// CollectDataShred processes an incoming data shred
func (p *Processor) CollectDataShred(shred *gturbine.Shred) error {
	if shred == nil {
		return fmt.Errorf("nil shred")
	}

	value, ok := p.groups.Load(shred.GroupID)
	if !ok {
		group := &ShredGroup{
			DataShreds:     make([]*gturbine.Shred, shred.Total),
			RecoveryShreds: make([]*gturbine.Shred, shred.Total),
			GroupID:        shred.GroupID,
			BlockHash:      shred.BlockHash,
			Height:         shred.Height,
			OriginalSize:   shred.FullDataSize,
		}
		group.DataShreds[shred.Index] = shred
		p.groups.Store(shred.GroupID, group)
		return nil
	}

	// Get or create the shred group
	group := value.(*ShredGroup)

	full, err := group.CollectDataShred(shred)
	if err != nil {
		return fmt.Errorf("failed to collect data shred: %w", err)
	}
	if full {
		block, err := group.ReconstructBlock(p.encoder)
		if err != nil {
			return fmt.Errorf("failed to reconstruct block: %w", err)
		}
		if err := p.cb.ProcessBlock(shred.Height, block); err != nil {
			return fmt.Errorf("failed to process block: %w", err)
		}
		p.DeleteGroup(group.GroupID)
	}
	return nil
}

// CollectRecoveryShred processes an incoming recovery shred
func (p *Processor) CollectRecoveryShred(shred *gturbine.Shred) error {
	if shred == nil {
		return fmt.Errorf("nil shred")
	}

	value, ok := p.groups.Load(shred.GroupID)
	if !ok {
		group := &ShredGroup{
			DataShreds:     make([]*gturbine.Shred, shred.Total),
			RecoveryShreds: make([]*gturbine.Shred, shred.Total),
			GroupID:        shred.GroupID,
			BlockHash:      shred.BlockHash,
			Height:         shred.Height,
			OriginalSize:   shred.FullDataSize,
		}
		group.RecoveryShreds[shred.Index] = shred
		p.groups.Store(shred.GroupID, group)
		return nil
	}

	// Get or create the shred group
	group := value.(*ShredGroup)

	full, err := group.CollectRecoveryShred(shred)
	if err != nil {
		return fmt.Errorf("failed to collect recovery shred: %w", err)
	}
	if full {
		block, err := group.ReconstructBlock(p.encoder)
		if err != nil {
			return fmt.Errorf("failed to reconstruct block: %w", err)
		}
		if err := p.cb.ProcessBlock(shred.Height, block); err != nil {
			return fmt.Errorf("failed to process block: %w", err)
		}
		p.DeleteGroup(group.GroupID)
	}
	return err
}

// GetGroup retrieves a shred group by its ID
func (p *Processor) GetGroup(groupID string) (*ShredGroup, bool) {
	value, exists := p.groups.Load(groupID)
	if !exists {
		return nil, false
	}
	return value.(*ShredGroup), true
}

// DeleteGroup removes a shred group
func (p *Processor) DeleteGroup(groupID string) {
	p.groups.Delete(groupID)
}
