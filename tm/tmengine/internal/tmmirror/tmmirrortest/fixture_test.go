package tmmirrortest_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/bits-and-blooms/bitset"
	"github.com/gordian-engine/gordian/tm/tmengine/internal/tmmirror"
	"github.com/gordian-engine/gordian/tm/tmengine/internal/tmmirror/internal/tmi"
	"github.com/gordian-engine/gordian/tm/tmengine/internal/tmmirror/tmmirrortest"
	"github.com/stretchr/testify/require"
)

func TestFixture_CommitInitialHeight(t *testing.T) {
	for _, nVals := range []int{2, 4} {
		nVals := nVals
		t.Run(fmt.Sprintf("with %d validators", nVals), func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			mfx := tmmirrortest.NewFixture(ctx, t, nVals)

			committers := make([]int, nVals)
			for i := range committers {
				committers[i] = i
			}
			mfx.CommitInitialHeight(
				ctx,
				[]byte("initial_height"), 0,
				committers,
			)

			// Now assert that the stores have the expected content.
			phs, _, precommits, err := mfx.Cfg.RoundStore.LoadRoundState(ctx, 1, 0)
			require.NoError(t, err)

			require.Len(t, phs, 1)

			ph := phs[0]
			require.Equal(t, []byte("initial_height"), ph.Header.DataID)

			require.Len(t, precommits.BlockSignatures, 1)
			fullPrecommits, err := precommits.ToFullPrecommitProofMap(
				1, 0,
				mfx.Fx.ValSet().PubKeys,
				mfx.Fx.SignatureScheme, mfx.Fx.CommonMessageSignatureProofScheme,
			)
			require.NoError(t, err)

			precommitProof := fullPrecommits[string(ph.Header.Hash)]
			require.NotNil(t, precommitProof)

			var bs bitset.BitSet
			precommitProof.SignatureBitSet(&bs)
			require.Equal(t, uint(nVals), bs.Count())

			// The mirror store has the right height and round.
			nhr, err := tmi.NetworkHeightRoundFromStore(mfx.Cfg.Store.NetworkHeightRound(ctx))
			require.NoError(t, err)

			require.Equal(t, tmmirror.NetworkHeightRound{
				CommittingHeight: 1,
				CommittingRound:  0,
				VotingHeight:     2,
				VotingRound:      0,
			}, nhr)

			// And if we generate another proposed block, it is at the right height.
			nextPH := mfx.Fx.NextProposedHeader([]byte("x"), 0)

			require.Equal(t, uint64(2), nextPH.Header.Height)
			require.Zero(t, nextPH.Round)
		})
	}
}
