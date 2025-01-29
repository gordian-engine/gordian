package memstorecompliance

import (
	"testing"

	"github.com/gordian-engine/gordian/gcrypto"
	"github.com/gordian-engine/gordian/gcrypto/gblsminsig"
	"github.com/gordian-engine/gordian/gcrypto/gblsminsig/gblsminsigtest"
	"github.com/gordian-engine/gordian/tm/tmconsensus"
	"github.com/gordian-engine/gordian/tm/tmconsensus/tmconsensustest"
	"github.com/gordian-engine/gordian/tm/tmstore"
	"github.com/gordian-engine/gordian/tm/tmstore/tmmemstore"
	"github.com/gordian-engine/gordian/tm/tmstore/tmstoretest"
)

func fixtureFactory(nVals int) *tmconsensustest.Fixture {
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

func TestActionStoreCompliance(t *testing.T) {
	t.Parallel()

	tmstoretest.TestActionStoreCompliance(
		t,
		func(func(func())) (tmstore.ActionStore, error) {
			return tmmemstore.NewActionStore(), nil
		},
		fixtureFactory,
	)
}

func TestCommittedHeaderStoreCompliance(t *testing.T) {
	t.Parallel()

	tmstoretest.TestCommittedHeaderStoreCompliance(
		t,
		func(func(func())) (tmstore.CommittedHeaderStore, error) {
			return tmmemstore.NewCommittedHeaderStore(), nil
		},
		fixtureFactory,
	)
}

func TestFinalizationStoreCompliance(t *testing.T) {
	t.Parallel()

	tmstoretest.TestFinalizationStoreCompliance(
		t,
		func(func(func())) (tmstore.FinalizationStore, error) {
			return tmmemstore.NewFinalizationStore(), nil
		},
		fixtureFactory,
	)
}

// TODO: does it make sense to include the multi store test here?
// The tmmemstore.MemMultiStore is inside tmememstore_test,
// which is of course inaccessible here.

// MirrorStore is skipped because it doesn't have any relation
// to signature- or validator-specific fixtures.

func TestRoundStoreCompliance(t *testing.T) {
	t.Parallel()

	tmstoretest.TestRoundStoreCompliance(
		t,
		func(func(func())) (tmstore.RoundStore, error) {
			return tmmemstore.NewRoundStore(), nil
		},
		fixtureFactory,
	)
}

// Also skipping the state machine store,
// because it is also focused on heights and rounds,
// and it does not include validators or signatures.

func TestValidatorStoreCompliance(t *testing.T) {
	t.Parallel()

	tmstoretest.TestValidatorStoreCompliance(
		t,
		func(func(func())) (tmstore.ValidatorStore, error) {
			hs := fixtureFactory(0).HashScheme
			return tmmemstore.NewValidatorStore(hs), nil
		},
		fixtureFactory,
	)
}
