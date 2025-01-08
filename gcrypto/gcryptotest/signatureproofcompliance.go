package gcryptotest

import (
	"context"
	"testing"

	"github.com/bits-and-blooms/bitset"
	"github.com/gordian-engine/gordian/gcrypto"
	"github.com/stretchr/testify/require"
)

// TestCommonMessageSignatureProofCompliance_Ed25519 tests the basic features of
// an implementation of CommonMessageSignatureProof compatible with ed25519 signatures.
//
// TODO: this signature will likely change in the future
// to accommodate other types of public keys, and to be aware
// of the presence or absence of particular features of a proof.
func TestCommonMessageSignatureProofCompliance_Ed25519(
	t *testing.T,
	s gcrypto.CommonMessageSignatureProofScheme,
) {
	t.Parallel()

	ctx := context.Background()

	signers := DeterministicEd25519Signers(4)

	edPubKey1 := signers[0].PubKey().(gcrypto.Ed25519PubKey)
	edPubKey2 := signers[1].PubKey().(gcrypto.Ed25519PubKey)
	edPubKey3 := signers[2].PubKey().(gcrypto.Ed25519PubKey)
	edPubKey4 := signers[3].PubKey().(gcrypto.Ed25519PubKey)

	hello := []byte("hello")

	helloSig1, err := signers[0].Sign(ctx, hello)
	require.NoError(t, err)

	helloSig2, err := signers[1].Sign(ctx, hello)
	require.NoError(t, err)

	helloSig3, err := signers[2].Sign(ctx, hello)
	require.NoError(t, err)

	helloSig4, err := signers[3].Sign(ctx, hello)
	require.NoError(t, err)

	t.Run("Message", func(t *testing.T) {
		t.Parallel()

		p, err := s.New(hello, []gcrypto.PubKey{edPubKey1, edPubKey2}, "myhash")
		require.NoError(t, err)

		require.Equal(t, hello, p.Message())
	})

	t.Run("AddSignature", func(t *testing.T) {
		t.Run("accepts valid signature", func(t *testing.T) {
			t.Parallel()

			p, err := s.New(hello, []gcrypto.PubKey{edPubKey1, edPubKey2}, "myhash")
			require.NoError(t, err)

			require.NoError(t, p.AddSignature(helloSig1, edPubKey1))
		})

		t.Run("rejects invalid signature from valid key", func(t *testing.T) {
			t.Parallel()

			p, err := s.New(hello, []gcrypto.PubKey{edPubKey1, edPubKey2}, "myhash")
			require.NoError(t, err)

			sig1, err := signers[0].Sign(ctx, []byte("something else"))
			require.NoError(t, err)

			require.ErrorIs(t, p.AddSignature(sig1, edPubKey1), gcrypto.ErrInvalidSignature)
		})

		t.Run("unknown key", func(t *testing.T) {
			t.Parallel()

			p, err := s.New(hello, []gcrypto.PubKey{edPubKey1}, "myhash")
			require.NoError(t, err)

			t.Run("rejected with valid signature", func(t *testing.T) {
				require.ErrorIs(t, p.AddSignature(helloSig2, edPubKey2), gcrypto.ErrUnknownKey)
			})

			t.Run("rejected with invalid signature", func(t *testing.T) {
				sig2, err := signers[1].Sign(ctx, []byte("something else"))
				require.NoError(t, err)

				require.ErrorIs(t, p.AddSignature(sig2, edPubKey2), gcrypto.ErrUnknownKey)
			})
		})
	})

	t.Run("Matches", func(t *testing.T) {
		t.Run("false when only messages differ", func(t *testing.T) {
			t.Parallel()

			keys := []gcrypto.PubKey{edPubKey1, edPubKey2}

			p1, err := s.New([]byte("msg1"), keys, "myhash")
			require.NoError(t, err)

			p2, err := s.New([]byte("msg2"), keys, "myhash")
			require.NoError(t, err)

			require.False(t, p1.Matches(p2))
			require.False(t, p2.Matches(p1))
		})

		t.Run("false when only keys differ", func(t *testing.T) {
			t.Parallel()

			p1, err := s.New(hello, []gcrypto.PubKey{edPubKey1}, "myhash")
			require.NoError(t, err)

			p2, err := s.New(hello, []gcrypto.PubKey{edPubKey2}, "myhash")
			require.NoError(t, err)

			require.False(t, p1.Matches(p2))
			require.False(t, p2.Matches(p1))
		})

		t.Run("false when only hashes differ", func(t *testing.T) {
			t.Parallel()

			p1, err := s.New(hello, []gcrypto.PubKey{edPubKey1}, "myhash1")
			require.NoError(t, err)

			p2, err := s.New(hello, []gcrypto.PubKey{edPubKey1}, "myhash2")
			require.NoError(t, err)

			require.False(t, p1.Matches(p2))
			require.False(t, p2.Matches(p1))
		})

		t.Run("true when all of message, keys, and hashes match", func(t *testing.T) {
			t.Parallel()

			keys := []gcrypto.PubKey{edPubKey1, edPubKey2}

			p1, err := s.New(hello, keys, "myhash")
			require.NoError(t, err)

			p2, err := s.New(hello, keys, "myhash")
			require.NoError(t, err)

			require.True(t, p1.Matches(p2))
			require.True(t, p2.Matches(p1))
		})
	})

	t.Run("Merge", func(t *testing.T) {
		t.Run("all new signatures, no overlap", func(t *testing.T) {
			t.Parallel()

			p1, err := s.New(hello, []gcrypto.PubKey{edPubKey1, edPubKey2, edPubKey3, edPubKey4}, "myhash")
			require.NoError(t, err)

			require.NoError(t, p1.AddSignature(helloSig1, edPubKey1))

			p2, err := s.New(hello, []gcrypto.PubKey{edPubKey1, edPubKey2, edPubKey3, edPubKey4}, "myhash")
			require.NoError(t, err)

			require.NoError(t, p2.AddSignature(helloSig2, edPubKey2))
			require.NoError(t, p2.AddSignature(helloSig3, edPubKey3))
			require.NoError(t, p2.AddSignature(helloSig4, edPubKey4))

			// Preconditions.
			var bs bitset.BitSet
			p1.SignatureBitSet(&bs)
			require.Equal(t, uint(1), bs.Count())
			p2.SignatureBitSet(&bs)
			require.Equal(t, uint(3), bs.Count())

			res := p1.Merge(p2)
			require.Equal(t, gcrypto.SignatureProofMergeResult{
				AllValidSignatures:  true,
				IncreasedSignatures: true,
				WasStrictSuperset:   false,
			}, res)

			p1.SignatureBitSet(&bs)
			require.Equal(t, uint(4), bs.Count())
			p2.SignatureBitSet(&bs)
			require.Equal(t, uint(3), bs.Count()) // p2 unaffected.
		})

		t.Run("no new signatures", func(t *testing.T) {
			t.Parallel()

			p1, err := s.New(hello, []gcrypto.PubKey{edPubKey1, edPubKey2}, "myhash")
			require.NoError(t, err)

			require.NoError(t, p1.AddSignature(helloSig1, edPubKey1))
			require.NoError(t, p1.AddSignature(helloSig2, edPubKey2))

			p2, err := s.New(hello, []gcrypto.PubKey{edPubKey1, edPubKey2}, "myhash")
			require.NoError(t, err)

			require.NoError(t, p2.AddSignature(helloSig2, edPubKey2))

			res := p1.Merge(p2)

			require.Equal(t, gcrypto.SignatureProofMergeResult{
				AllValidSignatures:  true,
				IncreasedSignatures: false,
				WasStrictSuperset:   false,
			}, res)

			var bs bitset.BitSet
			p1.SignatureBitSet(&bs)
			require.Equal(t, uint(2), bs.Count())
			p2.SignatureBitSet(&bs)
			require.Equal(t, uint(1), bs.Count())
		})

		t.Run("a new signature and partial overlap", func(t *testing.T) {
			t.Parallel()

			p1, err := s.New(hello, []gcrypto.PubKey{edPubKey1, edPubKey2, edPubKey3}, "myhash")
			require.NoError(t, err)

			require.NoError(t, p1.AddSignature(helloSig1, edPubKey1))
			require.NoError(t, p1.AddSignature(helloSig2, edPubKey2))

			p2, err := s.New(hello, []gcrypto.PubKey{edPubKey1, edPubKey2, edPubKey3}, "myhash")
			require.NoError(t, err)

			require.NoError(t, p2.AddSignature(helloSig2, edPubKey2))
			require.NoError(t, p2.AddSignature(helloSig3, edPubKey3))

			res := p1.Merge(p2)

			require.Equal(t, gcrypto.SignatureProofMergeResult{
				AllValidSignatures:  true,
				IncreasedSignatures: true,
				WasStrictSuperset:   false,
			}, res)

			var bs bitset.BitSet
			p1.SignatureBitSet(&bs)
			require.Equal(t, uint(3), bs.Count())
			p2.SignatureBitSet(&bs)
			require.Equal(t, uint(2), bs.Count())
		})

		t.Run("strict superset", func(t *testing.T) {
			t.Parallel()

			p1, err := s.New(hello, []gcrypto.PubKey{edPubKey1, edPubKey2, edPubKey3}, "myhash")
			require.NoError(t, err)

			require.NoError(t, p1.AddSignature(helloSig1, edPubKey1))

			p2, err := s.New(hello, []gcrypto.PubKey{edPubKey1, edPubKey2, edPubKey3}, "myhash")
			require.NoError(t, err)

			require.NoError(t, p2.AddSignature(helloSig1, edPubKey1))
			require.NoError(t, p2.AddSignature(helloSig2, edPubKey2))
			require.NoError(t, p2.AddSignature(helloSig3, edPubKey3))

			res := p1.Merge(p2)

			require.Equal(t, gcrypto.SignatureProofMergeResult{
				AllValidSignatures:  true,
				IncreasedSignatures: true,
				WasStrictSuperset:   true,
			}, res)

			var bs bitset.BitSet
			p1.SignatureBitSet(&bs)
			require.Equal(t, uint(3), bs.Count())
			p2.SignatureBitSet(&bs)
			require.Equal(t, uint(3), bs.Count())
		})
	})

	t.Run("Clone", func(t *testing.T) {
		t.Parallel()

		orig, err := s.New(hello, []gcrypto.PubKey{edPubKey1, edPubKey2, edPubKey3}, "myhash")
		require.NoError(t, err)

		clone := orig.Clone()

		require.True(t, orig.Matches(clone))

		t.Run("modifying original does not affect clone", func(t *testing.T) {
			require.NoError(t, orig.AddSignature(helloSig1, edPubKey1))
			var bs bitset.BitSet
			orig.SignatureBitSet(&bs)
			require.Equal(t, uint(1), bs.Count())

			clone.SignatureBitSet(&bs)
			require.Zero(t, bs.Count())
		})

		t.Run("modifying clone does not affect original", func(t *testing.T) {
			require.NoError(t, clone.AddSignature(helloSig2, edPubKey2))
			var origBS, cloneBS bitset.BitSet
			orig.SignatureBitSet(&origBS)
			require.Equal(t, uint(1), origBS.Count())

			clone.SignatureBitSet(&cloneBS)
			require.Zero(t, origBS.Intersection(&cloneBS).Count())
		})

		t.Run("new clone matches updated state", func(t *testing.T) {
			// We've added sig 1 to orig, so the new clone should have it.
			clone := orig.Clone()

			var bs bitset.BitSet
			orig.SignatureBitSet(&bs)
			require.Equal(t, uint(1), bs.Count())

			clone.SignatureBitSet(&bs)
			require.True(t, bs.Test(0))
		})
	})

	t.Run("SignatureBitSet", func(t *testing.T) {
		t.Parallel()

		p, err := s.New(hello, []gcrypto.PubKey{edPubKey1, edPubKey2, edPubKey3, edPubKey4}, "myhash")
		require.NoError(t, err)

		// Starts at zero.
		var bs bitset.BitSet
		p.SignatureBitSet(&bs)
		require.Zero(t, bs.Count())

		require.NoError(t, p.AddSignature(helloSig1, edPubKey1))

		p.SignatureBitSet(&bs)
		require.Equal(t, uint(1), bs.Count())
		require.True(t, bs.Test(0))
	})

	t.Run("AsSparse", func(t *testing.T) {
		t.Run("empty before any signatures added", func(t *testing.T) {
			t.Parallel()

			p, err := s.New(hello, []gcrypto.PubKey{edPubKey1, edPubKey2}, "myhash")
			require.NoError(t, err)

			sparse := p.AsSparse()
			require.Equal(t, "myhash", sparse.PubKeyHash)
			require.Empty(t, sparse.Signatures)
		})

		t.Run("map values contain signatures", func(t *testing.T) {
			t.Parallel()

			p, err := s.New(hello, []gcrypto.PubKey{edPubKey1, edPubKey2}, "myhash")
			require.NoError(t, err)

			require.NoError(t, p.AddSignature(helloSig1, edPubKey1))
			require.NoError(t, p.AddSignature(helloSig2, edPubKey2))

			sparse := p.AsSparse()
			require.Equal(t, "myhash", sparse.PubKeyHash)

			// Not checking specific length, because the proof may have aggregated the signatures.
			// The MergeSparse tests cover that the signatures can be merged back correctly.
			require.NotEmpty(t, sparse.Signatures)
		})

		t.Run("determinism", func(t *testing.T) {
			t.Parallel()

			p, err := s.New(hello, []gcrypto.PubKey{edPubKey1, edPubKey2, edPubKey3, edPubKey4}, "myhash")
			require.NoError(t, err)

			require.NoError(t, p.AddSignature(helloSig1, edPubKey1))
			require.NoError(t, p.AddSignature(helloSig2, edPubKey2))
			require.NoError(t, p.AddSignature(helloSig3, edPubKey3))
			require.NoError(t, p.AddSignature(helloSig4, edPubKey4))

			orig := p.AsSparse()

			for i := 0; i < 30; i++ {
				got := p.AsSparse()
				require.Equal(t, orig, got)
			}
		})
	})

	t.Run("MergeSparse", func(t *testing.T) {
		t.Run("one element", func(t *testing.T) {
			t.Parallel()

			p1, err := s.New(hello, []gcrypto.PubKey{edPubKey1, edPubKey2}, "myhash")
			require.NoError(t, err)

			require.NoError(t, p1.AddSignature(helloSig1, edPubKey1))

			sparse := p1.AsSparse()

			p2, err := s.New(hello, []gcrypto.PubKey{edPubKey1, edPubKey2}, "myhash")
			require.NoError(t, err)
			res := p2.MergeSparse(sparse)
			require.True(t, res.AllValidSignatures)
			require.True(t, res.IncreasedSignatures)
			require.True(t, res.WasStrictSuperset)

			var bs bitset.BitSet
			p2.SignatureBitSet(&bs)
			require.Equal(t, uint(1), bs.Count())
			require.True(t, bs.Test(0))
		})

		t.Run("two elements", func(t *testing.T) {
			t.Parallel()

			p1, err := s.New(hello, []gcrypto.PubKey{edPubKey1, edPubKey2}, "myhash")
			require.NoError(t, err)

			require.NoError(t, p1.AddSignature(helloSig1, edPubKey1))
			require.NoError(t, p1.AddSignature(helloSig2, edPubKey2))

			sparse := p1.AsSparse()

			p2, err := s.New(hello, []gcrypto.PubKey{edPubKey1, edPubKey2}, "myhash")
			require.NoError(t, err)
			res := p2.MergeSparse(sparse)
			require.True(t, res.AllValidSignatures)
			require.True(t, res.IncreasedSignatures)
			require.True(t, res.WasStrictSuperset)

			var bs bitset.BitSet
			p2.SignatureBitSet(&bs)
			require.Equal(t, uint(2), bs.Count())
			require.True(t, bs.Test(0))
			require.True(t, bs.Test(1))
		})

		t.Run("merge all new signatures, but not a strict superset", func(t *testing.T) {
			t.Parallel()

			p1, err := s.New(hello, []gcrypto.PubKey{edPubKey1, edPubKey2, edPubKey3}, "myhash")
			require.NoError(t, err)

			require.NoError(t, p1.AddSignature(helloSig1, edPubKey1))
			require.NoError(t, p1.AddSignature(helloSig2, edPubKey2))

			sparse := p1.AsSparse()

			p2, err := s.New(hello, []gcrypto.PubKey{edPubKey1, edPubKey2, edPubKey3}, "myhash")
			require.NoError(t, err)
			require.NoError(t, p2.AddSignature(helloSig3, edPubKey3))

			res := p2.MergeSparse(sparse)
			require.True(t, res.AllValidSignatures)
			require.True(t, res.IncreasedSignatures)
			require.False(t, res.WasStrictSuperset)

			var bs bitset.BitSet
			p2.SignatureBitSet(&bs)
			require.Equal(t, uint(3), bs.Count())
			require.True(t, bs.Test(0))
			require.True(t, bs.Test(1))
			require.True(t, bs.Test(2))
		})

		t.Run("merging a subset does not mark increased signatures", func(t *testing.T) {
			t.Parallel()

			p1, err := s.New(hello, []gcrypto.PubKey{edPubKey1, edPubKey2, edPubKey3}, "myhash")
			require.NoError(t, err)

			require.NoError(t, p1.AddSignature(helloSig1, edPubKey1))
			require.NoError(t, p1.AddSignature(helloSig2, edPubKey2))

			sparse := p1.AsSparse()

			p2, err := s.New(hello, []gcrypto.PubKey{edPubKey1, edPubKey2, edPubKey3}, "myhash")
			require.NoError(t, err)
			require.NoError(t, p2.AddSignature(helloSig1, edPubKey1))
			require.NoError(t, p2.AddSignature(helloSig2, edPubKey2))
			require.NoError(t, p2.AddSignature(helloSig3, edPubKey3))

			res := p2.MergeSparse(sparse)
			require.True(t, res.AllValidSignatures)
			require.False(t, res.IncreasedSignatures)
			require.False(t, res.WasStrictSuperset)
		})

		t.Run("wrong pub key hash causes otherwise recognized signatures to be ignored", func(t *testing.T) {
			t.Parallel()

			// p1 has only 2 keys.
			p1, err := s.New(hello, []gcrypto.PubKey{edPubKey1, edPubKey2}, "myhash12")
			require.NoError(t, err)

			require.NoError(t, p1.AddSignature(helloSig1, edPubKey1))
			require.NoError(t, p1.AddSignature(helloSig2, edPubKey2))

			sparse := p1.AsSparse()

			// p2 has one more key than p1.
			p2, err := s.New(hello, []gcrypto.PubKey{edPubKey1, edPubKey2, edPubKey3}, "myhash123")
			require.NoError(t, err)

			res := p2.MergeSparse(sparse)
			require.False(t, res.AllValidSignatures)
			require.False(t, res.IncreasedSignatures)
			require.False(t, res.WasStrictSuperset)

			var bs bitset.BitSet
			p2.SignatureBitSet(&bs)
			require.Equal(t, uint(0), bs.Count())
		})

		t.Run("modified sparse signatures", func(t *testing.T) {
			t.Run("unrecognized signature out of bounds is ignored", func(t *testing.T) {
				t.Parallel()

				// p1 has 3 keys.
				p1, err := s.New(hello, []gcrypto.PubKey{edPubKey1, edPubKey2, edPubKey3}, "myhash")
				require.NoError(t, err)

				require.NoError(t, p1.AddSignature(helloSig1, edPubKey1))
				require.NoError(t, p1.AddSignature(helloSig2, edPubKey2))
				require.NoError(t, p1.AddSignature(helloSig3, edPubKey3))

				sparse := p1.AsSparse()

				// p2 has only 2 keys.
				p2, err := s.New(hello, []gcrypto.PubKey{edPubKey1, edPubKey2}, "myhash")
				require.NoError(t, err)

				// Trick the sparse value into having a matching public key hash,
				// even though it has one unrecognized public key.
				sparse.PubKeyHash = string(p2.PubKeyHash())

				res := p2.MergeSparse(sparse)
				require.False(t, res.AllValidSignatures)
				require.True(t, res.IncreasedSignatures)
				require.True(t, res.WasStrictSuperset)

				var bs bitset.BitSet
				p2.SignatureBitSet(&bs)
				require.Equal(t, uint(2), bs.Count())
				require.True(t, bs.Test(0))
				require.True(t, bs.Test(1))
			})

			t.Run("unrecognized signature in bounds is ignored", func(t *testing.T) {
				t.Parallel()

				// p1 has 2 keys, but one is different from p2's values -- (1,3) instead of (1,2).
				p1, err := s.New(hello, []gcrypto.PubKey{edPubKey1, edPubKey3}, "myhash")
				require.NoError(t, err)

				require.NoError(t, p1.AddSignature(helloSig1, edPubKey1))
				require.NoError(t, p1.AddSignature(helloSig3, edPubKey3))

				sparse := p1.AsSparse()

				// p2 has keys (1,2).
				p2, err := s.New(hello, []gcrypto.PubKey{edPubKey1, edPubKey2}, "myhash")
				require.NoError(t, err)

				// Trick the sparse value into having a matching public key hash,
				// even though it has one unrecognized public key.
				sparse.PubKeyHash = string(p2.PubKeyHash())

				res := p2.MergeSparse(sparse)
				require.False(t, res.AllValidSignatures)
				require.True(t, res.IncreasedSignatures)
				require.True(t, res.WasStrictSuperset)

				var bs bitset.BitSet
				p2.SignatureBitSet(&bs)
				require.Equal(t, uint(1), bs.Count())
				require.True(t, bs.Test(0))
			})
		})
	})
}
