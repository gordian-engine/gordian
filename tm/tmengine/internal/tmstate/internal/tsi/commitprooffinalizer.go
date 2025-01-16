package tsi

import (
	"fmt"

	"github.com/gordian-engine/gordian/gcrypto"
	"github.com/gordian-engine/gordian/tm/tmconsensus"
)

// CommitProofFinalizer converts plain precommit proofs
// into a finalized commit proof,
// to be used when the state machine is constructing a proposed header.
type CommitProofFinalizer struct {
	SigScheme  tmconsensus.SignatureScheme
	CMSPScheme gcrypto.CommonMessageSignatureProofScheme
}

func (f CommitProofFinalizer) Finalize(
	h uint64,
	committedHash string,
	p tmconsensus.CommitProof,
	pubKeys []gcrypto.PubKey,
) (tmconsensus.CommitProof, error) {
	mainSignContent, err := tmconsensus.PrecommitSignBytes(tmconsensus.VoteTarget{
		Height:    h,
		Round:     p.Round,
		BlockHash: committedHash,
	}, f.SigScheme)
	if err != nil {
		return tmconsensus.CommitProof{}, fmt.Errorf(
			"failed to build precommit sign bytes: %w", err,
		)
	}

	mainProof, err := f.CMSPScheme.New(mainSignContent, pubKeys, p.PubKeyHash)
	if err != nil {
		return tmconsensus.CommitProof{}, fmt.Errorf(
			"failed to build common message signature proof: %w", err,
		)
	}

	res := mainProof.MergeSparse(gcrypto.SparseSignatureProof{
		PubKeyHash: p.PubKeyHash,
		Signatures: p.Proofs[committedHash],
	})
	if !res.IncreasedSignatures {
		// Should be impossible to reach this condition.
		return tmconsensus.CommitProof{}, fmt.Errorf(
			"no signatures for main committed block in previous proof",
		)
	}
	if !res.AllValidSignatures {
		return tmconsensus.CommitProof{}, fmt.Errorf(
			"invalid signatures for main committed block in previous proof",
		)
	}

	var rest []gcrypto.CommonMessageSignatureProof
	var hashesBySignContent map[string]string
	if len(p.Proofs) > 1 {
		rest = make([]gcrypto.CommonMessageSignatureProof, 0, len(p.Proofs)-1)
		hashesBySignContent = make(map[string]string, len(p.Proofs)-1)
	}

	for blockHash, sparseSigs := range p.Proofs {
		if blockHash == committedHash {
			continue
		}

		signContent, err := tmconsensus.PrecommitSignBytes(tmconsensus.VoteTarget{
			Height:    h,
			Round:     p.Round,
			BlockHash: blockHash,
		}, f.SigScheme)
		if err != nil {
			return tmconsensus.CommitProof{}, fmt.Errorf(
				"failed to build precommit sign bytes: %w", err,
			)
		}

		hashesBySignContent[string(signContent)] = blockHash

		proof, err := f.CMSPScheme.New(signContent, pubKeys, p.PubKeyHash)
		if err != nil {
			return tmconsensus.CommitProof{}, fmt.Errorf(
				"failed to build common message signature proof: %w", err,
			)
		}

		res := proof.MergeSparse(gcrypto.SparseSignatureProof{
			PubKeyHash: p.PubKeyHash,
			Signatures: sparseSigs,
		})
		if !res.IncreasedSignatures {
			// Should be impossible to reach this condition.
			return tmconsensus.CommitProof{}, fmt.Errorf(
				"no signatures for other committed block in previous proof",
			)
		}
		if !res.AllValidSignatures {
			return tmconsensus.CommitProof{}, fmt.Errorf(
				"invalid signatures for other committed block in previous proof",
			)
		}

		rest = append(rest, proof)
	}

	// We've populated the main proof and the rest,
	// so now we can finalize the commit proof.
	finalized := f.CMSPScheme.Finalize(mainProof, rest)

	// No need to continue referencing the main sign content,
	// so clear it for possible earlier GC.
	finalized.MainMessage = nil

	// Now, we need to convert the finalized proof
	// back into a plain CommitProof,
	// which means we need to map the signing content back to block hashes.
	outProofs := make(map[string][]gcrypto.SparseSignature, len(p.Proofs))
	outProofs[committedHash] = finalized.MainSignatures

	for content, sparseSigs := range finalized.Rest {
		blockHash := hashesBySignContent[content]
		outProofs[blockHash] = sparseSigs

		// More possible early GC.
		delete(finalized.Rest, content)
		delete(hashesBySignContent, content)
	}

	return tmconsensus.CommitProof{
		Round:      p.Round,
		PubKeyHash: p.PubKeyHash,
		Proofs:     outProofs,
	}, nil
}
