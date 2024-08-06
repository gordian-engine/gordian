package tmconsensus

import "github.com/rollchains/gordian/gcrypto"

// Block is the logical representation of a full block.
// The block may go through transformations,
// such as storing only hashes of validator sets rather than the longhand raw validator data,
// before writing to disk or sending across the network.
type Block struct {
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
	PrevCommitProof CommitProof

	// The validators for this block.
	Validators []Validator

	// The validators for the next block.
	NextValidators []Validator

	// ID of the data for this block.
	// The user-defined consensus strategy provides this ID,
	// and the application is responsible for retrieving the raw data belonging to the ID.
	//
	// The ID is typically, but not necessarily,
	// a cryptographic hash of the application data for the block
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
	Round uint32 // Necessary to derive signature content.

	PubKeyHash string // Required to validate sparse signatures.

	// Keyed by block hash, or an empty string for nil block.
	Proofs map[string][]gcrypto.SparseSignature
}

// CommittedBlock is a block and the proof that it was committed.
type CommittedBlock struct {
	Block Block
	Proof CommitProof
}

// ProposedBlock is the data sent by a proposer at the beginning of a round.
type ProposedBlock struct {
	// The block to consider committing.
	Block Block

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

// BlockFinalization is the set of data resulting from finalizing (read: evaluating) a block.
type BlockFinalization struct {
	// The block hash, for an unambiguous reference to which block is in consideration.
	BlockHash []byte

	// The round in which the block was finalized -- not necessarily the same round
	// in which it was proposed, if a validator was locked to a specific proposal.
	//
	// This is helpful when proposing a block,
	// to look up the corresponding RoundState and set up the proposed block's PrevCommitProof.
	Round uint32

	// The hash of the app state after finalizing the block.
	AppStateHash []byte

	// The next validators after evaluating the block.
	// If this is the finalization for height H,
	// this will be the NextValidators field for height H+1.
	NextNextValidators []Validator

	// The original NextValidators field on the block matching BlockHash.
	// Not overridable.
	// Offered as convenience for populating Validators and NextValidators
	// on the subsequent block.
	NextValidators []Validator
}

// Annotations are arbitrary data to associate with a [Block] or [ProposedBlock].
//
// The Driver annotations are set by the driver
// (that is, the low-level code providing the [ConsensusStrategy]).
// The User annotations are provided by the higher-level application.
type Annotations struct {
	User, Driver []byte
}
