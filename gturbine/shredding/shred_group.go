package shredding

import (
	"crypto/sha256"
	"fmt"

	"github.com/google/uuid"
	"github.com/gordian-engine/gordian/gturbine"
	"github.com/gordian-engine/gordian/gturbine/erasure"
)

// FromBlock creates a new ShredGroup from a block of data
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
	encoder, err := erasure.NewEncoder(dataShreds, recoveryShreds)
	if err != nil {
		return nil, fmt.Errorf("failed to create encoder: %w", err)
	}

	// Calculate block hash for verification
	blockHash := sha256.Sum256(block)

	// Create new shred group
	group := &ShredGroup{
		DataShreds:     make([]*gturbine.Shred, dataShreds),
		RecoveryShreds: make([]*gturbine.Shred, recoveryShreds),
		GroupID:        uuid.New().String(),
		BlockHash:      blockHash[:],
		Height:         height,
		OriginalSize:   len(block),
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
			Index:        uint32(i),
			Total:        uint32(dataShreds),
			Data:         dataBytes[i],
			BlockHash:    blockHash[:],
			GroupID:      group.GroupID,
			Height:       height,
			FullDataSize: group.OriginalSize,
		}
	}

	// Create recovery shreds
	for i := range recoveryBytes {
		group.RecoveryShreds[i] = &gturbine.Shred{
			Index:        uint32(i),
			Total:        uint32(len(recoveryBytes)),
			Data:         recoveryBytes[i],
			BlockHash:    blockHash[:],
			GroupID:      group.GroupID,
			Height:       height,
			FullDataSize: group.OriginalSize,
		}
	}

	return group, nil
}

// IsComplete checks if enough shreds are available for reconstruction
// NOTE: we'd like shredgroup to know the data threshold as a property on the shredgroup
func (g *ShredGroup) IsComplete(dataThreshold int) bool {

	// TODO: ensure that we've met the threshold by quorum of both data and recovery using the
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
	return valid >= dataThreshold
}

// ReconstructBlock attempts to reconstruct the original block from available shreds
func (g *ShredGroup) ReconstructBlock(encoder *erasure.Encoder) ([]byte, error) {

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
	computedHash := sha256.Sum256(reconstructed)
	if string(computedHash[:]) != string(g.BlockHash) {
		return nil, fmt.Errorf("block hash mismatch after reconstruction")
	}

	return reconstructed, nil
}

// CollectDataShred adds a data shred to the group
func (g *ShredGroup) CollectDataShred(shred *gturbine.Shred) (bool, error) {
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

	// Validate shred index
	if int(shred.Index) >= len(g.DataShreds) {
		return false, fmt.Errorf("invalid shred index: %d", shred.Index)
	}

	g.DataShreds[shred.Index] = shred
	return g.IsComplete(len(g.DataShreds)), nil
}

// CollectRecoveryShred adds a recovery shred to the group
func (g *ShredGroup) CollectRecoveryShred(shred *gturbine.Shred) (bool, error) {
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

	// Validate shred index
	if int(shred.Index) >= len(g.RecoveryShreds) {
		return false, fmt.Errorf("invalid recovery shred index: %d", shred.Index)
	}

	g.RecoveryShreds[shred.Index] = shred
	return g.IsComplete(len(g.DataShreds)), nil
}
