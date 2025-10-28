package tmconsensus_test

import (
	"context"
	"testing"

	"github.com/bits-and-blooms/bitset"
	"github.com/gordian-engine/gordian/tm/tmconsensus"
	"github.com/gordian-engine/gordian/tm/tmconsensus/tmconsensustest"
	"github.com/stretchr/testify/require"
)

func TestPrevoteSparseProof_ToFull(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fx := tmconsensustest.NewEd25519Fixture(4)
	valSet := fx.ValSet()
	pubKeys := valSet.PubKeys

	// Sparse proof with a subset of validators.
	voteMap := map[string][]int{
		"block_hash_2": {1, 2},
		"":             {0},
	}

	sparseProofs := fx.SparsePrevoteProofMap(ctx, 4, 9, voteMap)
	sparsePrevoteProof := tmconsensus.PrevoteSparseProof{
		Height:     4,
		Round:      9,
		PubKeyHash: string(valSet.PubKeyHash),
		Proofs:     sparseProofs,
	}

	fullProof, err := sparsePrevoteProof.ToFull(
		fx.CommonMessageSignatureProofScheme,
		fx.SignatureScheme,
		pubKeys,
		string(valSet.PubKeyHash),
	)
	require.NoError(t, err)

	require.Equal(t, uint64(4), fullProof.Height)
	require.Equal(t, uint32(9), fullProof.Round)

	// Proofs for both hashes.
	require.Len(t, fullProof.Proofs, 2)
	require.Contains(t, fullProof.Proofs, "block_hash_2")
	require.Contains(t, fullProof.Proofs, "")

	// Assert correct validators for each proof.
	var bs bitset.BitSet
	fullProof.Proofs["block_hash_2"].SignatureBitSet(&bs)
	require.Equal(t, uint(2), bs.Count())
	require.True(t, bs.Test(1))
	require.True(t, bs.Test(2))

	fullProof.Proofs[""].SignatureBitSet(&bs)
	require.Equal(t, uint(1), bs.Count())
	require.True(t, bs.Test(0))
}
