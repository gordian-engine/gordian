package gblsminsig_test

import (
	"context"
	"testing"

	"github.com/bits-and-blooms/bitset"
	"github.com/gordian-engine/gordian/gcrypto/gblsminsig"
	"github.com/gordian-engine/gordian/gcrypto/gblsminsig/gblsminsigtest"
	"github.com/stretchr/testify/require"
	blst "github.com/supranational/blst/bindings/go"
)

func TestSignatureProof_AddSignature(t *testing.T) {
	t.Parallel()

	msg := []byte("hello")

	keys := gblsminsigtest.DeterministicPubKeys(2)

	proof, err := gblsminsig.NewSignatureProof(msg, keys, "ignored")
	require.NoError(t, err)

	ctx := context.Background()

	sig0, err := gblsminsigtest.DeterministicSigners(1)[0].Sign(ctx, msg)
	require.NoError(t, err)

	require.NoError(t, proof.AddSignature(sig0, keys[0]))
}

func TestSignatureProof_AsSparse(t *testing.T) {
	t.Parallel()

	msg := []byte("hello")

	keys := gblsminsigtest.DeterministicPubKeys(16)
	const hash = "fake_hash"
	proof, err := gblsminsig.NewSignatureProof(msg, keys, hash)
	require.NoError(t, err)

	ctx := context.Background()

	signers := gblsminsigtest.DeterministicSigners(16)
	sig0, err := signers[0].Sign(ctx, msg)
	require.NoError(t, err)

	require.NoError(t, proof.AddSignature(sig0, keys[0]))

	sp := proof.AsSparse()
	require.Equal(t, hash, sp.PubKeyHash)
	require.Len(t, sp.Signatures, 1)
	require.Equal(t, sig0, sp.Signatures[0].Sig)

	// The sparse signature's key ID is the little endian uint16
	// corresponding to the index of the key in the tree.
	require.Equal(t, []byte{0, 0}, sp.Signatures[0].KeyID)

	// Now we can add a far-away signature with a more interesting ID.
	sig15, err := signers[15].Sign(ctx, msg)
	require.NoError(t, err)
	require.NoError(t, proof.AddSignature(sig15, keys[15]))

	sp = proof.AsSparse()
	require.Equal(t, hash, sp.PubKeyHash)
	require.Len(t, sp.Signatures, 2)
	require.Equal(t, []byte{0, 0}, sp.Signatures[0].KeyID)
	require.Equal(t, sig0, sp.Signatures[0].Sig)
	require.Equal(t, []byte{0, 15}, sp.Signatures[1].KeyID)
	require.Equal(t, sig15, sp.Signatures[1].Sig)

	// And then if we aggregate in signature 14...
	sig14, err := signers[14].Sign(ctx, msg)
	require.NoError(t, err)
	require.NoError(t, proof.AddSignature(sig14, keys[14]))

	// Then the aggregated signature is first.
	p15 := new(blst.P1Affine).Uncompress(sig15)
	p14 := new(blst.P1Affine).Uncompress(sig14)
	aggSig := new(blst.P1).Add(p14).Add(p15).ToAffine()

	sp = proof.AsSparse()
	require.Equal(t, hash, sp.PubKeyHash)
	require.Len(t, sp.Signatures, 2)
	require.Equal(t, []byte{0, 16 + 8 - 1}, sp.Signatures[0].KeyID)
	require.Equal(t, aggSig.Compress(), sp.Signatures[0].Sig)
	require.Equal(t, []byte{0, 0}, sp.Signatures[1].KeyID)
	require.Equal(t, sig0, sp.Signatures[1].Sig)
}

func TestSignatureProof_MergeSparse_disjoint(t *testing.T) {
	t.Parallel()

	msg := []byte("hello")

	const hash = "fake_hash"
	keys := gblsminsigtest.DeterministicPubKeys(8)
	proof0, err := gblsminsig.NewSignatureProof(msg, keys, hash)
	require.NoError(t, err)

	ctx := context.Background()

	signers := gblsminsigtest.DeterministicSigners(8)
	sig0, err := signers[0].Sign(ctx, msg)
	require.NoError(t, err)

	require.NoError(t, proof0.AddSignature(sig0, keys[0]))

	proof2, err := gblsminsig.NewSignatureProof(msg, keys, hash)
	require.NoError(t, err)

	sig2, err := signers[2].Sign(ctx, msg)
	require.NoError(t, err)

	require.NoError(t, proof2.AddSignature(sig2, keys[2]))

	res := proof0.MergeSparse(proof2.AsSparse())
	require.True(t, res.AllValidSignatures)
	require.True(t, res.IncreasedSignatures)
	require.False(t, res.WasStrictSuperset)

	var bs0 bitset.BitSet
	proof0.SignatureBitSet(&bs0)
	require.Equal(t, uint(2), bs0.Count())
	require.True(t, bs0.Test(0))
	require.True(t, bs0.Test(2))
}

func TestSignatureProof_HasSparseKeyID(t *testing.T) {
	t.Parallel()

	msg := []byte("hello")

	const hash = "fake_hash"
	keys := gblsminsigtest.DeterministicPubKeys(4)
	proof0, err := gblsminsig.NewSignatureProof(msg, keys, hash)
	require.NoError(t, err)

	ctx := context.Background()

	signers := gblsminsigtest.DeterministicSigners(4)
	sig0, err := signers[0].Sign(ctx, msg)
	require.NoError(t, err)

	require.NoError(t, proof0.AddSignature(sig0, keys[0]))

	sp0 := proof0.AsSparse()
	has, valid := proof0.HasSparseKeyID(sp0.Signatures[0].KeyID)
	require.True(t, valid)
	require.True(t, has)

	proof1, err := gblsminsig.NewSignatureProof(msg, keys, hash)
	require.NoError(t, err)

	// New proof doesn't have the other signature yet, of course.
	has, valid = proof1.HasSparseKeyID(sp0.Signatures[0].KeyID)
	require.True(t, valid)
	require.False(t, has)

	sig1, err := signers[1].Sign(ctx, msg)
	require.NoError(t, err)

	require.NoError(t, proof1.AddSignature(sig1, keys[1]))

	sp1 := proof1.AsSparse()
	has, valid = proof1.HasSparseKeyID(sp1.Signatures[0].KeyID)
	require.True(t, valid)
	require.True(t, has)
}
