package tmconsensus

import (
	"fmt"

	"github.com/gordian-engine/gordian/gcrypto"
)

// SparseSignatureCollection is a tm-specific,
// network- and store-optimized collection of [gcrypto.SparseSignature].
//
// A [gcrypto.SparseSignatureProof] is one public key hash
// with a collection of signatures,
// without a clear relationship to any particular block hash.
// Using a map[string]gcrypto.SparseSignatureProof
// requires the caller to verify every public key hash in every proof.
// So instead, with the SparseSignatureCollection,
// we raise the public key hash up a level so it only exists once.
//
// This type lives in tmconsensus instead of gcrypto
// due to its larger awareness of block hashes,
// which are outside the scope of the gcrypto package.
type SparseSignatureCollection struct {
	// There is exactly one public key hash for
	// the keys and signatures in the entire collection.
	// Consumers must assume this slice is shared across many goroutines,
	// and therefore must never modify the slice.
	PubKeyHash []byte

	// Mapping of block hash to sparse signatures.
	// The empty string represents a vote for nil.
	BlockSignatures map[string][]gcrypto.SparseSignature
}

// ToFullPrevoteProofMap converts c
// to a map of block hashes to full proofs.
func (c SparseSignatureCollection) ToFullPrevoteProofMap(
	height uint64,
	round uint32,
	pubKeys []gcrypto.PubKey,
	sigScheme SignatureScheme,
	cmspScheme gcrypto.CommonMessageSignatureProofScheme,
) (map[string]gcrypto.CommonMessageSignatureProof, error) {
	out, err := c.toFullProofMap(
		height, round,
		pubKeys,
		cmspScheme,
		func(vt VoteTarget) ([]byte, error) {
			return PrevoteSignBytes(vt, sigScheme)
		},
	)
	if err != nil {
		return nil, fmt.Errorf("failed to build prevote proof map: %w", err)
	}
	return out, nil
}

// ToFullPrecommitProofMap converts c
// to a map of block hashes to full proofs.
func (c SparseSignatureCollection) ToFullPrecommitProofMap(
	height uint64,
	round uint32,
	pubKeys []gcrypto.PubKey,
	sigScheme SignatureScheme,
	cmspScheme gcrypto.CommonMessageSignatureProofScheme,
) (map[string]gcrypto.CommonMessageSignatureProof, error) {
	out, err := c.toFullProofMap(
		height, round,
		pubKeys,
		cmspScheme,
		func(vt VoteTarget) ([]byte, error) {
			return PrecommitSignBytes(vt, sigScheme)
		},
	)
	if err != nil {
		return nil, fmt.Errorf("failed to build precommit proof map: %w", err)
	}
	return out, nil
}

func (c SparseSignatureCollection) toFullProofMap(
	height uint64,
	round uint32,
	pubKeys []gcrypto.PubKey,
	cmspScheme gcrypto.CommonMessageSignatureProofScheme,
	signBytesFunc func(VoteTarget) ([]byte, error),
) (map[string]gcrypto.CommonMessageSignatureProof, error) {
	out := make(map[string]gcrypto.CommonMessageSignatureProof, len(c.BlockSignatures))
	sHash := string(c.PubKeyHash)

	vt := VoteTarget{Height: height, Round: round}
	for hash, sparseSigs := range c.BlockSignatures {
		vt.BlockHash = hash
		content, err := signBytesFunc(vt)
		if err != nil {
			return nil, fmt.Errorf(
				"failed to build sign bytes for block hash %x: %w", hash, err,
			)
		}
		out[hash], err = cmspScheme.New(content, pubKeys, sHash)
		if err != nil {
			return nil, fmt.Errorf("failed to get initial proof for %x: %w", hash, err)
		}

		if len(sparseSigs) == 0 {
			panic(fmt.Errorf("BUG: saw len(sparseSigs) == 0 for hash=%x", hash))
		}

		sparseProof := gcrypto.SparseSignatureProof{
			PubKeyHash: sHash,
			Signatures: sparseSigs,
		}
		mergeRes := out[hash].MergeSparse(sparseProof)
		if !mergeRes.AllValidSignatures || !mergeRes.IncreasedSignatures {
			panic(fmt.Errorf(
				"BUG: invalid result after merging signatures: all_valid=%t, increased=%t",
				mergeRes.AllValidSignatures, mergeRes.IncreasedSignatures,
			))
		}
	}

	return out, nil
}
