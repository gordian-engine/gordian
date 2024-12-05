package shredding

import (
	"crypto/rand"
	"crypto/sha256"
	"fmt"

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
	encoder     *erasure.Encoder
	dataShreds  int
	totalShreds int
	chunkSize   uint32
}

func NewProcessor(chunkSize uint32, dataShreds, recoveryShreds int) (*Processor, error) {

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
	}, nil
}

func (p *Processor) ProcessBlock(block []byte, height uint64) (*ShredGroup, error) {

	if len(block) == 0 {
		return nil, fmt.Errorf("empty block")
	}
	if len(block) > maxBlockSize {
		return nil, fmt.Errorf("block too large: %d bytes exceeds max size %d", len(block), maxBlockSize)
	}
	if len(block) > int(p.chunkSize)*p.dataShreds {
		return nil, fmt.Errorf("block too large for configured shred size: %d bytes exceeds max size %d", len(block), p.chunkSize*uint32(p.dataShreds))
	}

	// Calculate block hash for verification before any padding
	blockHash := sha256.Sum256(block)

	// Generate unique group ID
	groupID := make([]byte, 32)
	if _, err := rand.Read(groupID); err != nil {
		return nil, fmt.Errorf("failed to generate group ID: %w", err)
	}

	// Calculate how many shreds we actually need for this block
	fullShreds := len(block) / int(p.chunkSize)
	if len(block)%int(p.chunkSize) != 0 {
		fullShreds++
	}

	// Create shreds of exact chunk size with padding
	dataBytes := make([][]byte, p.dataShreds)
	for i := 0; i < p.dataShreds; i++ {
		dataBytes[i] = make([]byte, p.chunkSize)
	}

	// Copy data into shreds
	remaining := len(block)
	offset := 0
	for i := 0; i < fullShreds; i++ {
		toCopy := remaining
		if toCopy > int(p.chunkSize) {
			toCopy = int(p.chunkSize)
		}
		copy(dataBytes[i], block[offset:offset+toCopy])
		offset += toCopy
		remaining -= toCopy
	}

	// Generate recovery data using erasure coding
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
			BlockHash: blockHash[:],
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
			BlockHash: blockHash[:],
			Height:    height,
		}
	}

	lastShredSize := len(block) % int(p.chunkSize)
	if lastShredSize == 0 && len(block) > 0 {
		lastShredSize = int(p.chunkSize)
	}

	return &ShredGroup{
		DataShreds:     dataShreds,
		RecoveryShreds: recoveryShreds,
		GroupID:        groupID,
		BlockHash:      blockHash[:],
		OriginalSize:   len(block),
		LastShredSize:  lastShredSize,
	}, nil
}

func (p *Processor) ReassembleBlock(group *ShredGroup) ([]byte, error) {

	if group == nil {
		return nil, fmt.Errorf("nil shred group")
	}
	if len(group.DataShreds) != p.dataShreds {
		return nil, fmt.Errorf("incorrect number of data shreds: got %d, want %d", len(group.DataShreds), p.dataShreds)
	}

	// Create working copies for reconstruction
	allBytes := make([][]byte, p.totalShreds)
	availableShreds := 0
	var refHeight uint64

	// First find a valid reference height
	for _, shred := range group.DataShreds {
		if shred != nil && len(shred.Data) == int(p.chunkSize) {
			refHeight = shred.Height
			break
		}
	}

	// Process data shreds
	for i, shred := range group.DataShreds {
		if shred != nil && len(shred.Data) == int(p.chunkSize) && shred.Height == refHeight {
			allBytes[i] = make([]byte, p.chunkSize)
			copy(allBytes[i], shred.Data)
			availableShreds++
		}
	}

	// Process recovery shreds
	if group.RecoveryShreds != nil {
		for i, shred := range group.RecoveryShreds {
			if shred != nil && len(shred.Data) == int(p.chunkSize) && shred.Height == refHeight {
				allBytes[i+p.dataShreds] = make([]byte, p.chunkSize)
				copy(allBytes[i+p.dataShreds], shred.Data)
				availableShreds++
			}
		}
	}

	if availableShreds < p.dataShreds {
		return nil, fmt.Errorf("insufficient shreds for reconstruction: have %d, need %d", availableShreds, p.dataShreds)
	}

	// Reconstruct missing/corrupted data
	if err := p.encoder.Reconstruct(allBytes); err != nil {
		return nil, fmt.Errorf("failed to reconstruct data: %w", err)
	}

	// Calculate exact number of shreds needed for original size
	fullShreds := group.OriginalSize / int(p.chunkSize)
	if group.OriginalSize%int(p.chunkSize) != 0 {
		fullShreds++
	}

	// Combine data shreds to form final block, respecting original size
	reconstructed := make([]byte, 0, group.OriginalSize)
	remaining := group.OriginalSize

	for i := 0; i < fullShreds && remaining > 0; i++ {
		if allBytes[i] == nil {
			return nil, fmt.Errorf("reconstruction failed: missing data for shard %d", i)
		}

		toCopy := remaining
		if toCopy > int(p.chunkSize) {
			toCopy = int(p.chunkSize)
		}
		reconstructed = append(reconstructed, allBytes[i][:toCopy]...)
		remaining -= toCopy
	}

	// Verify reconstructed block hash
	computedHash := sha256.Sum256(reconstructed)

	if string(computedHash[:]) != string(group.BlockHash) {
		return nil, fmt.Errorf("block hash mismatch after reconstruction: original %x, got %x",
			group.BlockHash, computedHash[:])
	}

	return reconstructed, nil
}

type ShredGroup struct {
	DataShreds     []*gturbine.Shred
	RecoveryShreds []*gturbine.Shred
	GroupID        []byte
	BlockHash      []byte
	OriginalSize   int
	LastShredSize  int // Size of actual data in last shred (0 means full)
}
