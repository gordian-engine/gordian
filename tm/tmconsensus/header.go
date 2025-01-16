package tmconsensus

import (
	"bytes"

	"github.com/gordian-engine/gordian/gcrypto"
)

// Header is the logical representation of a block header.
// The header may go through transformations,
// such as storing only hashes of validator sets rather than the longhand raw validator data,
// before writing to disk or sending across the network.
type Header struct {
	// Determined based on all the other fields.
	// Derived through a [HashScheme] method.
	Hash []byte

	// Hash of the previous block.
	PrevBlockHash []byte

	// Height of this block.
	Height uint64

	// PrevCommitProof is the proof for the previous committed block,
	// where there may be precommits for other blocks
	// besides the committed one and nil.
	//
	// The consensus engine determines the format of this proof.
	// Non-aggregating signatures will typically be a discrete set of signatures,
	// but aggregating signatures will usually undergo a final aggregation,
	// which is incompatible with the form used for consensus gossip.
	PrevCommitProof CommitProof

	// The validators for this block and the next block.
	ValidatorSet, NextValidatorSet ValidatorSet

	// ID of the data for this block.
	// The user-defined consensus strategy provides this ID,
	// and the driver is responsible for retrieving the raw data belonging to the ID.
	//
	// The ID is typically, but not necessarily,
	// a cryptographic hash of the application data for the block.
	DataID []byte

	// The hash of the app state as a result of executing the previous block.
	// Deriving this hash is an application-level concern.
	PrevAppStateHash []byte

	// Arbitrary data to associate with the block.
	// Unlike the annotations on a proposed block, these values are persisted to chain.
	// The values must be respected in the block's hash.
	//
	// Low-level driver code may set the Annotations.Driver field,
	// while on-chain code may set the Annotations.User field.
	//
	// One example of a use case for a block annotation from the driver
	// could be to include a timestamp with the block.
	// One contrived example for the user annotation could be
	// including the version of the application,
	// in order to reject blocks that don't match the version.
	Annotations Annotations
}

// CommitProof is the commit proof for a block.
type CommitProof struct {
	// Necessary to verify signature content.
	Round uint32

	// The hash of the ordered collection of public keys
	// of the validators at the height where the commit proof occurred.
	// Derived through a [HashScheme] method.
	PubKeyHash string

	// Keyed by block hash, or an empty string for nil block.
	Proofs map[string][]gcrypto.SparseSignature
}

// Clone returns a new copy of CommitProof with identical values
// but without any references to p.
func (p CommitProof) Clone() CommitProof {
	cloneProofs := make(map[string][]gcrypto.SparseSignature, len(p.Proofs))
	for hash, sigs := range p.Proofs {
		cloneSigs := make([]gcrypto.SparseSignature, len(sigs))
		for i, sig := range sigs {
			cloneSigs[i] = gcrypto.SparseSignature{
				KeyID: bytes.Clone(sig.KeyID),
				Sig:   bytes.Clone(sig.Sig),
			}
		}

		cloneProofs[hash] = cloneSigs
	}

	return CommitProof{
		Round:      p.Round,
		PubKeyHash: p.PubKeyHash,
		Proofs:     cloneProofs,
	}
}

// CommittedHeader is a header and the proof that it was committed.
type CommittedHeader struct {
	Header Header
	Proof  CommitProof
}

// ProposedHeader is the data sent by a proposer at the beginning of a round.
// This is the logical representation within the engine,
// not necessarily an exact representation of the data sent across the network.
type ProposedHeader struct {
	// The header of the block to consider committing.
	Header Header

	// The round in which this block was proposed.
	Round uint32

	// The public key of the proposer.
	// Used to verify the signature.
	ProposerPubKey gcrypto.PubKey

	// Arbitrary data to associate with the proposed block.
	// The annotations are considered when producing the proposed block's signature,
	// but they are otherwise not persisted to chain.
	// (Of course, off-chain utilities like indexers may persist the data off-chain.)
	//
	// Low-level driver code may set the Annotations.Driver field,
	// while on-chain code may set the Annotations.User field.
	//
	// The [ConsensusStrategy] may directly set either of those fields
	// when it provides a proposed block to the engine.
	//
	// One example of a use case for a proposed block annotation
	// would be for the engine to include its network address or p2p ID
	// as proof that the particular validator is associated with a particular peer.
	// There would be no need for that to be stored on chain,
	// but it would be potentially relevant to other live validators on the network.
	Annotations Annotations

	// Signature of the proposer.
	// The signing content is determined by the engine's [SignatureScheme].
	Signature []byte
}

// Annotations are arbitrary data to associate with a [Header] or [ProposedHeader].
//
// The Driver annotations are set by the driver
// (that is, the low-level code providing the [ConsensusStrategy]).
// The User annotations are provided by the higher-level application.
type Annotations struct {
	User, Driver []byte
}
