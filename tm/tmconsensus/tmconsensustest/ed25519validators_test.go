package tmconsensustest_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/gordian-engine/gordian/tm/tmconsensus/tmconsensustest"
	"github.com/stretchr/testify/require"
)

func TestDeterministicValidatorsEd25519(t *testing.T) {
	t.Run("defaults to voting power 1", func(t *testing.T) {
		vals := tmconsensustest.DeterministicValidatorsEd25519(4)

		for i, v := range vals {
			require.Equal(t, uint64(100_000-i), v.Val.Power)
		}
	})

	ctx := context.Background()

	t.Run("key determinism", func(t *testing.T) {
		vals4 := tmconsensustest.DeterministicValidatorsEd25519(4)

		t.Run("same keys when same size", func(t *testing.T) {
			again := tmconsensustest.DeterministicValidatorsEd25519(4)

			for i := range vals4 {
				pub1 := vals4[i].Val.PubKey.PubKeyBytes()
				pub2 := again[i].Val.PubKey.PubKeyBytes()
				require.Truef(
					t,
					bytes.Equal(pub1, pub2),
					"got different public key bytes at index %d across invocations: %X -> %X",
					i, pub1, pub2,
				)

				sig1, err := vals4[i].Signer.Sign(ctx, []byte("test"))
				require.NoError(t, err)
				sig2, err := again[i].Signer.Sign(ctx, []byte("test"))
				require.NoError(t, err)
				require.Equal(t, sig1, sig2)
			}
		})

		t.Run("same keys when shrinking", func(t *testing.T) {
			vals2 := tmconsensustest.DeterministicValidatorsEd25519(2)

			for i := range vals2 {
				pub1 := vals4[i].Val.PubKey.PubKeyBytes()
				pub2 := vals2[i].Val.PubKey.PubKeyBytes()
				require.Truef(
					t,
					bytes.Equal(pub1, pub2),
					"got different public key bytes at index %d across invocations: %X -> %X",
					i, pub1, pub2,
				)

				sig1, err := vals4[i].Signer.Sign(ctx, []byte("test"))
				require.NoError(t, err)
				sig2, err := vals2[i].Signer.Sign(ctx, []byte("test"))
				require.NoError(t, err)
				require.Equal(t, sig1, sig2)
			}
		})

		t.Run("same keys when growing", func(t *testing.T) {
			vals6 := tmconsensustest.DeterministicValidatorsEd25519(6)

			for i := range vals4 {
				pub1 := vals4[i].Val.PubKey.PubKeyBytes()
				pub2 := vals6[i].Val.PubKey.PubKeyBytes()
				require.Truef(
					t,
					bytes.Equal(pub1, pub2),
					"got different public key bytes at index %d across invocations: %X -> %X",
					i, pub1, pub2,
				)

				sig1, err := vals4[i].Signer.Sign(ctx, []byte("test"))
				require.NoError(t, err)
				sig2, err := vals6[i].Signer.Sign(ctx, []byte("test"))
				require.NoError(t, err)
				require.Equal(t, sig1, sig2)
			}
		})
	})

	t.Run("underlying byte slices are independent", func(t *testing.T) {
		a := tmconsensustest.DeterministicValidatorsEd25519(1)
		b := tmconsensustest.DeterministicValidatorsEd25519(1)

		pub1 := a[0].Val.PubKey.PubKeyBytes()
		pub2 := b[0].Val.PubKey.PubKeyBytes()
		pub2[0]++

		require.False(
			t,
			bytes.Equal(pub1, pub2),
			"expected public key bytes to differ after modifying one but they were the same",
		)
	})
}
