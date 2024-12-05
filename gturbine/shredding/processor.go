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
	if len(block) > int(p.chunkSize)*p.dataShreds {
		return nil, fmt.Errorf("block too large: %d bytes exceeds max size %d", len(block), p.chunkSize*uint32(p.dataShreds))
	}

	// Generate unique group ID
	groupID := make([]byte, 32)
	if _, err := rand.Read(groupID); err != nil {
		return nil, fmt.Errorf("failed to generate group ID: %w", err)
	}

	// Split data into equal sized chunks
	chunkSize := (len(block) + p.dataShreds - 1) / p.dataShreds
	dataBytes := make([][]byte, p.dataShreds)
	
	for i := 0; i < p.dataShreds; i++ {
		start := i * chunkSize
		end := start + chunkSize
		if end > len(block) {
			end = len(block)
			// Pad last chunk if needed
			chunk := make([]byte, chunkSize)
			copy(chunk, block[start:end])
			dataBytes[i] = chunk
		} else {
			dataBytes[i] = block[start:end]
		}
	}

	// Generate recovery data
	recoveryBytes, err := p.encoder.GenerateRecoveryShreds(dataBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to generate recovery shreds: %w", err)
	}

	// Create data shreds
	dataShreds := make([]*gturbine.Shred, p.dataShreds)
	for i := range dataBytes {
		dataShreds[i] = &gturbine.Shred{
			Index:     uint32(i),
			Total:     uint32(p.dataShreds),
			Data:      dataBytes[i],
			BlockHash: groupID,
			Height:    height,
		}
	}

	// Create recovery shreds 
	recoveryShreds := make([]*gturbine.Shred, len(recoveryBytes))
	for i := range recoveryBytes {
		recoveryShreds[i] = &gturbine.Shred{
			Index:     uint32(i),
			Total:     uint32(len(recoveryBytes)),
			Data:      recoveryBytes[i],
			BlockHash: groupID,
			Height:    height,
		}
	}

	return &ShredGroup{
		DataShreds:     dataShreds,
		RecoveryShreds: recoveryShreds,
		GroupID:        groupID,
	}, nil
}

func (p *Processor) ReassembleBlock(group *ShredGroup) ([]byte, error) {
	// Extract data bytes for erasure coding
	allBytes := make([][]byte, p.totalShreds)
	availableShreds := 0

	for i, shred := range group.DataShreds {
		if shred != nil {
			allBytes[i] = shred.Data
			availableShreds++
		}
	}

	for i, shred := range group.RecoveryShreds {
		if shred != nil {
			allBytes[i+p.dataShreds] = shred.Data
			availableShreds++
		}
	}

	if availableShreds < p.dataShreds {
		return nil, fmt.Errorf("insufficient shreds for reconstruction: have %d, need %d", availableShreds, p.dataShreds)
	}

	// Reconstruct missing data
	if err := p.encoder.Reconstruct(allBytes); err != nil {
		return nil, fmt.Errorf("failed to reconstruct data: %w", err)
	}

	// Combine data shreds
	totalSize := 0
	for i := 0; i < p.dataShreds; i++ {
		totalSize += len(allBytes[i])
	}

	block := make([]byte, 0, totalSize)
	for i := 0; i < p.dataShreds; i++ {
		block = append(block, allBytes[i]...)
	}

	// Remove any padding from the last chunk
	if len(block) > totalSize {
		block = block[:totalSize]
	}

	return block, nil
}

type ShredGroup struct {
	DataShreds     []*gturbine.Shred
	RecoveryShreds []*gturbine.Shred
	GroupID        []byte
}
