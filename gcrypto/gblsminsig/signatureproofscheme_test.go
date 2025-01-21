package gblsminsig_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/gordian-engine/gordian/gcrypto/gblsminsig"
	"github.com/stretchr/testify/require"
	blst "github.com/supranational/blst/bindings/go"
)

func TestFinalize_mainOnly_roundTrip(t *testing.T) {
	t.Parallel()

	s := gblsminsig.SignatureProofScheme{}

	msg := []byte("main message")
	proof, err := s.New(
		msg,
		testPubKeys[:],
		"ignored_hash",
	)
	require.NoError(t, err)

	ctx := context.Background()

	sig0, err := testSigners[0].Sign(ctx, msg)
	require.NoError(t, err)

	sig1, err := testSigners[1].Sign(ctx, msg)
	require.NoError(t, err)

	sig3, err := testSigners[3].Sign(ctx, msg)
	require.NoError(t, err)

	sig5, err := testSigners[5].Sign(ctx, msg)
	require.NoError(t, err)

	proof.AddSignature(sig0, testPubKeys[0])
	proof.AddSignature(sig1, testPubKeys[1])
	proof.AddSignature(sig3, testPubKeys[3])
	proof.AddSignature(sig5, testPubKeys[5])

	fin := s.Finalize(proof, nil)

	require.Len(t, fin.Keys, len(testPubKeys))
	require.Equal(t, "ignored_hash", fin.PubKeyHash)

	require.Equal(t, msg, fin.MainMessage)
	require.Len(t, fin.MainSignatures, 1)

	// Rest is nil if all present votes were in favor of the block.
	require.Nil(t, fin.Rest)

	// Aggregate the key manually and make sure it matches.
	aggP2 := new(blst.P2).Add(
		(*blst.P2Affine)(&testPubKeys[0]),
	).Add(
		(*blst.P2Affine)(&testPubKeys[1]),
	).Add(
		(*blst.P2Affine)(&testPubKeys[3]),
	).Add(
		(*blst.P2Affine)(&testPubKeys[5]),
	).ToAffine()
	aggKey := gblsminsig.PubKey(*aggP2)

	require.True(t, aggKey.Verify(msg, fin.MainSignatures[0].Sig))

	// And we need to validate it through the scheme too.
	signBits, unique := s.ValidateFinalizedProof(
		fin,
		map[string]string{
			string(fin.MainMessage): string(msg),
		},
	)

	require.True(t, unique)
	require.Len(t, signBits, 1)
	bs := signBits[string(msg)]
	require.NotNil(t, bs)
	require.Equal(t, uint(4), bs.Count())

	require.True(t, bs.Test(0))
	require.True(t, bs.Test(1))
	require.True(t, bs.Test(3))
	require.True(t, bs.Test(5))

	// If we modify the key combination, then the calculated key will differ,
	// so validation should fail.
	finCopy := fin
	finCopy.MainSignatures[0].KeyID = bytes.Clone(fin.MainSignatures[0].KeyID)
	finCopy.MainSignatures[0].KeyID[2]++

	signBits, unique = s.ValidateFinalizedProof(
		fin,
		map[string]string{
			string(fin.MainMessage): string(msg),
		},
	)

	require.False(t, unique)
	require.Nil(t, signBits)

	// Likewise, if we modify the signature, validation also fails.
	finCopy = fin
	finCopy.MainSignatures[0].Sig = bytes.Clone(fin.MainSignatures[0].Sig)
	finCopy.MainSignatures[0].Sig[0]++

	signBits, unique = s.ValidateFinalizedProof(
		fin,
		map[string]string{
			string(fin.MainMessage): string(msg),
		},
	)

	require.False(t, unique)
	require.Nil(t, signBits)
}
