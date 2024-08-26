package tmstore

import (
	"context"

	"github.com/rollchains/gordian/tm/tmconsensus"
)

// HeaderStore is the store that the Engine's Mirror uses for committed block headers.
// The committed headers always lag the voting round by two heights.
type HeaderStore interface {
	SaveHeader(ctx context.Context, ch tmconsensus.CommittedHeader) error

	LoadHeader(ctx context.Context, height uint64) (tmconsensus.CommittedHeader, error)
}