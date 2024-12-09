package gtreedsolomon

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"github.com/gordian-engine/gordian/gturbine"
	"github.com/klauspost/reedsolomon"
)

type Reconstructor struct {
	rs reedsolomon.Encoder

	// allShards is allocated at the correct size,
	// and with optimized byte alignment,
	// reflecting all possible data and parity shards.
	allShards [][]byte

	// haveShards is a subset of allShards.
	// Every time we receive a valid shard, we update haveShards to reference the same value in allShards.
	// TODO: we should be able to do away with haveShards and just take the zero-length slice
	// of every instance of allShards.
	haveShards [][]byte

	shardSize int
}

// NewReconstructor returns a new Reconstructor.
// The options within the given reedsolomon.Encoder determine the number of shards.
// The shardSize and totalDataSize must be discovered out of band;
func NewReconstructor(rs reedsolomon.Encoder, shardSize int) *Reconstructor {
	allShards := rs.(reedsolomon.Extensions).AllocAligned(shardSize)

	haveShards := make([][]byte, len(allShards))
	for i, realShard := range allShards {
		// By setting the have shard to a zero length slice aliasing the all shard,
		// when we eventually call ReconstructData, we can use that
		// already-allocated and already-aligned slice.
		haveShards[i] = realShard[:0]
	}

	return &Reconstructor{
		rs:         rs,
		allShards:  allShards,
		haveShards: haveShards,

		shardSize: shardSize,
	}
}

// ReconstructData satisfies [gturbine.Reconstructor].
func (r *Reconstructor) ReconstructData(_ context.Context, idx int, chunk []byte) error {
	if len(chunk) != r.shardSize {
		panic(fmt.Errorf(
			"BUG: attempted to reconstruct with invalid chunk size: want %d, got %d",
			r.shardSize, len(chunk),
		))
	}
	_ = copy(r.allShards[idx], chunk)
	r.haveShards[idx] = r.allShards[idx]

	// Now to attempt to reconstruct data,
	// we have to pass only the shards for which we actually have data.
	// We have been setting haveShards to zero-length slices from allShards,
	// so now as the call to ReconstructData populates haveShards,
	// it actually fills in allShards.
	if err := r.rs.ReconstructData(r.haveShards); err != nil {
		if errors.Is(err, reedsolomon.ErrTooFewShards) {
			// That internal error indicates we need more shards,
			// so return our gturbine error to satisfy our gturbine interfaces.
			return gturbine.ErrIncompleteSet
		}

		// For any other error, wrap it.
		return fmt.Errorf("failed to attempt data reconstruction: %w", err)
	}

	// No error in reconstruction,
	// so return nil to signal that the data is ready to consume.
	return nil
}

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
