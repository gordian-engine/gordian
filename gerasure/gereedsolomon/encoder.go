package gereedsolomon

import (
	"context"
	"fmt"

	"github.com/klauspost/reedsolomon"
)

// Encoder is a wrapper around [reedsolomon.Encoder]
// satisfying the [gerasure.Encoder] interface.
type Encoder struct {
	rs reedsolomon.Encoder
}

// NewEncoder returns a new Encoder.
// The options within the given reedsolomon.Encoder determine the number of shards.
func NewEncoder(dataShreds, parityShreds int) (*Encoder, error) {
	if dataShreds <= 0 {
		return nil, fmt.Errorf("data shreds must be > 0")
	}
	if parityShreds <= 0 {
		return nil, fmt.Errorf("parity shreds must be > 0")
	}
	rs, err := reedsolomon.New(dataShreds, parityShreds)
	if err != nil {
		return nil, fmt.Errorf("failed to create reed-solomon encoder: %w", err)
	}
	return &Encoder{rs: rs}, nil
}

// Encode satisfies [gerasure.Encoder].
// Callers should assume that the Encoder takes ownership of the given data slice.
func (e *Encoder) Encode(_ context.Context, data []byte) ([][]byte, error) {
	// From the original data, produce new subslices for the data shards and parity shards.
	allShards, err := e.rs.Split(data)
	if err != nil {
		return nil, fmt.Errorf("failed to split input data: %w", err)
	}

	// But just splitting doesn't populate the parity shards,
	// so we have to call encode in order to calculate and populate those shards.
	if err := e.rs.Encode(allShards); err != nil {
		return nil, fmt.Errorf("failed to encode parity: %w", err)
	}

	return allShards, nil
}
