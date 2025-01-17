package sigtree_test

import (
	"context"
	"iter"
	"testing"

	"github.com/gordian-engine/gordian/gcrypto/gblsminsig"
	"github.com/gordian-engine/gordian/gcrypto/gblsminsig/internal/sigtree"
	"github.com/stretchr/testify/require"
	blst "github.com/supranational/blst/bindings/go"
)

var (
	testSigners [16]gblsminsig.Signer
	testPubKeys [16]gblsminsig.PubKey
)

func init() {
	for i := range testSigners {
		ikm := [32]byte{}
		for j := range ikm {
			ikm[j] = byte(i)
		}

		s, err := gblsminsig.NewSigner(ikm[:])
		if err != nil {
			panic(err)
		}

		testSigners[i] = s

		testPubKeys[i] = s.PubKey().(gblsminsig.PubKey)
	}
}

func TestTree_indexing(t *testing.T) {
	t.Run("2 keys", func(t *testing.T) {
		t.Parallel()

		tree := sigtree.New(keysSeq(2), 2)

		// Tree layout:
		//   0 1
		//    2

		requireKeyAtIndex(t, tree, 0, testPubKeys[0])
		requireKeyAtIndex(t, tree, 1, testPubKeys[1])

		agg01 := new(blst.P2).Add(
			(*blst.P2Affine)(&testPubKeys[0]),
		).Add(
			(*blst.P2Affine)(&testPubKeys[1]),
		).ToAffine()
		requireP2AtIndex(t, tree, 2, *agg01)

		require.Equal(t, -1, tree.Index(blst.P2Affine(testPubKeys[5])))
	})

	t.Run("3 keys", func(t *testing.T) {
		t.Parallel()

		tree := sigtree.New(keysSeq(3), 3)

		// Tree layout:
		//   0 1 2 (3)
		//    4   (5)
		//      6
		// Element 3 is padding, and element 5 is effectively aliased to 2.

		requireKeyAtIndex(t, tree, 0, testPubKeys[0])
		requireKeyAtIndex(t, tree, 1, testPubKeys[1])
		requireKeyAtIndex(t, tree, 2, testPubKeys[2])

		// Padding elements.
		// requireKeyAtIndex works for the first blank key,
		// but for later padded blank keys,
		// we cannot look up the key value.
		requireKeyAtIndex(t, tree, 3, gblsminsig.PubKey{})
		requirePaddingMergedKeyAtIndex(t, tree, 5, testPubKeys[2])

		agg01 := new(blst.P2).Add(
			(*blst.P2Affine)(&testPubKeys[0]),
		).Add(
			(*blst.P2Affine)(&testPubKeys[1]),
		).ToAffine()
		requireP2AtIndex(t, tree, 4, *agg01)

		agg012 := new(blst.P2).Add(
			agg01,
		).Add(
			(*blst.P2Affine)(&testPubKeys[2]),
		).ToAffine()
		requireP2AtIndex(t, tree, 6, *agg012)

		require.Equal(t, -1, tree.Index(blst.P2Affine(testPubKeys[5])))
	})

	t.Run("4 keys", func(t *testing.T) {
		t.Parallel()

		tree := sigtree.New(keysSeq(4), 4)

		// Tree layout:
		//   0 1 2 3
		//    4   5
		//      6

		requireKeyAtIndex(t, tree, 0, testPubKeys[0])
		requireKeyAtIndex(t, tree, 1, testPubKeys[1])
		requireKeyAtIndex(t, tree, 2, testPubKeys[2])
		requireKeyAtIndex(t, tree, 3, testPubKeys[3])

		agg01 := new(blst.P2).Add(
			(*blst.P2Affine)(&testPubKeys[0]),
		).Add(
			(*blst.P2Affine)(&testPubKeys[1]),
		).ToAffine()
		requireP2AtIndex(t, tree, 4, *agg01)

		agg23 := new(blst.P2).Add(
			(*blst.P2Affine)(&testPubKeys[2]),
		).Add(
			(*blst.P2Affine)(&testPubKeys[3]),
		).ToAffine()
		requireP2AtIndex(t, tree, 5, *agg23)

		agg0123 := new(blst.P2).Add(agg01).Add(agg23).ToAffine()
		requireP2AtIndex(t, tree, 6, *agg0123)

		require.Equal(t, -1, tree.Index(blst.P2Affine(testPubKeys[5])))
	})
}

func TestTree_AddSignature(t *testing.T) {
	t.Parallel()

	tree := sigtree.New(keysSeq(2), 2)

	ctx := context.Background()
	msg := []byte("hello")

	sig0Bytes, err := testSigners[0].Sign(ctx, msg)
	require.NoError(t, err)

	sig0 := new(blst.P1Affine)
	sig0 = sig0.Uncompress(sig0Bytes)

	sig1Bytes, err := testSigners[1].Sign(ctx, msg)
	require.NoError(t, err)

	sig1 := new(blst.P1Affine)
	sig1 = sig1.Uncompress(sig1Bytes)

	tree.AddSignature(0, *sig0)
	require.Equal(t, uint(1), tree.SigBits.Count())
	require.True(t, tree.SigBits.Test(0))

	tree.AddSignature(1, *sig1)
	require.Equal(t, uint(2), tree.SigBits.Count())
	require.True(t, tree.SigBits.Test(0))
	require.True(t, tree.SigBits.Test(1))

	_, gotSig, ok := tree.Get(2)
	require.True(t, ok)

	expSig := new(blst.P1).Add(sig0).Add(sig1).ToAffine()
	require.True(t, expSig.Equals(&gotSig))
}

