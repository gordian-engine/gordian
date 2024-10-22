package tmdebug

import (
	"context"
	"log/slog"

	"github.com/gordian-engine/gordian/gexchange"
	"github.com/gordian-engine/gordian/internal/glog"
	"github.com/gordian-engine/gordian/tm/tmconsensus"
)

// LoggingConsensusHandler logs the inputs to the [tmconsensus.ConsensusHandler] interface
// and delegates to the configured Handler.
type LoggingConsensusHandler struct {
	Log *slog.Logger

	Handler tmconsensus.ConsensusHandler
}

func (h LoggingConsensusHandler) HandleProposedHeader(ctx context.Context, ph tmconsensus.ProposedHeader) gexchange.Feedback {
	log := h.Log.With(
		"height", ph.Header.Height,
		"round", ph.Round,
		"block_hash", glog.Hex(ph.Header.Hash),
	)

	log.Info("Handling proposed header")

	f := h.Handler.HandleProposedHeader(ctx, ph)

	log.Info("Handled proposed header", "feedback", f.String())

	return f
}

func (h LoggingConsensusHandler) HandlePrevoteProofs(ctx context.Context, p tmconsensus.PrevoteSparseProof) gexchange.Feedback {
	log := h.Log.With(
		"height", p.Height,
		"round", p.Round,
		"pubkey_hash", glog.Hex(p.PubKeyHash),
		"n_blocks", len(p.Proofs),
	)

	log.Info("Handling prevote proofs")

	f := h.Handler.HandlePrevoteProofs(ctx, p)

	log.Info("Handled prevote proofs", "feedback", f.String())

	return f
}

func (h LoggingConsensusHandler) HandlePrecommitProofs(ctx context.Context, p tmconsensus.PrecommitSparseProof) gexchange.Feedback {
	log := h.Log.With(
		"height", p.Height,
		"round", p.Round,
		"pubkey_hash", glog.Hex(p.PubKeyHash),
		"n_blocks", len(p.Proofs),
	)

	log.Info("Handling precommit proofs")

	f := h.Handler.HandlePrecommitProofs(ctx, p)

	log.Info("Handled precommit proofs", "feedback", f.String())

	return f
}

// LoggingFineGrainedConsensusHandler logs the inputs
// to the [tmconsensus.FineGrainedConsensusHandler] interface
// and delegates to the configured Handler.
type LoggingFineGrainedConsensusHandler struct {
	Log *slog.Logger

	Handler tmconsensus.FineGrainedConsensusHandler
}

func (h LoggingFineGrainedConsensusHandler) HandleProposedHeader(ctx context.Context, ph tmconsensus.ProposedHeader) tmconsensus.HandleProposedHeaderResult {
	log := h.Log.With(
		"height", ph.Header.Height,
		"round", ph.Round,
		"block_hash", glog.Hex(ph.Header.Hash),
	)

	log.Info("Handling proposed header")

	r := h.Handler.HandleProposedHeader(ctx, ph)

	log.Info("Handled proposed header", "result", r.String())

	return r
}

func (h LoggingFineGrainedConsensusHandler) HandlePrevoteProofs(ctx context.Context, p tmconsensus.PrevoteSparseProof) tmconsensus.HandleVoteProofsResult {
	log := h.Log.With(
		"height", p.Height,
		"round", p.Round,
		"pubkey_hash", glog.Hex(p.PubKeyHash),
		"n_blocks", len(p.Proofs),
	)

	log.Info("Handling prevote proofs")

	r := h.Handler.HandlePrevoteProofs(ctx, p)

	log.Info("Handled prevote proofs", "result", r.String())

	return r
}

func (h LoggingFineGrainedConsensusHandler) HandlePrecommitProofs(ctx context.Context, p tmconsensus.PrecommitSparseProof) tmconsensus.HandleVoteProofsResult {
	log := h.Log.With(
		"height", p.Height,
		"round", p.Round,
		"pubkey_hash", glog.Hex(p.PubKeyHash),
		"n_blocks", len(p.Proofs),
	)

	log.Info("Handling precommit proofs")

	r := h.Handler.HandlePrecommitProofs(ctx, p)

	log.Info("Handled precommit proofs", "result", r.String())

	return r
}
