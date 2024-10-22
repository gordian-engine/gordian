package tmstore

import (
	"context"

	"github.com/gordian-engine/gordian/tm/tmconsensus"
)

// CommittedHeaderStore is the store that the Engine's Mirror uses for committed block headers.
// The committed headers always lag the voting round by one height.
// The subsequent header's PrevCommitProof field is the canonical proof.
// But while the engine is still voting on that height,
// the [RoundStore] contains the most up to date previous commit proof for the committing header.
//
// The proofs associated with the committed headers must be considered subjective.
// That is, while the proof is expected to represent >2/3 voting power for the header,
// it is not necessarily the same set of signatures
// as in the subsequent block's PrevCommitProof field.
type CommittedHeaderStore interface {
	SaveCommittedHeader(ctx context.Context, ch tmconsensus.CommittedHeader) error

	LoadCommittedHeader(ctx context.Context, height uint64) (tmconsensus.CommittedHeader, error)
}
