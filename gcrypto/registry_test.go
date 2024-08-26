package gcrypto_test

import (
	"crypto/ed25519"
	"testing"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/rollchains/gordian/gcrypto"
	"github.com/stretchr/testify/require"
)

func TestRegistry_RoundTrip(t *testing.T) {
	// Generate Ed25519 key
	pubKey, _, err := ed25519.GenerateKey(nil)
	require.NoError(t, err)
	origKey := gcrypto.Ed25519PubKey(pubKey)

	// Generate Secp256k1 key
	secp256k1PrivKey, err := crypto.GenerateKey()
	require.NoError(t, err)
	secp256k1Key := gcrypto.Secp256k1PubKey(secp256k1PrivKey.PublicKey)

	// Create and register keys in registry
	reg := new(gcrypto.Registry)
	gcrypto.RegisterEd25519(reg)
	gcrypto.RegisterSecp256k1(reg)

	// Marshal keys
	b := reg.Marshal(origKey)
	c := reg.Marshal(secp256k1Key)

	// Unmarshal keys
	newKey, err := reg.Unmarshal(b)
	require.NoError(t, err)
	newKey2, err := reg.Unmarshal(c)
	require.NoError(t, err)

	// Check if unmarshaled keys match original keys
	require.True(t, origKey.Equal(newKey), "Ed25519 keys should be equal")
	require.True(t, secp256k1Key.Equal(newKey2), "Secp256k1 keys should be equal")

	// Additional checks
	require.IsType(t, gcrypto.Ed25519PubKey{}, newKey, "Unmarshaled Ed25519 key should be of type Ed25519PubKey")
	require.IsType(t, gcrypto.Secp256k1PubKey{}, newKey2, "Unmarshaled Secp256k1 key should be of type Secp256k1PubKey")

	// Check if marshaled data includes correct prefix
	require.Equal(t, origKey.PubKeyBytes(), newKey.PubKeyBytes())
	require.Equal(t, secp256k1Key.PubKeyBytes(), newKey2.PubKeyBytes())
}

func TestRegistry_Unmarshal_UnknownType(t *testing.T) {
	reg := new(gcrypto.Registry)
	reg.Register("ed25519", gcrypto.Ed25519PubKey{}, gcrypto.NewEd25519PubKey)
	reg.Register("secp256k1", gcrypto.Secp256k1PubKey{}, gcrypto.NewSecp256k1PubKey)

	_, err := reg.Unmarshal([]byte("abcd\x00\x00\x00\x00111222333"))
	require.ErrorContains(t, err, "no registered public key type for prefix \"abcd\"")
}
