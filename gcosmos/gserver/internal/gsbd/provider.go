package gsbd

import (
	"context"

	"cosmossdk.io/core/transaction"
)

// Provider has one method to provide block data.
type Provider interface {
	// Make the provided transactions available for retrieval.
	// The dataID return value should be set as the DataID in a [tmconsensus.Proposal].
	// The addrs can be included in the proposal's driver annotation
	// to indicate to the other validators where to retrieve the data.
	Provide(ctx context.Context, height uint64, round uint32, txs []transaction.Tx) (
		ProvideResult, error,
	)
}

// ProvideResult is the return value from [Provider.Provide].
type ProvideResult struct {
	// DataID should be set at the DataID field in a [tmconsensus.Proposal].
	DataID string

	// The DataSize indicates the size of the uncompressed,
	// serialized transactions.
	DataSize int

	// Addrs is the list of locations where the data has been provided.
	Addrs []Location
}

// Location indicates where a particular instance of block data is hosted.
type Location struct {
	// Scheme indicates the method of hosting the data.
	Scheme Scheme

	// Addr is the scheme-specific connection string.
	// This is usually only the address of the host;
	// the scheme and the data ID together imply the full connection string.
	Addr string
}

type Scheme uint8

const (
	InvalidScheme Scheme = 0

	Libp2pScheme Scheme = 1
)
