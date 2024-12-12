package gtshred

import (
	"fmt"
	"hash"
	"sync"

	"github.com/google/uuid"
	"github.com/gordian-engine/gordian/gerasure/gereedsolomon"
	"github.com/gordian-engine/gordian/gturbine"
)

// ShreddedBlock contains a shredded block's data and metadata
type ShreddedBlock struct {
	Shreds   []*gturbine.Shred
	Metadata *gturbine.ShredMetadata
}

// ShredBlock shreds a block into data and recovery shreds.
func ShredBlock(block []byte, hasher func() hash.Hash, height uint64, dataShreds, recoveryShreds int) (*ShreddedBlock, error) {
	if len(block) == 0 {
		return nil, fmt.Errorf("empty block")
	}
	if len(block) > maxBlockSize {
		return nil, fmt.Errorf("block too large: %d bytes exceeds max size %d", len(block), maxBlockSize)
	}

	// Create encoder for this block
	encoder, err := gereedsolomon.NewEncoder(dataShreds, recoveryShreds)
	if err != nil {
		return nil, fmt.Errorf("failed to create encoder: %w", err)
	}

	h := hasher()
	h.Write(block)
	blockHash := h.Sum(nil)

	m := &gturbine.ShredMetadata{
		GroupID:             uuid.New().String(),
		FullDataSize:        len(block),
		BlockHash:           blockHash[:],
		Height:              height,
		TotalDataShreds:     dataShreds,
		TotalRecoveryShreds: recoveryShreds,
	}

	// Create new shred group
	group := &ShreddedBlock{
		Metadata: m,
		Shreds:   make([]*gturbine.Shred, dataShreds+recoveryShreds),
	}

	allShreds, err := encoder.Encode(nil, block)
	if err != nil {
		return nil, fmt.Errorf("failed to encode block: %w", err)
	}

	var wg sync.WaitGroup
	wg.Add(len(allShreds))
	for i, shard := range allShreds {
		go func(i int, shard []byte) {
			defer wg.Done()
			h := hasher()
			h.Write(shard)
			hash := h.Sum(nil)

			group.Shreds[i] = &gturbine.Shred{
				Metadata: m,
				Index:    i,
				Data:     shard,
				Hash:     hash,
			}
		}(i, shard)
	}
	wg.Wait()

	return group, nil
}
