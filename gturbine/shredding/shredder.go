package shredding

import (
	"crypto/sha256"
	"fmt"

	"github.com/gordian-engine/gordian/gturbine"
)

// Shredder handles block shredding and reassembly
type Shredder struct {
	chunkSize uint32
}

func NewShredder(chunkSize uint32) *Shredder {
	return &Shredder{
		chunkSize: chunkSize,
	}
}

// ShredBlock splits a block into shreds
func (s *Shredder) ShredBlock(block []byte, height uint64) ([]*gturbine.Shred, error) {
	if len(block) == 0 {
		return nil, fmt.Errorf("empty block")
	}

	// Calculate block hash
	blockHash := sha256.Sum256(block)

	// Calculate number of shreds needed
	numShreds := (len(block) + int(s.chunkSize) - 1) / int(s.chunkSize)
	shreds := make([]*gturbine.Shred, numShreds)

	// Split block into shreds
	for i := 0; i < numShreds; i++ {
		start := i * int(s.chunkSize)
		end := start + int(s.chunkSize)
		if end > len(block) {
			end = len(block)
		}

		shreds[i] = &gturbine.Shred{
			Index:     uint32(i),
			Total:     uint32(numShreds),
			Data:      block[start:end],
			BlockHash: blockHash[:],
			Height:    height,
		}
	}

	return shreds, nil
}

// AssembleBlock reassembles a block from shreds
func (s *Shredder) AssembleBlock(shreds []*gturbine.Shred) ([]byte, error) {
	if len(shreds) == 0 {
		return nil, fmt.Errorf("no shreds provided")
	}

	// Validate shreds belong to same block
	blockHash := shreds[0].BlockHash
	total := shreds[0].Total
	height := shreds[0].Height

	for _, shred := range shreds {
		if string(shred.BlockHash) != string(blockHash) {
			return nil, fmt.Errorf("mismatched block hash")
		}
		if shred.Total != total {
			return nil, fmt.Errorf("mismatched total shred count")
		}
		if shred.Height != height {
			return nil, fmt.Errorf("mismatched height")
		}
	}

	if uint32(len(shreds)) != total {
		return nil, fmt.Errorf("incomplete shreds: got %d, want %d", len(shreds), total)
	}

	// Sort shreds by index
	sorted := make([]*gturbine.Shred, total)
	for _, shred := range shreds {
		if shred.Index >= total {
			return nil, fmt.Errorf("invalid shred index %d", shred.Index)
		}
		sorted[shred.Index] = shred
	}

	// Concatenate data
	totalSize := 0
	for _, shred := range sorted {
		totalSize += len(shred.Data)
	}

	block := make([]byte, 0, totalSize)
	for _, shred := range sorted {
		block = append(block, shred.Data...)
	}

	// Verify block hash
	computedHash := sha256.Sum256(block)
	if string(computedHash[:]) != string(blockHash) {
		return nil, fmt.Errorf("reassembled block hash mismatch")
	}

	return block, nil
}
