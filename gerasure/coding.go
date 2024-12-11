package gerasure

import (
	"context"
	"errors"
)

// Encoder encodes data into a set of erasure-corrected shards of bytes.
// The precise set of byte slices returned,
// and which of them are required to reconstitute the original data,
// are determined by the implementation.
//
// Settings for the encoder are typically also applied to the [Reconstructor],
// but the source of those settings is out of scope of these interfaces.
type Encoder interface {
	Encode(ctx context.Context, data []byte) ([][]byte, error)
}

// Reconstructor manages reconstructing the original data
// from a series of erasure-coded shards,
// typically produced from an instance of [Encoder].
//
// This interface currently is focused on reconstructing only the data shards.
// In the future, we may expand the interface with a new method
// to repopulate any missing parity shards, if that proves useful.
type Reconstructor interface {
	// Reconstruct attempts to use the shard to reconstruct the original data,
	// and the returned error value indicates:
	//   - whether the shard was invalid, by an implementation-specific error
	//   - whether the shard was valid but insufficient to reconstruct the original data,
	//     by returning [ErrIncompleteSet]
	//   - or whether the shard was accepted and the data is able to be reconstructed
	//     with a call to Data, by returning nil.
	//
	// Some erasure coding schemes (such as Reed-Solomon) have an explicit index for each shard;
	// "rateless" erasure codes may ignore the index or may require that each call
	// uses an incremented index.
	//
	// Callers should preferably track which indices are passed in,
	// and ensure each index is only passed to ReconstructData once.
	ReconstructData(ctx context.Context, idx int, shard []byte) error

	// Data appends the reconstructed data to dst,
	// returning the modified dst slice if dst has sufficient capacity,
	// otherwise returning a newly allocated slice.
	//
	// The dataSize parameter is required because the reconstructor cannot
	// determine the size from shards alone,
	// as the final data shard may be padded with zeros.
	//
	// We are assuming for now that all erasure-coded data encountered will fit in memory;
	// if that assumption changes, we may add a new method to this interface
	// or add a separate interface altogether.
	//
	// Any error in producing the data may be returned directly,
	// with no wrapping by any gerasure errors.
	Data(dst []byte, dataSize int) ([]byte, error)
}

// ErrIncompleteSet is returned by [Reconstructor.RestructData] when a shard was accepted
// but was not sufficient to fully restore the original data.
var ErrIncompleteSet = errors.New("insufficient shard received to reconstruct data")