func TestTree_AddSignature_root(t *testing.T) {
	t.Parallel()

	tree := sigtree.New(keysSeq(2), 2)

	ctx := context.Background()
	msg := []byte("hello")

	sig0Bytes, err := testSigners[0].Sign(ctx, msg)
	require.NoError(t, err)

	sig0 := new(blst.P1Affine)
	sig0 = sig0.Uncompress(sig0Bytes)

	sig1Bytes, err := testSigners[1].Sign(ctx, msg)
	require.NoError(t, err)

	sig1 := new(blst.P1Affine)
	sig1 = sig1.Uncompress(sig1Bytes)

	aggSig := new(blst.P1).Add(sig0).Add(sig1).ToAffine()

	tree.AddSignature(2, *aggSig)

	_, gotSig, ok := tree.Get(2)
	require.True(t, ok)

	require.True(t, aggSig.Equals(&gotSig))

	require.Equal(t, uint(2), tree.SigBits.Count())
	require.True(t, tree.SigBits.Test(0))
	require.True(t, tree.SigBits.Test(1))
}

func TestTree_AddSignature_cascadesUpward(t *testing.T) {
	t.Parallel()

	tree := sigtree.New(keysSeq(4), 4)

	// Tree layout:
	//   0 1 2 3
	//    4   5
	//      6

	ctx := context.Background()
	msg := []byte("hello")

	sig0Bytes, err := testSigners[0].Sign(ctx, msg)
	require.NoError(t, err)
	sig0 := new(blst.P1Affine)
	sig0 = sig0.Uncompress(sig0Bytes)
	tree.AddSignature(0, *sig0)

	sig1Bytes, err := testSigners[1].Sign(ctx, msg)
	require.NoError(t, err)
	sig1 := new(blst.P1Affine)
	sig1 = sig1.Uncompress(sig1Bytes)
	tree.AddSignature(1, *sig1)

	sig2Bytes, err := testSigners[2].Sign(ctx, msg)
	require.NoError(t, err)
	sig2 := new(blst.P1Affine)
	sig2 = sig2.Uncompress(sig2Bytes)
	tree.AddSignature(2, *sig2)

	// Now that we've added all three individually,
	// this last signature should trigger the 2-3 aggregation
	// which should trigger the 0-1-2-3 aggregation.
	sig3Bytes, err := testSigners[3].Sign(ctx, msg)
	require.NoError(t, err)
	sig3 := new(blst.P1Affine)
	sig3 = sig3.Uncompress(sig3Bytes)
	tree.AddSignature(3, *sig3)

	expRootSig := new(blst.P1).
		Add(sig0).Add(sig1).Add(sig2).Add(sig3).
		ToAffine()

	_, gotSig, ok := tree.Get(6)
	require.True(t, ok)
	require.True(t, gotSig.Equals(expRootSig))

	require.Equal(t, uint(4), tree.SigBits.Count())
	require.True(t, tree.SigBits.Test(0))
	require.True(t, tree.SigBits.Test(1))
	require.True(t, tree.SigBits.Test(2))
	require.True(t, tree.SigBits.Test(3))
}

func TestTree_AddSignature_withPadding(t *testing.T) {
	t.Parallel()

	tree := sigtree.New(keysSeq(3), 3)

	// Tree layout:
	//   0 1 2 (3)
	//    4   (5)
	//      6
	// Element 3 is padding, and element 5 is effectively aliased to 2.

	requireKeyAtIndex(t, tree, 0, testPubKeys[0])
	requireKeyAtIndex(t, tree, 1, testPubKeys[1])
	requireKeyAtIndex(t, tree, 2, testPubKeys[2])

	ctx := context.Background()
	msg := []byte("hello")

	sig2Bytes, err := testSigners[2].Sign(ctx, msg)
	require.NoError(t, err)
	sig2 := new(blst.P1Affine)
	sig2 = sig2.Uncompress(sig2Bytes)
	tree.AddSignature(2, *sig2)

	// Getting the direct signature should still work with padding, of course.
	_, gotSig, ok := tree.Get(2)
	require.True(t, ok)
	require.True(t, gotSig.Equals(sig2))

	// Index 5 is aggregation of index 2 with nothing at index 3,
	// so index 5 should be present and should be the same as 2.
	_, gotSig, ok = tree.Get(5)
	require.True(t, ok)
	require.True(t, gotSig.Equals(sig2))

	// And since we've only added 2, that should still be the only set bit.
	require.Equal(t, uint(1), tree.SigBits.Count())
	require.True(t, tree.SigBits.Test(2))
}

