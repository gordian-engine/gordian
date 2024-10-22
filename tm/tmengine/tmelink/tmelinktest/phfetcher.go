package tmelinktest

import (
	"github.com/gordian-engine/gordian/tm/tmconsensus"
	"github.com/gordian-engine/gordian/tm/tmengine/tmelink"
)

// PHFetcher can be used in tests where a [tmelink.ProposedHeaderFetcher] is needed.
type PHFetcher struct {
	// Bidirectional request channel,
	// so tests can inspect requests.
	ReqCh chan tmelink.ProposedHeaderFetchRequest

	// Bidirectional output channel,
	// so tests can send responses.
	FetchedCh chan tmconsensus.ProposedHeader
}

func NewPHFetcher(reqSz, fetchedSz int) PHFetcher {
	return PHFetcher{
		ReqCh:     make(chan tmelink.ProposedHeaderFetchRequest, reqSz),
		FetchedCh: make(chan tmconsensus.ProposedHeader, fetchedSz),
	}
}

// ProposedHeaderFetcher returns a ProposedHeaderFetcher struct,
// as the engine and its internal components expect.
func (f PHFetcher) ProposedHeaderFetcher() tmelink.ProposedHeaderFetcher {
	return tmelink.ProposedHeaderFetcher{
		FetchRequests:          f.ReqCh,
		FetchedProposedHeaders: f.FetchedCh,
	}
}
