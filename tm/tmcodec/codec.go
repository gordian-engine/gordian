package tmcodec

import (
	"github.com/gordian-engine/gordian/tm/tmconsensus"
)

type Marshaler interface {
	MarshalConsensusMessage(ConsensusMessage) ([]byte, error)

	MarshalHeader(tmconsensus.Header) ([]byte, error)
	MarshalProposedHeader(tmconsensus.ProposedHeader) ([]byte, error)
	MarshalCommittedHeader(tmconsensus.CommittedHeader) ([]byte, error)

	MarshalPrevoteProof(tmconsensus.PrevoteSparseProof) ([]byte, error)
	MarshalPrecommitProof(tmconsensus.PrecommitSparseProof) ([]byte, error)
}

type Unmarshaler interface {
	UnmarshalConsensusMessage([]byte, *ConsensusMessage) error

	UnmarshalHeader([]byte, *tmconsensus.Header) error
	UnmarshalProposedHeader([]byte, *tmconsensus.ProposedHeader) error
	UnmarshalCommittedHeader([]byte, *tmconsensus.CommittedHeader) error

	UnmarshalPrevoteProof([]byte, *tmconsensus.PrevoteSparseProof) error
	UnmarshalPrecommitProof([]byte, *tmconsensus.PrecommitSparseProof) error
}

// MarshalCodec marshals and unmarshals tmconsensus values, producing byte slices.
// In the future we may have a plain Codec type that operates against an io.Writer.
type MarshalCodec interface {
	Marshaler
	Unmarshaler
}

// ConsensusMessage is a wrapper around the three types of consensus values sent during rounds.
// Exactly one of the fields must be set.
// If zero or multiple fields are set, behavior is undefined.
type ConsensusMessage struct {
	ProposedHeader *tmconsensus.ProposedHeader

	PrevoteProof   *tmconsensus.PrevoteSparseProof
	PrecommitProof *tmconsensus.PrecommitSparseProof
}
