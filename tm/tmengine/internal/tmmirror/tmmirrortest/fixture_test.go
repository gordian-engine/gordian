package tmmirrortest_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/rollchains/gordian/tm/tmengine/internal/tmmirror"
	"github.com/rollchains/gordian/tm/tmengine/internal/tmmirror/internal/tmi"
	"github.com/rollchains/gordian/tm/tmengine/internal/tmmirror/tmmirrortest"
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
			pbs, _, precommits, err := mfx.Cfg.RoundStore.LoadRoundState(ctx, 1, 0)
			require.NoError(t, err)

			require.Len(t, pbs, 1)

			pb := pbs[0]
			require.Equal(t, []byte("initial_height"), pb.Block.DataID)

			require.Len(t, precommits, 1)
			precommitProof := precommits[string(pb.Block.Hash)]
			require.NotNil(t, precommitProof)

			require.Equal(t, uint(nVals), precommitProof.SignatureBitSet().Count())

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
			nextPB := mfx.Fx.NextProposedBlock([]byte("x"), 0)

			require.Equal(t, uint64(2), nextPB.Block.Height)
			require.Zero(t, nextPB.Round)
		})
	}
}
