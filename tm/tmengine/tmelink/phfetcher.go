package tmelink

import (
	"context"

	"github.com/gordian-engine/gordian/tm/tmconsensus"
)

// ProposedHeaderFetcher contains the input and output channels to fetch proposed headers.
// The engine uses this when there are sufficient votes for a proposed header,
// and the engine does not have the proposed header.
type ProposedHeaderFetcher struct {
	// FetchRequests is the channel for the engine
	// to send requests to fetch a proposed header
	// at a given height and with a given hash.
	//
	// Once a proposed header matching the height and hash has been found,
	// that header is sent to the FetchedProposedHeaders channel.
	//
	// The ProposedHeaderFetchRequest struct has a context field.
	// The context is associated with the fetch for this particular header.
	// Canceling the context will abort any in-progress requests to find the header.
	// If the context is cancelled after the proposed header has been enqueued into FetchedProposedHeaders,
	// there is no effect.
	//
	// A ProposedHeaderFetcher should have an upper limit on the number of outstanding fetch requests.
	// If the number of in-flight requests is at its limit,
	// the send to this channel will block.
	FetchRequests chan<- ProposedHeaderFetchRequest

	// FetchedProposedHeaders is the single channel that sends any header
	// discovered as a result of a call to FetchProposedHeader.
	FetchedProposedHeaders <-chan tmconsensus.ProposedHeader
}

// ProposedHeaderFetchRequest is used to make requests to fetch missed proposed headers.
type ProposedHeaderFetchRequest struct {
	// Context associated with the request.
	// Canceling this context will abort the request, if it is still in-flight.
	Ctx context.Context

	// The height to search for the proposed header.
	Height uint64

	// The block hash of the header to search for.
	BlockHash string
}
