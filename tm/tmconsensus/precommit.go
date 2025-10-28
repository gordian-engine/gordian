package tmconsensus

import (
	"fmt"
	"slices"

	"github.com/gordian-engine/gordian/gcrypto"
)

type PrecommitProof struct {
	Height uint64
	Round  uint32

	Proofs map[string]gcrypto.CommonMessageSignatureProof
}

func (p PrecommitProof) AsSparse() (PrecommitSparseProof, error) {
	out := PrecommitSparseProof{
		Height: p.Height,
		Round:  p.Round,

		Proofs: make(map[string][]gcrypto.SparseSignature, len(p.Proofs)),
	}

	// Use an arbitrary entry to set the pub key hash.
	for _, proof := range p.Proofs {
		out.PubKeyHash = string(proof.PubKeyHash())
		break
	}

	for blockHash, proof := range p.Proofs {
		if pubKeyHash := string(proof.PubKeyHash()); pubKeyHash != out.PubKeyHash {
			return out, fmt.Errorf(
				"public key hash mismatch when converting precommit proof to sparse: expected %x, got %x",
				out.PubKeyHash, pubKeyHash,
			)
		}
		out.Proofs[blockHash] = proof.AsSparse().Signatures
	}

	return out, nil
}

// PrecommitSparseProof is the representation of sparse proofs for precommits arriving across the network.
// It is currently identical to PrevoteSparseProof, but that may change with vote extensions.
type PrecommitSparseProof struct {
	Height uint64
	Round  uint32

	PubKeyHash string

	Proofs map[string][]gcrypto.SparseSignature
}

func PrecommitSparseProofFromFullProof(height uint64, round uint32, fullProof map[string]gcrypto.CommonMessageSignatureProof) (PrecommitSparseProof, error) {
	p := PrecommitSparseProof{
		Height: height,
		Round:  round,

		Proofs: make(map[string][]gcrypto.SparseSignature, len(fullProof)),
	}

	// Pick an arbitrary public key hash to put on the sparse proof.
	for _, proof := range fullProof {
		p.PubKeyHash = string(proof.PubKeyHash())
		break
	}

	for blockHash, proof := range fullProof {
		s := proof.AsSparse()
		if s.PubKeyHash != p.PubKeyHash {
			return PrecommitSparseProof{}, fmt.Errorf("public key hash mismatch: expected %x, got %x", p.PubKeyHash, s.PubKeyHash)
		}

		p.Proofs[blockHash] = s.Signatures
	}

	return p, nil
}

func (p PrecommitSparseProof) Clone() PrecommitSparseProof {
	m := make(map[string][]gcrypto.SparseSignature, len(p.Proofs))
	for k, v := range p.Proofs {
		m[k] = slices.Clone(v)
	}
	return PrecommitSparseProof{
		Height: p.Height,
		Round:  p.Round,

		PubKeyHash: p.PubKeyHash,

		Proofs: m,
	}
}

// ToFull returns a newly allocated full PrecommitProof
// based on the sparse proof and the provided arguments.
//
// This is mostly intended for tests that intercept a sparse proof,
// but it may be useful in limited edge cases in production.
func (p PrecommitSparseProof) ToFull(
	cmsps gcrypto.CommonMessageSignatureProofScheme,
	sigScheme SignatureScheme,
	pubKeys []gcrypto.PubKey,
	pubKeyHash string,
) (PrecommitProof, error) {
	out := PrecommitProof{
		Height: p.Height,
		Round:  p.Round,
		Proofs: make(map[string]gcrypto.CommonMessageSignatureProof, len(p.Proofs)),
	}

	for h, sigs := range p.Proofs {
		vt := VoteTarget{
			Height:    p.Height,
			Round:     p.Round,
			BlockHash: h,
		}
		msg, err := PrecommitSignBytes(vt, sigScheme)
		if err != nil {
			return out, fmt.Errorf("failed to build precommit sign bytes: %w", err)
		}

		out.Proofs[h], err = cmsps.New(msg, pubKeys, pubKeyHash)
		if err != nil {
			return out, fmt.Errorf("failed to build precommit signature proof: %w", err)
		}

		sparseProof := gcrypto.SparseSignatureProof{
			PubKeyHash: p.PubKeyHash,
			Signatures: sigs,
		}
		_ = out.Proofs[h].MergeSparse(sparseProof)
	}

	return out, nil
}
