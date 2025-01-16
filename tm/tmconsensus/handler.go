package tmconsensus

import (
	"context"

	"github.com/gordian-engine/gordian/gexchange"
)

// ConsensusHandler is the interface to handle the set of consensus messages.
//
// In production this will be a [tmgossip.Strategy] value.
type ConsensusHandler interface {
	HandleProposedHeader(context.Context, ProposedHeader) gexchange.Feedback
	HandlePrevoteProofs(context.Context, PrevoteSparseProof) gexchange.Feedback
	HandlePrecommitProofs(context.Context, PrecommitSparseProof) gexchange.Feedback
}

// FineGrainedConsensusHandler is the preferred interface (over [ConsensusHandler])
// for handling incoming p2p consensus messages.
//
// Use one of the FeedbackMapper types to convert a FineGrainedConsensusHandler into a ConsensusHandler.
type FineGrainedConsensusHandler interface {
	HandleProposedHeader(context.Context, ProposedHeader) HandleProposedHeaderResult
	HandlePrevoteProofs(context.Context, PrevoteSparseProof) HandleVoteProofsResult
	HandlePrecommitProofs(context.Context, PrecommitSparseProof) HandleVoteProofsResult
}

// HandleProposedHeaderResult is a set of constants
// to be returned from a FineGrainedConsensusHandler's HandleProposedHeader method.
type HandleProposedHeaderResult uint8

//go:generate go run golang.org/x/tools/cmd/stringer -type HandleProposedHeaderResult -trimprefix=HandleProposedHeader .
const (
	// Keep zero value invalid, so return 0 is somewhat meaningful.
	_ HandleProposedHeaderResult = iota

	// Everything checked out -- this was a new proposed block added to our store.
	HandleProposedHeaderAccepted

	// We already stored a copy of this proposed block.
	HandleProposedHeaderAlreadyStored

	// The signer of the proposed block did not match a validator in the current round.
	HandleProposedHeaderSignerUnrecognized

	// Our calculation of the block hash was different from what the block reported.
	HandleProposedHeaderBadBlockHash

	// Signature verification on the proposed header failed.
	HandleProposedHeaderBadSignature

	// ProposerPubKey field was not populated.
	HandleProposedHeaderMissingProposerPubKey

	// Something was wrong with the header's PrevCommitProof.
	// It could be a validator pub key mismatch,
	// an invalid signature in the proof,
	// or an incorrect amount of votes (e.g. less than majority for the committing block).
	HandleProposedHeaderBadPrevCommitProofPubKeyHash
	HandleProposedHeaderBadPrevCommitProofSignature
	HandleProposedHeaderBadPrevCommitProofDoubleSigned
	HandleProposedHeaderBadPrevCommitVoteCount

	// Proposed block had older height or round than our current view of the world.
	HandleProposedHeaderRoundTooOld

	// Proposed block is beyond our NextHeight and/or NextRound handlers.
	HandleProposedHeaderRoundTooFarInFuture

	// Internal error not necessarily correlated with the actual proposed block.
	HandleProposedHeaderInternalError
)

// HandleVoteProofsResult is a set of constants
// to be returned from a FineGrainedConsensusHandler's HandlePrevoteProofs and HandlePrecommitProofs methods.
type HandleVoteProofsResult uint8

//go:generate go run golang.org/x/tools/cmd/stringer -type HandleVoteProofsResult -trimprefix=HandleVoteProofs .
const (
	_ HandleVoteProofsResult = iota

	// Proofs were added to the store.
	HandleVoteProofsAccepted

	// We already had all the signatures in the given proof.
	HandleVoteProofsNoNewSignatures

	// There were no proofs in the message.
	// (This should only happen on messages from a misbehaving peer.)
	HandleVoteProofsEmpty

	// The public key hash did not match what we expected for the given height and round.
	HandleVoteProofsBadPubKeyHash

	// Votes had older height or round than our current view of the world.
	HandleVoteProofsRoundTooOld

	// Vote is beyond our NextHeight and/or NextRound handlers.
	HandleVoteProofsTooFarInFuture

	// Internal error not necessarily correlated with the actual prevote proof.
	HandleVoteProofsInternalError
)
