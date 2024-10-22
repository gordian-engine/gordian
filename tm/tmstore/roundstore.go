package tmstore

import (
	"context"

	"github.com/gordian-engine/gordian/tm/tmconsensus"
)

// RoundStore stores and retrieves the proposed headers, prevotes, and precommits
// observed during each round.
type RoundStore interface {
	// SaveRoundProposedHeader saves the given proposed block header
	// as a candidate proposed header in the given height and round.
	SaveRoundProposedHeader(ctx context.Context, ph tmconsensus.ProposedHeader) error

	// SaveRoundReplayedHeader saves the header as one
	// that is about to be committed in the given height,
	// due to mirror catchup.
	//
	// In the normal mirror flow, the replayed header is saved,
	// and then OverwriteRoundPrecommitProofs is called.
	SaveRoundReplayedHeader(ctx context.Context, h tmconsensus.Header) error

	// The overwrite proofs methods overwrite existing entries
	// for the corresponding proof at the given height and round.
	// TODO: these methods should both accept sparse proofs,
	// as sparse proofs are more suited to storage.
	OverwriteRoundPrevoteProofs(
		ctx context.Context,
		height uint64,
		round uint32,
		proofs tmconsensus.SparseSignatureCollection,
	) error
	OverwriteRoundPrecommitProofs(
		ctx context.Context,
		height uint64,
		round uint32,
		proofs tmconsensus.SparseSignatureCollection,
	) error

	// LoadRoundState returns the saved proposed blocks and votes
	// for the given height and round.
	// The order of the proposed blocks in the pbs slice is undefined
	// and may differ from one call to another.
	//
	// Note that in the event of replayed blocks during a mirror catchup,
	// there may be ProposedHeader values without its PubKey, Signature, or Annotations fields set.
	//
	// If there are no proposed blocks or votes at the given height and round,
	// [tmconsensus.RoundUnknownError] is returned.
	// If at least one proposed block, prevote, or precommit exists at the height and round,
	// a nil error is returned.
	LoadRoundState(ctx context.Context, height uint64, round uint32) (
		phs []tmconsensus.ProposedHeader,
		prevotes, precommits tmconsensus.SparseSignatureCollection,
		err error,
	)
}
