package gcrypto_test

import (
	"testing"

	"github.com/gordian-engine/gordian/gcrypto"
	"github.com/gordian-engine/gordian/gcrypto/gcryptotest"
)

func TestSimpleCommonMessageSignatureProof(t *testing.T) {
	gcryptotest.TestCommonMessageSignatureProofCompliance_Ed25519(
		t,
		gcrypto.SimpleCommonMessageSignatureProofScheme{},
	)
}
