package tmconsensustest

import (
	"github.com/gordian-engine/gordian/gcrypto"
	"github.com/gordian-engine/gordian/tm/tmconsensus"
)

// NewEd25519Fixture returns an initialized Fixture
// with the given number of determinstic ed25519 validators,
// a [SimpleSignatureScheme], and a [SimpleHashScheme].
//
// See the Fixture docs for other fields that
// have default values but which may be overridden before use.
func NewEd25519Fixture(numVals int) *Fixture {
	privVals := DeterministicValidatorsEd25519(numVals)

	var reg gcrypto.Registry
	gcrypto.RegisterEd25519(&reg)

	return &Fixture{
		PrivVals: privVals,

		SignatureScheme: SimpleSignatureScheme{},

		CommonMessageSignatureProofScheme: gcrypto.SimpleCommonMessageSignatureProofScheme{},

		HashScheme: SimpleHashScheme{},

		Registry: reg,

		prevCommitProof: tmconsensus.CommitProof{
			// This map is expected to be empty, not nil, for the initial height.
			// TODO: why though? the stores return nil proofs when looking up the initial height,
			// and things appear to work fine that way.
			// And the nil-empty mismatch clutters up a lot of tests.
			Proofs: map[string][]gcrypto.SparseSignature{},
		},

		prevAppStateHash: []byte("uninitialized"),
	}
}
