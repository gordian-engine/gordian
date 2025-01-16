package tmconsensustest_test

import (
	"testing"

	"github.com/gordian-engine/gordian/gcrypto"
	"github.com/gordian-engine/gordian/tm/tmconsensus/tmconsensustest"
)

func TestSimpleHashScheme(t *testing.T) {
	tmconsensustest.TestHashSchemeCompliance(t, tmconsensustest.SimpleHashScheme{}, gcrypto.SimpleCommonMessageSignatureProofScheme{}, nil)
}
