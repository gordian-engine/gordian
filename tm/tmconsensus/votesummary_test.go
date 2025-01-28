package tmconsensus_test

import (
	"context"
	"testing"

	"github.com/gordian-engine/gordian/tm/tmconsensus"
	"github.com/gordian-engine/gordian/tm/tmconsensus/tmconsensustest"
	"github.com/stretchr/testify/require"
)

func TestVoteSummary_powers(t *testing.T) {
	t.Parallel()

	fx := tmconsensustest.NewEd25519Fixture(4)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	vals := fx.Vals()

	vs := tmconsensus.NewVoteSummary()
	vs.SetAvailablePower(vals)

	prevoteMap := fx.PrevoteProofMap(ctx, 1, 0, map[string][]int{
		"":           {0},
		"some_block": {1, 2, 3},
	})

	precommitMap := fx.PrevoteProofMap(ctx, 1, 0, map[string][]int{
		"":           {0},
		"some_block": {1, 2, 3},
	})

	vs.SetVotePowers(vals, prevoteMap, precommitMap)
	nilPow := vals[0].Power
	blockPow := vals[1].Power + vals[2].Power + vals[3].Power

	require.Equal(t, nilPow+blockPow, vs.AvailablePower)

	t.Run("prevotes", func(t *testing.T) {
		require.Equal(t, vs.AvailablePower, vs.TotalPrevotePower)
		require.Equal(t, "some_block", vs.MostVotedPrevoteHash)
		require.Equal(t, map[string]uint64{
			"":           nilPow,
			"some_block": blockPow,
		}, vs.PrevoteBlockPower)
	})

	t.Run("precommits", func(t *testing.T) {
		require.Equal(t, vs.AvailablePower, vs.TotalPrecommitPower)
		require.Equal(t, "some_block", vs.MostVotedPrecommitHash)
		require.Equal(t, map[string]uint64{
			"":           nilPow,
			"some_block": blockPow,
		}, vs.PrecommitBlockPower)
	})
}
