package tmconsensustest

import (
	"context"
	"sync"

	"github.com/gordian-engine/gordian/gexchange"
	"github.com/gordian-engine/gordian/tm/tmconsensus"
)

// ChannelConsensusHandler is a [tmconsensus.ConsensusHandler]
// that emits messages to a set of channels.
//
// This is useful in tests where you have a "client-only" connection
// and you want to observe messages sent to the network,
// without interfering with any individual engine.
type ChannelConsensusHandler struct {
	incomingProposals chan tmconsensus.ProposedHeader

	incomingPrevoteProofs   chan tmconsensus.PrevoteSparseProof
	incomingPrecommitProofs chan tmconsensus.PrecommitSparseProof

	closeOnce sync.Once
}

// NewChannelConsensusHandler returns a ChannelConsensusHandler
// whose channels are all sized according to bufSize.
func NewChannelConsensusHandler(bufSize int) *ChannelConsensusHandler {
	return &ChannelConsensusHandler{
		incomingProposals: make(chan tmconsensus.ProposedHeader, bufSize),

		incomingPrevoteProofs:   make(chan tmconsensus.PrevoteSparseProof, bufSize),
		incomingPrecommitProofs: make(chan tmconsensus.PrecommitSparseProof, bufSize),
	}
}

// HandleProposedProposedHeader implements [tmconsensus.ConsensusHandler].
func (h *ChannelConsensusHandler) HandleProposedHeader(ctx context.Context, ph tmconsensus.ProposedHeader) gexchange.Feedback {
	select {
	case h.incomingProposals <- ph:
		return gexchange.FeedbackAccepted
	case <-ctx.Done():
		return gexchange.FeedbackIgnored
	}
}

func (h *ChannelConsensusHandler) HandlePrevoteProofs(ctx context.Context, p tmconsensus.PrevoteSparseProof) gexchange.Feedback {
	select {
	case h.incomingPrevoteProofs <- p:
		return gexchange.FeedbackAccepted
	case <-ctx.Done():
		return gexchange.FeedbackIgnored
	}
}

func (h *ChannelConsensusHandler) HandlePrecommitProofs(ctx context.Context, p tmconsensus.PrecommitSparseProof) gexchange.Feedback {
	select {
	case h.incomingPrecommitProofs <- p:
		return gexchange.FeedbackAccepted
	case <-ctx.Done():
		return gexchange.FeedbackIgnored
	}
}

// IncomingProposals returns a channel of the values that were passed to HandleProposedHeader.
func (h *ChannelConsensusHandler) IncomingProposals() <-chan tmconsensus.ProposedHeader {
	return h.incomingProposals
}

// IncomingPrecommitProofs returns a channel of the values that were passed to HandlePrecommitProof.
func (h *ChannelConsensusHandler) IncomingPrecommitProofs() <-chan tmconsensus.PrecommitSparseProof {
	return h.incomingPrecommitProofs
}

// IncomingPrevoteProofs returns a channel of the values that were passed to HandlePrevoteProof.
func (h *ChannelConsensusHandler) IncomingPrevoteProofs() <-chan tmconsensus.PrevoteSparseProof {
	return h.incomingPrevoteProofs
}

// Close closes h.
// It is safe to call Close multiple times.
func (h *ChannelConsensusHandler) Close() {
	h.closeOnce.Do(func() {
		close(h.incomingProposals)
	})
}
