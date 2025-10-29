package tmstore

import (
	"context"

	"github.com/gordian-engine/gordian/gcrypto"
	"github.com/gordian-engine/gordian/tm/tmconsensus"
)

// ActionStore stores the active actions the current state machine and application take;
// specifically, proposed blocks, prevotes, and precommits.
type ActionStore interface {
	SaveProposedHeaderAction(context.Context, tmconsensus.ProposedHeader) error

	SavePrevoteAction(ctx context.Context, pubKey gcrypto.PubKey, vt tmconsensus.VoteTarget, sig []byte) error
	SavePrecommitAction(ctx context.Context, pubKey gcrypto.PubKey, vt tmconsensus.VoteTarget, sig []byte) error

	// LoadActions returns all actions recorded for this round.
	//
	// If there are no actions stored for the given round,
	// the store must return [tmconsensus.RoundUnknownError].
	LoadActions(ctx context.Context, height uint64, round uint32) (RoundActions, error)
}

// RoundActions contains all three possible actions the current validator
// may have taken for a single round.
type RoundActions struct {
	Height uint64
	Round  uint32

	ProposedHeader tmconsensus.ProposedHeader

	PubKey gcrypto.PubKey

	PrevoteTarget    string // Block hash or empty string for nil.
	PrevoteSignature string // Immutable signature.

	PrecommitTarget    string // Block hash or empty string for nil.
	PrecommitSignature string // Immutable signature.
}
