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
	maxChunkSize = 1 << 20          // 1MB maximum chunk size
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

	// Calculate block hash for verification
	blockHash := sha256.Sum256(block)

	// Generate unique group ID for this block
	groupID := make([]byte, 32)
	if _, err := rand.Read(groupID); err != nil {
		return nil, fmt.Errorf("failed to generate group ID: %w", err)
	}

	// Create fixed-size data chunks
	dataBytes := make([][]byte, p.dataShreds)
	bytesPerShred := int(p.chunkSize)
	
	// Initialize all shreds to full chunk size with zeros
	for i := 0; i < p.dataShreds; i++ {
		dataBytes[i] = make([]byte, bytesPerShred)
	}

	// Copy data into shreds
	remaining := len(block)
	offset := 0
	for i := 0; i < p.dataShreds && remaining > 0; i++ {
		toCopy := remaining
		if toCopy > bytesPerShred {
			toCopy = bytesPerShred
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
			GroupID:   groupID,    // Set the group ID for each shred
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
			GroupID:   groupID,    // Set the group ID for each recovery shred too
			Height:    height,
		}
	}

	return &ShredGroup{
		DataShreds:     dataShreds,
		RecoveryShreds: recoveryShreds,
		GroupID:        groupID,
		BlockHash:      blockHash[:],
		OriginalSize:   len(block),
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
	var refGroupID []byte

	// First find a valid reference height and group ID
	for _, shred := range group.DataShreds {
		if shred != nil && len(shred.Data) == int(p.chunkSize) {
			refHeight = shred.Height
			refGroupID = shred.GroupID
			break
		}
	}

	if refGroupID == nil {
		return nil, fmt.Errorf("no valid shreds found to determine group ID")
	}

	// Process data shreds
	for i, shred := range group.DataShreds {
		if shred != nil {
			if len(shred.Data) != int(p.chunkSize) {
				continue
			}
			if shred.Height != refHeight {
				continue
			}
			if string(shred.GroupID) != string(refGroupID) {
				continue // Skip shreds from different groups
			}
			allBytes[i] = make([]byte, p.chunkSize)
			copy(allBytes[i], shred.Data)
			availableShreds++
		}
	}

	// Process recovery shreds
	if group.RecoveryShreds != nil {
		for i, shred := range group.RecoveryShreds {
			if shred != nil {
				if len(shred.Data) != int(p.chunkSize) {
					continue
				}
				if shred.Height != refHeight {
					continue
				}
				if string(shred.GroupID) != string(refGroupID) {
					continue // Skip shreds from different groups
				}
				allBytes[i+p.dataShreds] = make([]byte, p.chunkSize)
				copy(allBytes[i+p.dataShreds], shred.Data)
				availableShreds++
			}
		}
	}

	if availableShreds < p.dataShreds {
		return nil, fmt.Errorf("insufficient shreds for reconstruction: have %d, need %d", availableShreds, p.dataShreds)
	}

	// Reconstruct missing data
	if err := p.encoder.Reconstruct(allBytes); err != nil {
		return nil, fmt.Errorf("failed to reconstruct data: %w", err)
	}

	// Combine data shreds to form final block
	reconstructed := make([]byte, 0, group.OriginalSize)
	remaining := group.OriginalSize

	for i := 0; i < p.dataShreds && remaining > 0; i++ {
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
		return nil, fmt.Errorf("block hash mismatch after reconstruction")
	}

	return reconstructed, nil
}

type ShredGroup struct {
	DataShreds     []*gturbine.Shred
	RecoveryShreds []*gturbine.Shred
	GroupID        []byte
	BlockHash      []byte
	OriginalSize   int
}
