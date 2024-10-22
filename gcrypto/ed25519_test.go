package gcrypto_test

import (
	"context"
	"testing"

	"github.com/gordian-engine/gordian/gcrypto"
	"github.com/gordian-engine/gordian/gcrypto/gcryptotest"
	"github.com/stretchr/testify/require"
)

func TestEd25519(t *testing.T) {
	t.Parallel()

	// TODO: this structure could probably be converted
	// into a pubkey compliance test.

	var reg gcrypto.Registry
	gcrypto.RegisterEd25519(&reg)

	signers := gcryptotest.DeterministicEd25519Signers(2)

	s1, s2 := signers[0], signers[1]

	dec1, err := reg.Decode(s1.PubKey().TypeName(), s1.PubKey().PubKeyBytes())
	require.NoError(t, err)

	require.True(t, s1.PubKey().Equal(dec1))
	require.False(t, s2.PubKey().Equal(dec1))

	msg := []byte("hello")
	sig, err := s1.Sign(context.Background(), msg)
	require.NoError(t, err)

	require.True(t, s1.PubKey().Verify(msg, sig))
	require.False(t, s2.PubKey().Verify(msg, sig))
}
