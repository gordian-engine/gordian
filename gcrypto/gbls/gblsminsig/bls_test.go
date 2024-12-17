package gblsminsig_test

import (
	"context"
	"testing"

	"github.com/gordian-engine/gordian/gcrypto/gbls/gblsminsig"
	"github.com/stretchr/testify/require"
	blst "github.com/supranational/blst/bindings/go"
)

func TestSignAndVerify_single(t *testing.T) {
	t.Parallel()

	ikm := make([]byte, 32)
	for i := range ikm {
		ikm[i] = byte(i)
	}

	s, err := gblsminsig.NewSigner(ikm)
	require.NoError(t, err)

	msg := []byte("hello world")

	sig, err := s.Sign(context.Background(), msg)
	require.NoError(t, err)

	require.True(t, s.PubKey().Verify(msg, sig))

	// Modifying the message fails verification.
	msg[0]++
	require.False(t, s.PubKey().Verify(msg, sig))
	msg[0]--

	// Modifying the signature fails verification too.
	sig[0]++
	require.False(t, s.PubKey().Verify(msg, sig))
}

func TestSignAndVerify_multiple(t *testing.T) {
	t.Parallel()

	ikm1 := make([]byte, 32)
	ikm2 := make([]byte, 32)
	for i := range ikm1 {
		ikm1[i] = byte(i)
		ikm2[i] = byte(i) + 32
	}

	s1, err := gblsminsig.NewSigner(ikm1)
	require.NoError(t, err)
	s2, err := gblsminsig.NewSigner(ikm2)
	require.NoError(t, err)

	msg := []byte("hello world")

	sig1, err := s1.Sign(context.Background(), msg)
	require.NoError(t, err)

	sig2, err := s2.Sign(context.Background(), msg)
	require.NoError(t, err)

	sigp11 := new(blst.P1Affine).Uncompress(sig1)
	require.NotNil(t, sigp11)
	sigp12 := new(blst.P1Affine).Uncompress(sig2)
	require.NotNil(t, sigp12)

	// Aggregate the signatures into a single affine point.
	sigAgg := new(blst.P1Aggregate)
	require.True(t, sigAgg.AggregateCompressed([][]byte{sig1, sig2}, true))
	finalSig := sigAgg.ToAffine().Compress()

	// Aggregate the keys too.
	keyAgg := new(blst.P2Aggregate)
	require.True(t, keyAgg.AggregateCompressed([][]byte{
		s1.PubKey().PubKeyBytes(),
		s2.PubKey().PubKeyBytes(),
	}, true))

	finalKeyAffine := keyAgg.ToAffine()
	finalKey := gblsminsig.PubKey(*finalKeyAffine)

	require.True(t, finalKey.Verify(msg, finalSig))

	// Changing the message fails verification.
	msg[0]++
	require.False(t, finalKey.Verify(msg, finalSig))
	msg[0]--

	// Modifying the signature fails verification too.
	finalSig[0]++
	require.False(t, finalKey.Verify(msg, finalSig))
}
