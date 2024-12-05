package shredding

import (
	"crypto/rand"
	"fmt"

	"github.com/gordian-engine/gordian/gturbine"
	"github.com/gordian-engine/gordian/gturbine/erasure"
)

type Processor struct {
	shredder    *Shredder
	encoder     *erasure.Encoder
	dataShreds  int
	totalShreds int
	chunkSize   uint32
}

func NewProcessor(chunkSize uint32, dataShreds, recoveryShreds int) (*Processor, error) {
	encoder, err := erasure.NewEncoder(dataShreds, recoveryShreds)
	if err != nil {
		return nil, fmt.Errorf("failed to create encoder: %w", err)
	}

	return &Processor{
		shredder:    NewShredder(chunkSize),
		encoder:     encoder,
		dataShreds:  dataShreds,
		totalShreds: dataShreds + recoveryShreds,
		chunkSize:   chunkSize,
	}, nil
}

func (p *Processor) ProcessBlock(block []byte, height uint64) (*ShredGroup, error) {
	// Generate unique group ID
	groupID := make([]byte, 32)
	if _, err := rand.Read(groupID); err != nil {
		return nil, fmt.Errorf("failed to generate group ID: %w", err)
	}

	// Split into equal-sized data shreds
	shreds := make([]*gturbine.Shred, p.dataShreds)
	shredSize := (len(block) + p.dataShreds - 1) / p.dataShreds
	if shredSize > int(p.chunkSize) {
		return nil, fmt.Errorf("block too large: %d bytes exceeds max size %d", len(block), p.chunkSize*uint32(p.dataShreds))
	}

	for i := 0; i < p.dataShreds; i++ {
		start := i * shredSize
		end := start + shredSize
		if end > len(block) {
			end = len(block)
		}
		shreds[i] = &gturbine.Shred{
			Index:     uint32(i),
			Total:     uint32(p.dataShreds),
			Data:      append([]byte(nil), block[start:end]...),
			BlockHash: groupID, // Using groupID as block hash for now
			Height:    height,
		}
	}

	// Convert to byte slices for erasure coding
	dataBytes := make([][]byte, len(shreds))
	for i, shred := range shreds {
		data, err := SerializeShred(shred, ShredTypeData, groupID)
		if err != nil {
			return nil, fmt.Errorf("failed to serialize shred %d: %w", i, err)
		}
		dataBytes[i] = data
	}

	// Generate recovery shreds
	recoveryBytes, err := p.encoder.GenerateRecoveryShreds(dataBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to generate recovery shreds: %w", err)
	}

	recoveryShreds := make([]*gturbine.Shred, len(recoveryBytes))
	for i, data := range recoveryBytes {
		shred, _, _, err := DeserializeShred(data)
		if err != nil {
			return nil, fmt.Errorf("failed to deserialize recovery shred %d: %w", i, err)
		}
		recoveryShreds[i] = shred
	}

	return &ShredGroup{
		DataShreds:     shreds,
		RecoveryShreds: recoveryShreds,
		GroupID:        groupID,
	}, nil
}

func (p *Processor) ReassembleBlock(group *ShredGroup) ([]byte, error) {
	allShreds := make([][]byte, p.totalShreds)
	availableShreds := 0

	// Gather available data and recovery shreds
	for i, shred := range group.DataShreds {
		if shred != nil {
			data, err := SerializeShred(shred, ShredTypeData, group.GroupID)
			if err != nil {
				return nil, fmt.Errorf("failed to serialize data shred %d: %w", i, err)
			}
			allShreds[i] = data
			availableShreds++
		}
	}

	offset := len(group.DataShreds)
	for i, shred := range group.RecoveryShreds {
		if shred != nil {
			data, err := SerializeShred(shred, ShredTypeRecovery, group.GroupID)
			if err != nil {
				return nil, fmt.Errorf("failed to serialize recovery shred %d: %w", i, err)
			}
			allShreds[offset+i] = data
			availableShreds++
		}
	}

	// Verify we have enough shreds to reconstruct
	if availableShreds < p.dataShreds {
		return nil, fmt.Errorf("insufficient shreds for reconstruction: have %d, need %d", availableShreds, p.dataShreds)
	}

	// Reconstruct missing shreds
	if err := p.encoder.Reconstruct(allShreds); err != nil {
		return nil, fmt.Errorf("failed to reconstruct shreds: %w", err)
	}

	// Extract and validate data shreds
	dataShreds := make([]*gturbine.Shred, p.dataShreds)
	for i := 0; i < p.dataShreds; i++ {
		shred, _, _, err := DeserializeShred(allShreds[i])
		if err != nil {
			return nil, fmt.Errorf("failed to deserialize reconstructed shred %d: %w", i, err)
		}
		dataShreds[i] = shred
	}

	// Reassemble block from data shreds
	totalSize := 0
	for _, shred := range dataShreds {
		totalSize += len(shred.Data)
	}

	block := make([]byte, 0, totalSize)
	for _, shred := range dataShreds {
		block = append(block, shred.Data...)
	}

	return block, nil
}

type ShredGroup struct {
	DataShreds     []*gturbine.Shred
	RecoveryShreds []*gturbine.Shred
	GroupID        []byte
}
