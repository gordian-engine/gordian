package tmconsensus

import (
	"fmt"
	"slices"

	"github.com/gordian-engine/gordian/gcrypto"
)

type PrevoteProof struct {
	Height uint64
	Round  uint32

	Proofs map[string]gcrypto.CommonMessageSignatureProof
}

func (p PrevoteProof) AsSparse() (PrevoteSparseProof, error) {
	out := PrevoteSparseProof{
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
				"public key hash mismatch when converting prevote proof to sparse: expected %x, got %x",
				out.PubKeyHash, pubKeyHash,
			)
		}
		out.Proofs[blockHash] = proof.AsSparse().Signatures
	}

	return out, nil
}

// PrevoteSparseProof is the representation of sparse proofs for prevotes arriving across the network.
type PrevoteSparseProof struct {
	Height uint64
	Round  uint32

	PubKeyHash string

	Proofs map[string][]gcrypto.SparseSignature
}

func PrevoteSparseProofFromFullProof(height uint64, round uint32, fullProof map[string]gcrypto.CommonMessageSignatureProof) (PrevoteSparseProof, error) {
	p := PrevoteSparseProof{
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
			return PrevoteSparseProof{}, fmt.Errorf("public key hash mismatch: expected %x, got %x", p.PubKeyHash, s.PubKeyHash)
		}

		p.Proofs[blockHash] = s.Signatures
	}

	return p, nil
}

func (p PrevoteSparseProof) Clone() PrevoteSparseProof {
	m := make(map[string][]gcrypto.SparseSignature, len(p.Proofs))
	for k, v := range p.Proofs {
		m[k] = slices.Clone(v)
	}
	return PrevoteSparseProof{
		Height: p.Height,
		Round:  p.Round,

		PubKeyHash: p.PubKeyHash,

		Proofs: m,
	}
}

func (p PrevoteSparseProof) ToFull(
	cmsps gcrypto.CommonMessageSignatureProofScheme,
	sigScheme SignatureScheme,
	hashScheme HashScheme,
	trustedVals []Validator,
) (PrevoteProof, error) {
	out := PrevoteProof{
		Height: p.Height,
		Round:  p.Round,
		Proofs: make(map[string]gcrypto.CommonMessageSignatureProof, len(p.Proofs)),
	}

	valPubKeys := ValidatorsToPubKeys(trustedVals)
	bValPubKeyHash, err := hashScheme.PubKeys(valPubKeys)
	if err != nil {
		return out, fmt.Errorf("failed to build validator pub key hash: %w", err)
	}
	valPubKeyHash := string(bValPubKeyHash)

	for h, sigs := range p.Proofs {
		vt := VoteTarget{
			Height:    p.Height,
			Round:     p.Round,
			BlockHash: h,
		}
		msg, err := PrevoteSignBytes(vt, sigScheme)
		if err != nil {
			return out, fmt.Errorf("failed to build prevote sign bytes: %w", err)
		}

		out.Proofs[h], err = cmsps.New(msg, valPubKeys, valPubKeyHash)
		if err != nil {
			return out, fmt.Errorf("failed to build signature proof: %w", err)
		}

		sparseProof := gcrypto.SparseSignatureProof{
			PubKeyHash: p.PubKeyHash,
			Signatures: sigs,
		}
		_ = out.Proofs[h].MergeSparse(sparseProof)
	}

	return out, nil
}
