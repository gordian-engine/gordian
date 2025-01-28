package tmconsensustest

import (
	"github.com/gordian-engine/gordian/gcrypto"
	"github.com/gordian-engine/gordian/gcrypto/gcryptotest"
	"github.com/gordian-engine/gordian/tm/tmconsensus"
)

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
func DeterministicValidatorsEd25519(n int) PrivVals {
	res := make(PrivVals, n)
	signers := gcryptotest.DeterministicEd25519Signers(n)

	for i := range res {
		res[i] = PrivVal{
			Val: tmconsensus.Validator{
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
