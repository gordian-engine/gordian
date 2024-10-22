package gcryptotest_test

import (
	"context"
	"testing"

	"github.com/gordian-engine/gordian/gcrypto/gcryptotest"
	"github.com/stretchr/testify/require"
)

func TestDeterministicEd25519Signers(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	t.Run("key determinism", func(t *testing.T) {
		s4 := gcryptotest.DeterministicEd25519Signers(4)

		t.Run("same keys when same size", func(t *testing.T) {
			again := gcryptotest.DeterministicEd25519Signers(4)

			for i := range s4 {
				pub1 := s4[i].PubKey()
				pub2 := again[i].PubKey()

				require.Truef(
					t,
					pub1.Equal(pub2),
					"got different public keys at index %d across invocations: %X -> %X",
					i, pub1.PubKeyBytes(), pub2.PubKeyBytes(),
				)

				sig1, err := s4[i].Sign(ctx, []byte("test"))
				require.NoError(t, err)
				sig2, err := again[i].Sign(ctx, []byte("test"))
				require.NoError(t, err)
				require.Equal(t, sig1, sig2)
			}
		})

		t.Run("same keys when shrinking", func(t *testing.T) {
			s2 := gcryptotest.DeterministicEd25519Signers(2)

			for i := range s2 {
				pub1 := s4[i].PubKey()
				pub2 := s2[i].PubKey()

				require.Truef(
					t,
					pub1.Equal(pub2),
					"got different public keys at index %d across invocations: %X -> %X",
					i, pub1.PubKeyBytes(), pub2.PubKeyBytes(),
				)

				sig1, err := s4[i].Sign(ctx, []byte("test"))
				require.NoError(t, err)
				sig2, err := s2[i].Sign(ctx, []byte("test"))
				require.NoError(t, err)
				require.Equal(t, sig1, sig2)
			}
		})

		t.Run("same keys when growing", func(t *testing.T) {
			s6 := gcryptotest.DeterministicEd25519Signers(6)

			for i := range s4 {
				pub1 := s4[i].PubKey()
				pub2 := s6[i].PubKey()

				require.Truef(
					t,
					pub1.Equal(pub2),
					"got different public keys at index %d across invocations: %X -> %X",
					i, pub1.PubKeyBytes(), pub2.PubKeyBytes(),
				)

				sig1, err := s4[i].Sign(ctx, []byte("test"))
				require.NoError(t, err)
				sig2, err := s6[i].Sign(ctx, []byte("test"))
				require.NoError(t, err)
				require.Equal(t, sig1, sig2)
			}
		})
	})

	t.Run("underlying byte slices are independent", func(t *testing.T) {
		a := gcryptotest.DeterministicEd25519Signers(1)
		b := gcryptotest.DeterministicEd25519Signers(1)

		pub1 := a[0].PubKey().PubKeyBytes()
		pub2 := b[0].PubKey().PubKeyBytes()
		pub2[0]++

		require.NotEqual(
			t,
			pub1, pub2,
			"expected public key bytes to differ after modifying one but they were the same",
		)
	})
}
