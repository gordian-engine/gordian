package gblsminsig_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/gordian-engine/gordian/gcrypto"
	"github.com/gordian-engine/gordian/gcrypto/gblsminsig"
	"github.com/gordian-engine/gordian/gcrypto/gcryptotest"
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

	require.NoError(t, proof.AddSignature(sig0, testPubKeys[0]))
	require.NoError(t, proof.AddSignature(sig1, testPubKeys[1]))
	require.NoError(t, proof.AddSignature(sig3, testPubKeys[3]))
	require.NoError(t, proof.AddSignature(sig5, testPubKeys[5]))

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

func TestFinalize_singleRest_roundTrip(t *testing.T) {
	t.Parallel()

	s := gblsminsig.SignatureProofScheme{}

	mainMsg := []byte("main sign content")
	mainProof, err := s.New(mainMsg, testPubKeys[:], "pub_key_hash")
	require.NoError(t, err)

	ctx := context.Background()

	sig0, err := testSigners[0].Sign(ctx, mainMsg)
	require.NoError(t, err)

	sig1, err := testSigners[1].Sign(ctx, mainMsg)
	require.NoError(t, err)

	sig3, err := testSigners[3].Sign(ctx, mainMsg)
	require.NoError(t, err)

	sig5, err := testSigners[5].Sign(ctx, mainMsg)
	require.NoError(t, err)

	require.NoError(t, mainProof.AddSignature(sig0, testPubKeys[0]))
	require.NoError(t, mainProof.AddSignature(sig1, testPubKeys[1]))
	require.NoError(t, mainProof.AddSignature(sig3, testPubKeys[3]))
	require.NoError(t, mainProof.AddSignature(sig5, testPubKeys[5]))

	// Doesn't really matter if the vote is for nil or another block,
	// but we will say this is for nil anyway.
	nilMsg := []byte("nil sign content")
	nilProof, err := s.New(nilMsg, testPubKeys[:], "pub_key_hash")
	require.NoError(t, err)

	sig2, err := testSigners[2].Sign(ctx, nilMsg)
	require.NoError(t, err)

	sig4, err := testSigners[4].Sign(ctx, nilMsg)
	require.NoError(t, err)

	sig7, err := testSigners[7].Sign(ctx, nilMsg)
	require.NoError(t, err)

	require.NoError(t, nilProof.AddSignature(sig2, testPubKeys[2]))
	require.NoError(t, nilProof.AddSignature(sig4, testPubKeys[4]))
	require.NoError(t, nilProof.AddSignature(sig7, testPubKeys[7]))

	fin := s.Finalize(mainProof, []gcrypto.CommonMessageSignatureProof{nilProof})

	require.Len(t, fin.Keys, len(testPubKeys))
	require.Equal(t, "pub_key_hash", fin.PubKeyHash)

	require.Equal(t, mainMsg, fin.MainMessage)
	require.Len(t, fin.MainSignatures, 1)

	require.Len(t, fin.Rest, 1)                 // One entry in the map.
	require.Len(t, fin.Rest[string(nilMsg)], 1) // One sparse signature entry.

	// Aggregate the main key manually and make sure it matches.
	aggP2 := new(blst.P2).Add(
		(*blst.P2Affine)(&testPubKeys[0]),
	).Add(
		(*blst.P2Affine)(&testPubKeys[1]),
	).Add(
		(*blst.P2Affine)(&testPubKeys[3]),
	).Add(
		(*blst.P2Affine)(&testPubKeys[5]),
	).ToAffine()
	mainAggKey := gblsminsig.PubKey(*aggP2)

	require.True(t, mainAggKey.Verify(mainMsg, fin.MainSignatures[0].Sig))

	// Also aggregate the key for the nil votes.
	aggP2 = new(blst.P2).Add(
		(*blst.P2Affine)(&testPubKeys[2]),
	).Add(
		(*blst.P2Affine)(&testPubKeys[4]),
	).Add(
		(*blst.P2Affine)(&testPubKeys[7]),
	).ToAffine()
	nilAggKey := gblsminsig.PubKey(*aggP2)

	require.True(t, nilAggKey.Verify(nilMsg, fin.Rest[string(nilMsg)][0].Sig))

	// And we need to validate it through the scheme too.
	messageMap := map[string]string{
		string(fin.MainMessage): "main hash",
		string(nilMsg):          "",
	}
	signBitsByHash, unique := s.ValidateFinalizedProof(fin, messageMap)

	require.True(t, unique)
	require.Len(t, signBitsByHash, 2)

	mainBS := signBitsByHash["main hash"]
	require.Equal(t, uint(4), mainBS.Count())
	require.True(t, mainBS.Test(0))
	require.True(t, mainBS.Test(1))
	require.True(t, mainBS.Test(3))
	require.True(t, mainBS.Test(5))

	restBS := signBitsByHash[""]
	require.Equal(t, uint(3), restBS.Count())
	require.True(t, restBS.Test(2))
	require.True(t, restBS.Test(4))
	require.True(t, restBS.Test(7))

	// Then if we modify some of the bits in the finalization,
	// validating it should fail.

	// Change main signature.
	finCopy := gcryptotest.CloneFinalizedCommonMessageSignatureProof(fin)
	finCopy.MainSignatures[0].Sig[0]++
	signBitsByHash, unique = s.ValidateFinalizedProof(finCopy, messageMap)
	require.False(t, unique)
	require.Nil(t, signBitsByHash)

	// Change main key ID.
	finCopy = gcryptotest.CloneFinalizedCommonMessageSignatureProof(fin)
	finCopy.MainSignatures[0].KeyID[0]++
	signBitsByHash, unique = s.ValidateFinalizedProof(finCopy, messageMap)
	require.False(t, unique)
	require.Nil(t, signBitsByHash)

	// Change rest signature.
	finCopy = gcryptotest.CloneFinalizedCommonMessageSignatureProof(fin)
	finCopy.Rest[string(nilMsg)][0].Sig[0]++
	signBitsByHash, unique = s.ValidateFinalizedProof(finCopy, messageMap)
	require.False(t, unique)
	require.Nil(t, signBitsByHash)

	// Change rest key ID.
	finCopy = gcryptotest.CloneFinalizedCommonMessageSignatureProof(fin)
	finCopy.Rest[string(nilMsg)][0].KeyID[1]++
	signBitsByHash, unique = s.ValidateFinalizedProof(finCopy, messageMap)
	require.False(t, unique)
	require.Nil(t, signBitsByHash)
}

