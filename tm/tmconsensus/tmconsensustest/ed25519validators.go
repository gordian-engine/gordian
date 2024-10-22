package tmconsensustest

import (
	"github.com/gordian-engine/gordian/gcrypto"
	"github.com/gordian-engine/gordian/gcrypto/gcryptotest"
	"github.com/gordian-engine/gordian/tm/tmconsensus"
)

// PrivValEd25519 contains a Validator with an ed25519 key
// and the corresponding private key.
type PrivValEd25519 struct {
	// The plain consensus validator.
	CVal tmconsensus.Validator

	Signer gcrypto.Ed25519Signer
}

// PrivValsEd25519 is a slice of PrivValEd25519.
type PrivValsEd25519 []PrivValEd25519

// Vals returns an unordered Validator slice,
// as a convenience for types that expect it.
func (vs PrivValsEd25519) Vals() []tmconsensus.Validator {
	out := make([]tmconsensus.Validator, len(vs))
	for i, v := range vs {
		out[i] = v.CVal
	}
	return out
}

// PubKeys returns a slice of gcrypto.PubKey corresponding to vs.
func (vs PrivValsEd25519) PubKeys() []gcrypto.PubKey {
	out := make([]gcrypto.PubKey, len(vs))
	for i, v := range vs {
		out[i] = v.Signer.PubKey()
	}
	return out
}

// DeterministicValidatorsEd25519 returns a deterministic set
// of validators with ed25519 keys.
//
// Each validator will have its VotingPower set to 1.
//
// There are two advantages to using deterministic keys.
// First, subsequent runs of the same test will use the same keys,
// so logs involving keys or IDs will not change across runs,
// simplifying the debugging process.
// Second, the generated keys are cached,
// so there is effectively zero CPU time cost for additional tests
// calling this function, beyond the first call.
func DeterministicValidatorsEd25519(n int) PrivValsEd25519 {
	res := make([]PrivValEd25519, n)
	signers := gcryptotest.DeterministicEd25519Signers(n)

	for i := range res {
		res[i] = PrivValEd25519{
			CVal: tmconsensus.Validator{
				PubKey: signers[i].PubKey().(gcrypto.Ed25519PubKey),

				// Order by power descending,
				// with the power difference being negligible,
				// so that the validator order matches the default deterministic key order.
				// (Without this power adjustment, the validators would be ordered
				// by public key or by ID, which is unlikely to match their order
				// as defined in fixtures or other uses of determinsitic validators.
				Power: uint64(100_000 - i),
			},
			Signer: signers[i],
		}
	}

	return res
}
