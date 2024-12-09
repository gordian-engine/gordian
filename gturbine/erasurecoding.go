package gturbine

import (
	"context"
	"errors"
)

// Encoder encodes data into a set of erasure-corrected shards of bytes.
// The precise set of byte slices returned,
// and which of them are required to reconstitute the original data,
// are determined by the implementation.
type Encoder interface {
	Encode(ctx context.Context, data []byte) ([][]byte, error)
}

// Reconstructor manages reconstructing the original data
// from a series of erasure-coded shards,
// typically produced from an instance of [Encoder].
//
// This interface currently is focused on reconstructing only the data shards.
// In the future, we may expand the interface with a new method
// to repopulate any missing parity shards,
// if that proves useful.
// (This would presumably be more efficient with the possibility
// that we already have some parity shards populated.)
type Reconstructor interface {
	// Reconstruct attempts to use the shard to reconstruct the original data.
	//
	// Some erasure coding schemes (such as Reed Solomon) have an explicit index for each shard;
	// "rateless" erasure codes may ignore the index or may require that each call
	// uses an incremented index.
	//
	// If the shard was accepted properly and the data is able to be reconstructed,
	// Reconstruct returns nil.
	// If the shard was accepted but more shards are required,
	// Reconstruct returns [ErrIncompleteSet].
	// Otherwise, an implementation-specific error is returned.
	ReconstructData(ctx context.Context, idx int, shard []byte) error

	// Data appends the reconstructed data to dst,
	// returning the modified dst slice if dst has sufficient capacity,
	// otherwise returning a newly allocated slice.
	//
	// Any error in producing the data may be returned directly,
	// with no wrapping by any gturbine errors.
	Data(dst []byte, dataSize int) ([]byte, error)
}

// ErrIncompleteSet is returned by [Reconstructor.Restore] when a shard was accepted
// but was not sufficient to fully restore the original data.
var ErrIncompleteSet = errors.New("insufficient shard received to reconstruct data")
