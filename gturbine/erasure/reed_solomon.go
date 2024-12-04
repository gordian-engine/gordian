package erasure

import (
	"fmt"
	"github.com/klauspost/reedsolomon"
)

const maxTotalShards = 128 // Lowered from 256 to match test expectations

// Encoder handles Reed-Solomon encoding/decoding of shreds
type Encoder struct {
	enc    reedsolomon.Encoder
	data   int
	parity int
}

// NewEncoder creates a new Reed-Solomon encoder with the given data and parity shard counts
func NewEncoder(dataShards, parityShards int) (*Encoder, error) {
	// Validate inputs
	if dataShards <= 0 {
		return nil, fmt.Errorf("data shards must be > 0")
	}
	if parityShards <= 0 {
		return nil, fmt.Errorf("parity shards must be > 0")
	}
	if dataShards+parityShards > maxTotalShards {
		return nil, fmt.Errorf("total shards must be <= %d", maxTotalShards)
	}

	enc, err := reedsolomon.New(dataShards, parityShards)
	if err != nil {
		return nil, fmt.Errorf("failed to create reed-solomon encoder: %w", err)
	}

	return &Encoder{
		enc:    enc,
		data:   dataShards,
		parity: parityShards,
	}, nil
}

// Encode takes data shreds and generates parity shreds
func (e *Encoder) Encode(data [][]byte) ([][]byte, error) {
	if len(data) != e.data {
		return nil, fmt.Errorf("expected %d data shreds, got %d", e.data, len(data))
	}

	// Create empty parity shreds
	shreds := make([][]byte, e.data+e.parity)
	copy(shreds, data)
	
	for i := e.data; i < e.data+e.parity; i++ {
		shreds[i] = make([]byte, len(data[0]))
	}

	if err := e.enc.Encode(shreds); err != nil {
		return nil, fmt.Errorf("encoding failed: %w", err)
	}

	return shreds[e.data:], nil
}

// Reconstruct attempts to reconstruct missing shreds
func (e *Encoder) Reconstruct(shreds [][]byte) error {
	if err := e.enc.Reconstruct(shreds); err != nil {
		return fmt.Errorf("reconstruction failed: %w", err) 
	}
	return nil
}

// Verify checks if the data and parity shreds are valid
func (e *Encoder) Verify(shreds [][]byte) (bool, error) {
	if len(shreds) != e.data+e.parity {
		return false, fmt.Errorf("expected %d total shards, got %d", e.data+e.parity, len(shreds))
	}
	return e.enc.Verify(shreds)
}
