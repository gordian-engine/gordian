package gcrypto

import (
	"github.com/bits-and-blooms/bitset"
)

// CommonMessageSignatureProof manages a mapping of signatures to public keys against a single common message.
// Constructors for instances of CommonMessageSignatureProof should accept a "candidate public keys" slice
// as the Signed method returns a bit set indicating the indices of those candidate values
// whose signatures we have accepted and validated.
//
// This is intended primarily for checking validator signatures,
// when validators are each signing an identical message.
type CommonMessageSignatureProof interface {
	// Message is the value being signed in this proof.
	// It is assumed that one proof contains signatures representing one or many public keys,
	// all for the same message.
	//
	// Depending on its configuration, the engine may aggregate
	// different signature proofs, for different messages,
	// into a single, multi-message proof when serializing a block.
	Message() []byte

	// PubKeyHash is an implementation-specific hash across all the candidate keys,
	// to be used as a quick check whether two independent proofs
	// reference the same set of validators.
	//
	// Note, in the future, the algorithm for determining a candidate key hash
	// will probably fall upon a new Scheme definition.
	PubKeyHash() []byte

	// AddSignature adds a signature representing a single key.
	//
	// This should only be called when receiving the local application's signature for a message.
	// Otherwise, use the Merge method to combine incoming proofs with the existing one.
	//
	// If the signature does not match, or if the public key was not one of the candidate keys,
	// an error is returned.
	AddSignature(sig []byte, key PubKey) error

	// Matches reports whether the other proof references the same message and keys
	// as the current proof.
	//
	// Matches does not inspect the signatures present in either proof.
	Matches(other CommonMessageSignatureProof) bool

	// Merge adds the signature information in other to the current proof, without modifying other.
	//
	// The other value is assumed to be untrusted, and the proof should verify
	// every provided signature in other.
	//
	// If other is not the same underlying type, Merge panics.
	Merge(other CommonMessageSignatureProof) SignatureProofMergeResult

	// MergeSparse merges a sparse proof into the current proof.
	// This is intended to be used as part of accepting proofs from peers,
	// where peers will transmit a sparse value.
	MergeSparse(SparseSignatureProof) SignatureProofMergeResult

	// HasSparseKeyID reports whether the full proof already contains a signature
	// matching the given sparse key ID.
	// If the key ID does not properly map into the set of trusted public keys,
	// the "valid" return parameter will be false.
	HasSparseKeyID(keyID []byte) (has, valid bool)

	// Clone returns a copy of the current proof.
	//
	// This is useful when one goroutine owns the writes to a proof,
	// and another goroutine needs a read-only view without mutex contention.
	Clone() CommonMessageSignatureProof

	// Derive is like Clone;
	// it returns a copy of the current proof, but with all signature data cleared.
	//
	// This is occasionally useful when you have a valid proof,
	// but not a proof scheme, and you need to make a complicated operation.
	Derive() CommonMessageSignatureProof

	// SignatureBitSet writes the proof's underlying bit set
	// (indicating which of the candidate keys have signatures included in this proof)
	// to the given destination bit set.
	//
	// In the case of a SignatureProof that involves aggregating signatures,
	// the count of set bits may be greater than the number of signatures
	// that would be returned from the AsSparse method.
	//
	// By having the caller provide the bit set,
	// the caller controls allocations for the bitset.
	SignatureBitSet(*bitset.BitSet)

	// AsSparse returns a sparse version of the proof,
	// suitable for transmitting over the network.
	AsSparse() SparseSignatureProof
}

// SparseSignatureProof is a minimal representation of a single signature proof.
//
// This format is suitable for network transmission,
// as it does not encode the entire proof state,
// but it suffices for the remote end with fuller knowledge
// to use MergeSparse to increase signature proof awareness.
//
// NOTE: this may be renamed in the future if multiple-message signature proofs
// need a different sparse representation.
type SparseSignatureProof struct {
	// The PubKeyHash of the original proof.
	PubKeyHash string

	// The signatures for this proof,
	// along with implementation-specific key IDs.
	Signatures []SparseSignature
}

// SparseSignature is part of a SparseSignatureProof,
// representing one or many original signatures,
// depending on whether the non-sparse proof aggregates signatures.
type SparseSignature struct {
	// The Key ID is an opaque value, specific to the full proof,
	// indicating which key or keys are represented by the given signature.
	KeyID []byte

	// The bytes of the signature.
	Sig []byte
}

// CommonMessageSignatureProofScheme indicates how to create
// CommonMessageSignatureProof instances.
//
// It also contains methods that have no relation to a particular proof instance.
type CommonMessageSignatureProofScheme interface {
	// New creates a new, empty proof.
	New(msg []byte, candidateKeys []PubKey, pubKeyHash string) (CommonMessageSignatureProof, error)

	// IsValidKeyID reports whether the given sparse key ID is valid given the set of public keys.
	//
	// TODO: this should return a new type so that the cost of key aggregation
	// can be amortized once instead of on every check.
	IsValidKeyID(id []byte, keys []PubKey) bool
}

// LiteralCommonMessageSignatureProofScheme returns a CommonMessageSignatureProofScheme
// from a literal function with a strongly typed return values.
//
// This allows signature proof authors to follow the more common pattern
// of returning the concrete types in their constructor functions,
// without writing extra boilerplate to produce a corresponding scheme.
func LiteralCommonMessageSignatureProofScheme[P CommonMessageSignatureProof](
	newFn func([]byte, []PubKey, string) (P, error),
	isValidKeyIDFn func([]byte, []PubKey) bool,
) CommonMessageSignatureProofScheme {
	return literalCommonMessageSignatureProofScheme{
		newFn: func(msg []byte, candidateKeys []PubKey, pubKeyHash string) (CommonMessageSignatureProof, error) {
			return newFn(msg, candidateKeys, pubKeyHash)
		},
		isValidKeyIDFn: isValidKeyIDFn,
	}
}

type literalCommonMessageSignatureProofScheme struct {
	newFn func([]byte, []PubKey, string) (CommonMessageSignatureProof, error)

	isValidKeyIDFn func([]byte, []PubKey) bool
}

func (s literalCommonMessageSignatureProofScheme) New(msg []byte, candidateKeys []PubKey, pubKeyHash string) (CommonMessageSignatureProof, error) {
	return s.newFn(msg, candidateKeys, pubKeyHash)
}

func (s literalCommonMessageSignatureProofScheme) IsValidKeyID(id []byte, keys []PubKey) bool {
	return s.isValidKeyIDFn(id, keys)
}