func TestTree_SparseIndices(t *testing.T) {
	t.Parallel()

	tree := sigtree.New(keysSeq(4), 4)

	ctx := context.Background()
	msg := []byte("hello")

	// Tree layout:
	//   0 1 2 3
	//    4   5
	//      6

	sig0Bytes, err := testSigners[0].Sign(ctx, msg)
	require.NoError(t, err)
	sig0 := new(blst.P1Affine)
	sig0 = sig0.Uncompress(sig0Bytes)
	tree.AddSignature(0, *sig0)

	// Just 0, is the only reported index.
	ids := tree.SparseIndices(nil)
	require.Equal(t, []int{0}, ids)

	sig1Bytes, err := testSigners[1].Sign(ctx, msg)
	require.NoError(t, err)
	sig1 := new(blst.P1Affine)
	sig1 = sig1.Uncompress(sig1Bytes)
	tree.AddSignature(1, *sig1)

	// Adding 1, aggregates into index 4.
	ids = tree.SparseIndices(ids[:0])
	require.Equal(t, []int{4}, ids)

	sig2Bytes, err := testSigners[2].Sign(ctx, msg)
	require.NoError(t, err)
	sig2 := new(blst.P1Affine)
	sig2 = sig2.Uncompress(sig2Bytes)
	tree.AddSignature(2, *sig2)

	// Adding 2 is a new standalone.
	ids = tree.SparseIndices(ids[:0])
	require.Equal(t, []int{4, 2}, ids)

	sig3Bytes, err := testSigners[3].Sign(ctx, msg)
	require.NoError(t, err)
	sig3 := new(blst.P1Affine)
	sig3 = sig3.Uncompress(sig3Bytes)
	tree.AddSignature(3, *sig3)

	// Finally, adding 3 goes to the root.
	ids = tree.SparseIndices(ids[:0])
	require.Equal(t, []int{6}, ids)
}

func TestTree_Finalized(t *testing.T) {
	t.Parallel()

	tree := sigtree.New(keysSeq(4), 4)

	ctx := context.Background()
	msg := []byte("hello")

	// Tree layout:
	//   0 1 2 3
	//    4   5
	//      6

	// Add signature 0.
	sig0Bytes, err := testSigners[0].Sign(ctx, msg)
	require.NoError(t, err)
	sig0 := new(blst.P1Affine)
	sig0 = sig0.Uncompress(sig0Bytes)
	tree.AddSignature(0, *sig0)

	// Add signature 1.
	sig1Bytes, err := testSigners[1].Sign(ctx, msg)
	require.NoError(t, err)
	sig1 := new(blst.P1Affine)
	sig1 = sig1.Uncompress(sig1Bytes)
	tree.AddSignature(1, *sig1)

	// Add signature 3.
	sig3Bytes, err := testSigners[3].Sign(ctx, msg)
	require.NoError(t, err)
	sig3 := new(blst.P1Affine)
	sig3 = sig3.Uncompress(sig3Bytes)
	tree.AddSignature(3, *sig3)

	expKey := new(blst.P2).
		Add((*blst.P2Affine)(&testPubKeys[0])).
		Add((*blst.P2Affine)(&testPubKeys[1])).
		Add((*blst.P2Affine)(&testPubKeys[3])).
		ToAffine()
	expSig := new(blst.P1).
		Add(sig0).Add(sig1).Add(sig3).
		ToAffine()

	finKey, finSig := tree.Finalized()
	require.True(t, expKey.Equals(&finKey))
	require.True(t, expSig.Equals(&finSig))
}

func keysSeq(n int) iter.Seq[blst.P2Affine] {
	return func(yield func(blst.P2Affine) bool) {
		for _, pk := range testPubKeys[:n] {
			if !yield(blst.P2Affine(pk)) {
				return
			}
		}
	}
}

func requireKeyAtIndex(t *testing.T, tree sigtree.Tree, expIdx int, expKey gblsminsig.PubKey) {
	t.Helper()

	requireP2AtIndex(t, tree, expIdx, blst.P2Affine(expKey))
}

func requireP2AtIndex(t *testing.T, tree sigtree.Tree, expIdx int, expP2 blst.P2Affine) {
	t.Helper()

	require.Equal(t, expIdx, tree.Index(expP2))

	k, _, ok := tree.Get(expIdx)
	require.True(t, ok)
	require.True(t, expP2.Equals(&k))
}

func requirePaddingMergedKeyAtIndex(t *testing.T, tree sigtree.Tree, expIdx int, expKey gblsminsig.PubKey) {
	t.Helper()

	k, _, ok := tree.Get(expIdx)
	require.True(t, ok)
	require.True(t, k.Equals((*blst.P2Affine)(&expKey)))
}

func requireP2PaddingAtIndex(t *testing.T, tree sigtree.Tree, expIdx int) {
	t.Helper()

	k, _, ok := tree.Get(expIdx)
	require.True(t, ok)
	require.True(t, new(blst.P2Affine).Equals(&k))
}
