package gcryptotest

import (
	"bytes"
	"slices"

	"github.com/gordian-engine/gordian/gcrypto"
)

// CloneFinalized returns a deep copy of in,
// which is very useful for tests that need to assert that modifications
// to the proof cause validation to fail.
//
// There has not been a use case for this in production code,
// hence why this function lives in gcryptotest.
func CloneFinalizedCommonMessageSignatureProof(
	in gcrypto.FinalizedCommonMessageSignatureProof,
) gcrypto.FinalizedCommonMessageSignatureProof {
	out := gcrypto.FinalizedCommonMessageSignatureProof{
		Keys:       slices.Clone(in.Keys),
		PubKeyHash: in.PubKeyHash,

		MainMessage: bytes.Clone(in.MainMessage),
	}

	out.MainMessage = slices.Clone(out.MainMessage)
	out.MainSignatures = make([]gcrypto.SparseSignature, len(in.MainSignatures))

	for i, ss := range in.MainSignatures {
		out.MainSignatures[i] = gcrypto.SparseSignature{
			KeyID: bytes.Clone(ss.KeyID),
			Sig:   bytes.Clone(ss.Sig),
		}
	}

	out.Rest = make(map[string][]gcrypto.SparseSignature)

	for k, sss := range in.Rest {
		outSigs := make([]gcrypto.SparseSignature, len(sss))
		for i, ss := range sss {
			outSigs[i] = gcrypto.SparseSignature{
				KeyID: bytes.Clone(ss.KeyID),
				Sig:   bytes.Clone(ss.Sig),
			}
		}

		out.Rest[k] = outSigs
	}

	return out
}
