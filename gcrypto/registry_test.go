package gcrypto_test

import (
	"crypto/ed25519"
	"testing"

	"github.com/gordian-engine/gordian/gcrypto"
	"github.com/stretchr/testify/require"
)

func TestRegistry_RoundTrip(t *testing.T) {
	pubKey, _, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)

	origKey := gcrypto.Ed25519PubKey(pubKey)

	reg := new(gcrypto.Registry)
	reg.Register("ed25519", gcrypto.Ed25519PubKey{}, gcrypto.NewEd25519PubKey)

	b := reg.Marshal(origKey)
	require.NoError(t, err)

	newKey, err := reg.Unmarshal(b)
	require.NoError(t, err)

	require.Equal(t, origKey.PubKeyBytes(), newKey.PubKeyBytes())
}

func TestRegistry_Unmarshal_UnknownType(t *testing.T) {
	reg := new(gcrypto.Registry)
	reg.Register("ed25519", gcrypto.Ed25519PubKey{}, gcrypto.NewEd25519PubKey)

	_, err := reg.Unmarshal([]byte("abcd\x00\x00\x00\x00111222333"))
	require.ErrorContains(t, err, "no registered public key type for prefix \"abcd\"")
}
