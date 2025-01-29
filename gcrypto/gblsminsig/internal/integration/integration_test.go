package integration_test

import (
	"context"
	"testing"

	"github.com/gordian-engine/gordian/gcrypto"
	"github.com/gordian-engine/gordian/gcrypto/gblsminsig"
	"github.com/gordian-engine/gordian/gcrypto/gblsminsig/gblsminsigtest"
	"github.com/gordian-engine/gordian/tm/tmconsensus"
	"github.com/gordian-engine/gordian/tm/tmconsensus/tmconsensustest"
	"github.com/gordian-engine/gordian/tm/tmintegration"
)

type blsFactory struct {
	tmintegration.Libp2pFactory
	tmintegration.InmemStoreFactory
	tmintegration.InmemSchemeFactory
}

func newBLSFactory(e *tmintegration.Env) blsFactory {
	return blsFactory{
		Libp2pFactory: tmintegration.NewLibp2pFactory(e),

		// Zero-value structs are fine for the two in-mem factories.
	}
}

func (f blsFactory) CommonMessageSignatureProofScheme(_ context.Context, idx int) (
	gcrypto.CommonMessageSignatureProofScheme, error,
) {
	return gblsminsig.SignatureProofScheme{}, nil
}

func (f blsFactory) NewConsensusFixture(nVals int) *tmconsensustest.Fixture {
	var reg gcrypto.Registry
	gblsminsig.Register(&reg)

	privVals := make(tmconsensustest.PrivVals, nVals)
	signers := gblsminsigtest.DeterministicSigners(nVals)

	for i := range privVals {
		privVals[i] = tmconsensustest.PrivVal{
			Val: tmconsensus.Validator{
				PubKey: signers[i].PubKey().(gblsminsig.PubKey),

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

	return &tmconsensustest.Fixture{
		PrivVals: privVals,

		SignatureScheme:                   tmconsensustest.SimpleSignatureScheme{},
		HashScheme:                        tmconsensustest.SimpleHashScheme{},
		CommonMessageSignatureProofScheme: gblsminsig.SignatureProofScheme{},

		Registry: reg,

		// The fixture also has prevCommitProof and prevAppStateHash fields,
		// which are unexported so we can't access them from this package.
		// Tests are passing currently, but the inability to set those fields
		// seems likely to cause an issue at some point.
	}
}

func TestGblsminsig(t *testing.T) {
	tmintegration.RunIntegrationTest(t, func(e *tmintegration.Env) tmintegration.Factory {
		return newBLSFactory(e)
	})
}
