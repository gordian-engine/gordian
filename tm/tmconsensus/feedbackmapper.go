package tmconsensus

import (
	"context"
	"fmt"

	"github.com/gordian-engine/gordian/gexchange"
)

// AcceptAllValidFeedbackMapper converts a [FineGrainedConsensusHandler]
// to a [ConsensusHandler], accepting any valid input,
// even if the input was already known.
type AcceptAllValidFeedbackMapper struct {
	Handler FineGrainedConsensusHandler
}

func (m AcceptAllValidFeedbackMapper) HandleProposedHeader(
	ctx context.Context, ph ProposedHeader,
) gexchange.Feedback {
	f := m.Handler.HandleProposedHeader(ctx, ph)
	switch f {
	case HandleProposedHeaderAccepted,
		HandleProposedHeaderAlreadyStored:
		return gexchange.FeedbackAccepted

	case HandleProposedHeaderRoundTooOld,
		HandleProposedHeaderInternalError:
		return gexchange.FeedbackIgnored

	case HandleProposedHeaderSignerUnrecognized,
		HandleProposedHeaderBadSignature,
		HandleProposedHeaderBadBlockHash,
		HandleProposedHeaderBadPrevCommitProofPubKeyHash,
		HandleProposedHeaderBadPrevCommitProofSignature,
		HandleProposedHeaderBadPrevCommitVoteCount:
		return gexchange.FeedbackRejected

	default:
		panic(fmt.Errorf("BUG: no HandleProposedHeaderResult mapping set for %s", f))
	}
}

func (m AcceptAllValidFeedbackMapper) HandlePrevoteProofs(
	ctx context.Context, p PrevoteSparseProof,
) gexchange.Feedback {
	f := m.Handler.HandlePrevoteProofs(ctx, p)
	return m.mapVoteResult(f, "HandlePrevoteProofs")
}

func (m AcceptAllValidFeedbackMapper) HandlePrecommitProofs(
	ctx context.Context, p PrecommitSparseProof,
) gexchange.Feedback {
	f := m.Handler.HandlePrecommitProofs(ctx, p)
	return m.mapVoteResult(f, "HandlePrecommitProofs")
}

func (m AcceptAllValidFeedbackMapper) mapVoteResult(
	f HandleVoteProofsResult, name string,
) gexchange.Feedback {
	switch f {
	case HandleVoteProofsNoNewSignatures,
		HandleVoteProofsAccepted:
		return gexchange.FeedbackAccepted

	case HandleVoteProofsRoundTooOld,
		HandleVoteProofsInternalError:
		return gexchange.FeedbackIgnored

	case HandleVoteProofsEmpty,
		HandleVoteProofsBadPubKeyHash:
		return gexchange.FeedbackRejected

	default:
		panic(fmt.Errorf("BUG: no %s mapping set for %s", name, f))
	}
}

// DropDuplicateFeedbackMapper is a [Handler] that wraps a FineGrainedConsensusHandler
// that ignores proposed block messages if we already have the proposed block
// and ignores vote messages if they do not increase existing vote knowledge.
type DropDuplicateFeedbackMapper struct {
	Handler FineGrainedConsensusHandler
}

func (m DropDuplicateFeedbackMapper) HandleProposedHeader(
	ctx context.Context, ph ProposedHeader,
) gexchange.Feedback {
	f := m.Handler.HandleProposedHeader(ctx, ph)
	switch f {
	case HandleProposedHeaderAccepted:
		return gexchange.FeedbackAccepted

	case HandleProposedHeaderRoundTooOld,
		HandleProposedHeaderInternalError,
		HandleProposedHeaderAlreadyStored:
		return gexchange.FeedbackIgnored

	case HandleProposedHeaderSignerUnrecognized,
		HandleProposedHeaderBadSignature,
		HandleProposedHeaderBadBlockHash,
		HandleProposedHeaderBadPrevCommitProofPubKeyHash,
		HandleProposedHeaderBadPrevCommitProofSignature,
		HandleProposedHeaderBadPrevCommitVoteCount:
		return gexchange.FeedbackRejected

	default:
		panic(fmt.Errorf("BUG: no HandleProposedHeaderResult mapping set for %s", f))
	}
}

func (m DropDuplicateFeedbackMapper) HandlePrevoteProofs(
	ctx context.Context, p PrevoteSparseProof,
) gexchange.Feedback {
	f := m.Handler.HandlePrevoteProofs(ctx, p)
	return m.mapVoteResult(f, "HandlePrevoteProofs")
}

func (m DropDuplicateFeedbackMapper) HandlePrecommitProofs(
	ctx context.Context, p PrecommitSparseProof,
) gexchange.Feedback {
	f := m.Handler.HandlePrecommitProofs(ctx, p)
	return m.mapVoteResult(f, "HandlePrecommitProofs")
}

func (m DropDuplicateFeedbackMapper) mapVoteResult(
	f HandleVoteProofsResult, name string,
) gexchange.Feedback {
	switch f {
	case HandleVoteProofsAccepted:
		return gexchange.FeedbackAccepted

	case HandleVoteProofsRoundTooOld,
		HandleVoteProofsNoNewSignatures,
		HandleVoteProofsInternalError:
		return gexchange.FeedbackIgnored

	case HandleVoteProofsEmpty,
		HandleVoteProofsBadPubKeyHash:
		return gexchange.FeedbackRejected

	default:
		panic(fmt.Errorf("BUG: no %s mapping set for %s", name, f))
	}
}