func TestFinalize_multipleRest_equalSigCounts_roundTrip(t *testing.T) {
	t.Parallel()

	s := gblsminsig.SignatureProofScheme{}

	mainMsg := []byte("main sign content")
	mainProof, err := s.New(mainMsg, testPubKeys[:], "pub_key_hash")
	require.NoError(t, err)

	ctx := context.Background()

	sig0, err := testSigners[0].Sign(ctx, mainMsg)
	require.NoError(t, err)

	sig1, err := testSigners[1].Sign(ctx, mainMsg)
	require.NoError(t, err)

	sig3, err := testSigners[3].Sign(ctx, mainMsg)
	require.NoError(t, err)

	sig5, err := testSigners[5].Sign(ctx, mainMsg)
	require.NoError(t, err)

	require.NoError(t, mainProof.AddSignature(sig0, testPubKeys[0]))
	require.NoError(t, mainProof.AddSignature(sig1, testPubKeys[1]))
	require.NoError(t, mainProof.AddSignature(sig3, testPubKeys[3]))
	require.NoError(t, mainProof.AddSignature(sig5, testPubKeys[5]))

	// Doesn't really matter if the vote is for nil or another block,
	// but we will say this is for nil anyway.
	nilMsg := []byte("nil sign content")
	nilProof, err := s.New(nilMsg, testPubKeys[:], "pub_key_hash")
	require.NoError(t, err)

	sig2, err := testSigners[2].Sign(ctx, nilMsg)
	require.NoError(t, err)

	sig9, err := testSigners[9].Sign(ctx, nilMsg)
	require.NoError(t, err)

	require.NoError(t, nilProof.AddSignature(sig2, testPubKeys[2]))
	require.NoError(t, nilProof.AddSignature(sig9, testPubKeys[9]))

	// And some other validators voted for a different block.
	otherMsg := []byte("other sign content")
	otherProof, err := s.New(otherMsg, testPubKeys[:], "pub_key_hash")
	require.NoError(t, err)

	sig7, err := testSigners[7].Sign(ctx, otherMsg)
	require.NoError(t, err)

	sig11, err := testSigners[11].Sign(ctx, otherMsg)
	require.NoError(t, err)

	require.NoError(t, otherProof.AddSignature(sig7, testPubKeys[7]))
	require.NoError(t, otherProof.AddSignature(sig11, testPubKeys[11]))

	fin := s.Finalize(mainProof, []gcrypto.CommonMessageSignatureProof{nilProof, otherProof})

	require.Len(t, fin.Keys, len(testPubKeys))
	require.Equal(t, "pub_key_hash", fin.PubKeyHash)

	require.Equal(t, mainMsg, fin.MainMessage)
	require.Len(t, fin.MainSignatures, 1)

	require.Len(t, fin.Rest, 2)                   // Two entries in the map.
	require.Len(t, fin.Rest[string(nilMsg)], 1)   // One sparse signature per entry.
	require.Len(t, fin.Rest[string(otherMsg)], 1) // One sparse signature per entry.

	// Aggregate the main key manually and make sure it matches.
	aggP2 := new(blst.P2).Add(
		(*blst.P2Affine)(&testPubKeys[0]),
	).Add(
		(*blst.P2Affine)(&testPubKeys[1]),
	).Add(
		(*blst.P2Affine)(&testPubKeys[3]),
	).Add(
		(*blst.P2Affine)(&testPubKeys[5]),
	).ToAffine()
	mainAggKey := gblsminsig.PubKey(*aggP2)

	require.True(t, mainAggKey.Verify(mainMsg, fin.MainSignatures[0].Sig))

	// Also aggregate the key for the nil votes.
	aggP2 = new(blst.P2).Add(
		(*blst.P2Affine)(&testPubKeys[2]),
	).Add(
		(*blst.P2Affine)(&testPubKeys[9]),
	).ToAffine()
	nilAggKey := gblsminsig.PubKey(*aggP2)
	t.Logf("nil msg key: %x", aggP2.Compress())

	require.True(t, nilAggKey.Verify(nilMsg, fin.Rest[string(nilMsg)][0].Sig))

	// And finally the one for the other block.
	aggP2 = new(blst.P2).Add(
		(*blst.P2Affine)(&testPubKeys[7]),
	).Add(
		(*blst.P2Affine)(&testPubKeys[11]),
	).ToAffine()
	otherAggKey := gblsminsig.PubKey(*aggP2)
	t.Logf("other key: %x", aggP2.Compress())

	require.True(t, otherAggKey.Verify(otherMsg, fin.Rest[string(otherMsg)][0].Sig))

	// The signatures are as expected,
	// so validate it through the scheme.
	messageMap := map[string]string{
		string(fin.MainMessage): "main hash",
		string(nilMsg):          "",
		string(otherMsg):        "other hash",
	}
	signBitsByHash, unique := s.ValidateFinalizedProof(fin, messageMap)

	require.True(t, unique)
	require.Len(t, signBitsByHash, 3)

	mainBS := signBitsByHash["main hash"]
	require.Equal(t, uint(4), mainBS.Count())
	require.True(t, mainBS.Test(0))
	require.True(t, mainBS.Test(1))
	require.True(t, mainBS.Test(3))
	require.True(t, mainBS.Test(5))

	nilBS := signBitsByHash[""]
	require.Equal(t, uint(2), nilBS.Count())
	require.True(t, nilBS.Test(2))
	require.True(t, nilBS.Test(9))

	otherBS := signBitsByHash["other hash"]
	require.Equal(t, uint(2), otherBS.Count())
	require.True(t, otherBS.Test(7))
	require.True(t, otherBS.Test(11))

	// Then if we modify some of the bits in the finalization,
	// validating it should fail.

	// Change main signature.
	finCopy := gcryptotest.CloneFinalizedCommonMessageSignatureProof(fin)
	finCopy.MainSignatures[0].Sig[0]++
	signBitsByHash, unique = s.ValidateFinalizedProof(finCopy, messageMap)
	require.False(t, unique)
	require.Nil(t, signBitsByHash)

	// Change main key ID.
	finCopy = gcryptotest.CloneFinalizedCommonMessageSignatureProof(fin)
	finCopy.MainSignatures[0].KeyID[0]++
	signBitsByHash, unique = s.ValidateFinalizedProof(finCopy, messageMap)
	require.False(t, unique)
	require.Nil(t, signBitsByHash)

	// Change nil signature.
	finCopy = gcryptotest.CloneFinalizedCommonMessageSignatureProof(fin)
	finCopy.Rest[string(nilMsg)][0].Sig[0]++
	signBitsByHash, unique = s.ValidateFinalizedProof(finCopy, messageMap)
	require.False(t, unique)
	require.Nil(t, signBitsByHash)

	// Change nil key ID.
	finCopy = gcryptotest.CloneFinalizedCommonMessageSignatureProof(fin)
	finCopy.Rest[string(nilMsg)][0].KeyID[1]++
	signBitsByHash, unique = s.ValidateFinalizedProof(finCopy, messageMap)
	require.False(t, unique)
	require.Nil(t, signBitsByHash)

	// Change other signature.
	finCopy = gcryptotest.CloneFinalizedCommonMessageSignatureProof(fin)
	finCopy.Rest[string(otherMsg)][0].Sig[0]++
	signBitsByHash, unique = s.ValidateFinalizedProof(finCopy, messageMap)
	require.False(t, unique)
	require.Nil(t, signBitsByHash)

	// Change other key ID.
	finCopy = gcryptotest.CloneFinalizedCommonMessageSignatureProof(fin)
	finCopy.Rest[string(otherMsg)][0].KeyID[1]++
	signBitsByHash, unique = s.ValidateFinalizedProof(finCopy, messageMap)
	require.False(t, unique)
	require.Nil(t, signBitsByHash)
}

// TODO:
//   - Test just main signature, 100% voted.
//   - Test partial main signature with 100% rest on nil.
