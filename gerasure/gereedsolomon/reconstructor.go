package gereedsolomon

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"github.com/gordian-engine/gordian/gerasure"
	"github.com/klauspost/reedsolomon"
)

// Reconstructor is a wrapper around [reedsolomon.Encoder]
// satisfying the [gerasure.Reconstructor] interface.
type Reconstructor struct {
	rs reedsolomon.Encoder

	// allShards is allocated at the correct size,
	// and with optimized byte alignment,
	// reflecting all possible data and parity shards.
	// Following the same organization as the reedsolomon library,
	// the data shards are first and in order, and then the parity shards in order.
	allShards [][]byte

	shardSize int
}

// NewReconstructor returns a new Reconstructor.
// The options within the given reedsolomon.Encoder determine the number of shards.
// The shardSize and totalDataSize must be discovered out of band;
func NewReconstructor(dataShards, parityShards, shardSize int, opts ...reedsolomon.Option) (*Reconstructor, error) {
	if dataShards <= 0 {
		return nil, fmt.Errorf("data shards must be > 0")
	}
	if parityShards <= 0 {
		return nil, fmt.Errorf("parity shards must be > 0")
	}
	rs, err := reedsolomon.New(dataShards, parityShards, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create reed-solomon reconstructor: %w", err)
	}

	// All reedsolomon.Encoder instances are guaranteed to satisfy reedsolomon.Extensions.
	// Calling AllocAligned is supposed to result in better throughput
	// when actually encoding and decoding.
	allShards := rs.(reedsolomon.Extensions).AllocAligned(shardSize)

	// All shards will have sufficient capacity now,
	// but we need them to be zero-length until we actually receive the data.
	// If they are full length, the reedsolomon implementation
	// assumes the existing data or parity (which is all zeros) is correct.
	for i, s := range allShards {
		allShards[i] = s[:0]
	}

	return &Reconstructor{
		rs:        rs,
		allShards: allShards,

		shardSize: shardSize,
	}, nil
}

// ReconstructData satisfies [gerasure.Reconstructor].
// We assume that the caller is keeping track of already marked indices;
// nothing should go wrong if the same index is used more than once,
// but it will waste some CPU cycles.
func (r *Reconstructor) ReconstructData(_ context.Context, idx int, shard []byte) error {
	if len(shard) != r.shardSize {
		panic(fmt.Errorf(
			"BUG: attempted to reconstruct with invalid shard size: want %d, got %d",
			r.shardSize, len(shard),
		))
	}

	// Re-slice the shard we have, to fit the incoming shard.
	r.allShards[idx] = r.allShards[idx][:r.shardSize]
	_ = copy(r.allShards[idx], shard)

	// Now that the shard is updated with proper data,
	// attempt again to reconstruct the data.
	if err := r.rs.ReconstructData(r.allShards); err != nil {
		if errors.Is(err, reedsolomon.ErrTooFewShards) {
			// That internal error indicates we need more shards,
			// so return our gerasure error to satisfy our gerasure interfaces.
			return gerasure.ErrIncompleteSet
		}

		// For any other error, wrap it.
		return fmt.Errorf("failed to attempt data reconstruction: %w", err)
	}

	// No error in reconstruction,
	// so return nil to signal that the data is ready to consume.
	return nil
}

// Data satisfies [gerasure.Reconstructor].
func (r *Reconstructor) Data(dst []byte, dataSize int) ([]byte, error) {
	// The underlying encoder has a Join method that accepts an io.Writer.
	// That makes sense for large data that may not fit in memory,
	// but we are assuming that any turbine style block will trivially fit in memory.

	// First, decide if we can reuse the existing slice or if we need to allocate a new one.
	if cap(dst) < dataSize {
		dst = make([]byte, 0, dataSize)
	}

	// Next, we wrap dst in a bytes.Buffer so we have an io.Writer for the encoder.
	buf := bytes.NewBuffer(dst)

	if err := r.rs.Join(buf, r.allShards, dataSize); err != nil {
		return nil, fmt.Errorf("failed to write reconstructed data: %w", err)
	}

	// It was written successfully, so now we can return the buffer's underlying slice,
	// which is now a different slice from dst (due to having different length),
	// although it points at the same underlying array.
	//
	// And although it is generally incorrect to return a long-lived reference
	// to a bytes.Buffer's underlying slice,
	// there are no other references to the buffer so it is safe here.
	return buf.Bytes(), nil
}
