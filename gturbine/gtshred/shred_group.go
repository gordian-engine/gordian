package gtshred

import (
	"crypto/sha256"
	"fmt"
	"sync"

	"github.com/google/uuid"
	"github.com/gordian-engine/gordian/gturbine"
	"github.com/gordian-engine/gordian/gturbine/gtencoding"
)

// ShredGroup represents a group of shreds that can be used to reconstruct a block.
type ShredGroup struct {
	DataShreds          []*gturbine.Shred
	RecoveryShreds      []*gturbine.Shred
	TotalDataShreds     int
	TotalRecoveryShreds int
	GroupID             string // Changed to string for UUID
	BlockHash           []byte
	Height              uint64 // Added to struct level
	OriginalSize        int

	mu sync.Mutex
}

// NewShredGroup creates a new ShredGroup from a block of data
func NewShredGroup(block []byte, height uint64, dataShreds, recoveryShreds int, chunkSize uint32) (*ShredGroup, error) {
	if len(block) == 0 {
		return nil, fmt.Errorf("empty block")
	}
	if len(block) > maxBlockSize {
		return nil, fmt.Errorf("block too large: %d bytes exceeds max size %d", len(block), maxBlockSize)
	}
	if len(block) > int(chunkSize)*dataShreds {
		return nil, fmt.Errorf("block too large for configured shred size: %d bytes exceeds max size %d", len(block), chunkSize*uint32(dataShreds))
	}

	// Create encoder for this block
	encoder, err := gtencoding.NewEncoder(dataShreds, recoveryShreds)
	if err != nil {
		return nil, fmt.Errorf("failed to create encoder: %w", err)
	}

	// Calculate block hash for verification
	// TODO hasher should be interface.
	blockHash := sha256.Sum256(block)

	// Create new shred group
	group := &ShredGroup{
		DataShreds:          make([]*gturbine.Shred, dataShreds),
		RecoveryShreds:      make([]*gturbine.Shred, recoveryShreds),
		TotalDataShreds:     dataShreds,
		TotalRecoveryShreds: recoveryShreds,
		GroupID:             uuid.New().String(),
		BlockHash:           blockHash[:],
		Height:              height,
		OriginalSize:        len(block),
	}

	// Create fixed-size data chunks
	dataBytes := make([][]byte, dataShreds)
	bytesPerShred := int(chunkSize)

	// Initialize all shreds to full chunk size with zeros
	for i := 0; i < dataShreds; i++ {
		dataBytes[i] = make([]byte, bytesPerShred)
	}

	// Copy data into shreds
	remaining := len(block)
	offset := 0
	for i := 0; i < dataShreds && remaining > 0; i++ {
		toCopy := remaining
		if toCopy > bytesPerShred {
			toCopy = bytesPerShred
		}
		copy(dataBytes[i], block[offset:offset+toCopy])
		offset += toCopy
		remaining -= toCopy
	}

	// Generate recovery data using erasure coding
	recoveryBytes, err := encoder.GenerateRecoveryShreds(dataBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to generate recovery shreds: %w", err)
	}

	// Create data shreds
	for i := range dataBytes {
		group.DataShreds[i] = &gturbine.Shred{
			Type:                gturbine.DataShred,
			Index:               i,
			TotalDataShreds:     dataShreds,
			TotalRecoveryShreds: recoveryShreds,
			Data:                dataBytes[i],
			BlockHash:           blockHash[:],
			GroupID:             group.GroupID,
			Height:              height,
			FullDataSize:        group.OriginalSize,
		}
	}

	// Create recovery shreds
	for i := range recoveryBytes {
		group.RecoveryShreds[i] = &gturbine.Shred{
			Type:                gturbine.RecoveryShred,
			Index:               i,
			TotalDataShreds:     dataShreds,
			TotalRecoveryShreds: recoveryShreds,
			Data:                recoveryBytes[i],
			BlockHash:           blockHash[:],
			GroupID:             group.GroupID,
			Height:              height,
			FullDataSize:        group.OriginalSize,
		}
	}

	return group, nil
}

// isFull checks if enough shreds are available to attempt reconstruction.
func (g *ShredGroup) isFull() bool {
	valid := 0
	for _, s := range g.DataShreds {
		if s != nil {
			valid++
		}
	}

	for _, s := range g.RecoveryShreds {
		if s != nil {
			valid++
		}
	}

	return valid >= g.TotalDataShreds
}

// reconstructBlock attempts to reconstruct the original block from available shreds
func (g *ShredGroup) reconstructBlock(encoder *gtencoding.Encoder) ([]byte, error) {
	// Extract data bytes for erasure coding
	allBytes := make([][]byte, len(g.DataShreds)+len(g.RecoveryShreds))

	// Copy available data shreds
	for i, shred := range g.DataShreds {
		if shred != nil {
			allBytes[i] = make([]byte, len(shred.Data))
			copy(allBytes[i], shred.Data)
		}
	}

	// Copy available recovery shreds
	for i, shred := range g.RecoveryShreds {
		if shred != nil {
			allBytes[i+len(g.DataShreds)] = make([]byte, len(shred.Data))
			copy(allBytes[i+len(g.DataShreds)], shred.Data)
		}
	}

	// Reconstruct missing data
	if err := encoder.Reconstruct(allBytes); err != nil {
		return nil, fmt.Errorf("failed to reconstruct data: %w", err)
	}

	// Combine data shreds
	reconstructed := make([]byte, 0, g.OriginalSize)
	remaining := g.OriginalSize

	for i := 0; i < len(g.DataShreds) && remaining > 0; i++ {
		if allBytes[i] == nil {
			return nil, fmt.Errorf("reconstruction failed: missing data for shard %d", i)
		}
		toCopy := remaining
		if toCopy > len(allBytes[i]) {
			toCopy = len(allBytes[i])
		}
		reconstructed = append(reconstructed, allBytes[i][:toCopy]...)
		remaining -= toCopy
	}

	// Verify reconstructed block hash
	// TODO hasher should be interface.
	computedHash := sha256.Sum256(reconstructed)
	if string(computedHash[:]) != string(g.BlockHash) {
		return nil, fmt.Errorf("block hash mismatch after reconstruction")
	}

	return reconstructed, nil
}

// collectShred adds a data shred to the group
func (g *ShredGroup) collectShred(shred *gturbine.Shred) (bool, error) {
	if shred == nil {
		return false, fmt.Errorf("nil shred")
	}

	// Validate shred matches group parameters
	if shred.GroupID != g.GroupID {
		return false, fmt.Errorf("group ID mismatch: got %s, want %s", shred.GroupID, g.GroupID)
	}
	if shred.Height != g.Height {
		return false, fmt.Errorf("height mismatch: got %d, want %d", shred.Height, g.Height)
	}
	if string(shred.BlockHash) != string(g.BlockHash) {
		return false, fmt.Errorf("block hash mismatch")
	}

	switch shred.Type {
	case gturbine.DataShred:
		// Validate shred index
		if int(shred.Index) >= len(g.DataShreds) {
			return false, fmt.Errorf("invalid data shred index: %d", shred.Index)
		}

		g.DataShreds[shred.Index] = shred
	case gturbine.RecoveryShred:
		// Validate shred index
		if int(shred.Index) >= len(g.RecoveryShreds) {
			return false, fmt.Errorf("invalid recovery shred index: %d", shred.Index)
		}

		g.RecoveryShreds[shred.Index] = shred
	default:
		return false, fmt.Errorf("invalid shred type: %d", shred.Type)
	}

	return g.isFull(), nil
}
