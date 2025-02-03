package tmconsensustest

import (
	"github.com/gordian-engine/gordian/gcrypto"
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

	fx := NewBareFixture()
	fx.Registry = reg
	fx.PrivVals = privVals

	return fx
}
