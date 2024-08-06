package tsi_test

import (
	"context"
	"testing"

	"github.com/rollchains/gordian/tm/tmconsensus"
	"github.com/rollchains/gordian/tm/tmconsensus/tmconsensustest"
	"github.com/rollchains/gordian/tm/tmengine/internal/tmstate/internal/tsi"
	"github.com/stretchr/testify/require"
)

func TestStepFromVoteSummary(t *testing.T) {
	t.Run("with no proposed blocks, prevotes, or precommits", func(t *testing.T) {
		t.Parallel()

		_, rv := makeRV(2)

		s := tsi.GetStepFromVoteSummary(rv.VoteSummary)
		expectStep(t, tsi.StepAwaitingProposal, s)
	})

	t.Run("with minority prevote power present", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		fx, rv := makeRV(4)
		vs := rv.VoteSummary
		pb1 := fx.NextProposedBlock([]byte("app_data_1"), 0)
		prevoteMap := fx.PrevoteProofMap(ctx, 1, 0, map[string][]int{
			string(pb1.Block.Hash): {0, 1},
		})
		vs.SetPrevotePowers(fx.Vals(), prevoteMap)

		s := tsi.GetStepFromVoteSummary(vs)
		expectStep(t, tsi.StepAwaitingProposal, s)
	})

	t.Run("with majority prevote power present for one block", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		fx, rv := makeRV(4)
		vs := rv.VoteSummary
		pb1 := fx.NextProposedBlock([]byte("app_data_1"), 0)
		prevoteMap := fx.PrevoteProofMap(ctx, 1, 0, map[string][]int{
			string(pb1.Block.Hash): {0, 1, 2},
		})
		vs.SetPrevotePowers(fx.Vals(), prevoteMap)

		s := tsi.GetStepFromVoteSummary(vs)
		expectStep(t, tsi.StepAwaitingPrecommits, s)
	})

	t.Run("with split majority prevote power present", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		fx, rv := makeRV(4)
		vs := rv.VoteSummary
		pb1 := fx.NextProposedBlock([]byte("app_data_1"), 0)
		prevoteMap := fx.PrevoteProofMap(ctx, 1, 0, map[string][]int{
			string(pb1.Block.Hash): {0, 1},
			"":                     {2},
		})
		vs.SetPrevotePowers(fx.Vals(), prevoteMap)

		s := tsi.GetStepFromVoteSummary(vs)
		expectStep(t, tsi.StepPrevoteDelay, s)
	})

	t.Run("with minority precommit power present", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		fx, rv := makeRV(4)
		vs := rv.VoteSummary
		pb1 := fx.NextProposedBlock([]byte("app_data_1"), 0)
		precommitMap := fx.PrecommitProofMap(ctx, 1, 0, map[string][]int{
			string(pb1.Block.Hash): {0, 1},
		})
		vs.SetPrecommitPowers(fx.Vals(), precommitMap)

		s := tsi.GetStepFromVoteSummary(vs)
		expectStep(t, tsi.StepAwaitingPrecommits, s)
	})

	t.Run("with majority precommit power present", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		fx, rv := makeRV(4)
		vs := rv.VoteSummary
		pb1 := fx.NextProposedBlock([]byte("app_data_1"), 0)
		precommitMap := fx.PrecommitProofMap(ctx, 1, 0, map[string][]int{
			string(pb1.Block.Hash): {0, 1, 2},
		})
		vs.SetPrecommitPowers(fx.Vals(), precommitMap)

		s := tsi.GetStepFromVoteSummary(vs)
		expectStep(t, tsi.StepCommitWait, s)
	})

	t.Run("with split majority precommit power present", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		fx, rv := makeRV(4)
		vs := rv.VoteSummary
		pb1 := fx.NextProposedBlock([]byte("app_data_1"), 0)
		precommitMap := fx.PrecommitProofMap(ctx, 1, 0, map[string][]int{
			string(pb1.Block.Hash): {0, 1},
			"":                     {2},
		})
		vs.SetPrecommitPowers(fx.Vals(), precommitMap)

		s := tsi.GetStepFromVoteSummary(vs)
		expectStep(t, tsi.StepPrecommitDelay, s)
	})
}

// expectStep requires that want and got are equal.
// If they differ, the failure message includes the string version of the values,
// so that one does not need to put .String() in all Step value assertions.
func expectStep(t *testing.T, want, got tsi.Step) {
	t.Helper()

	require.Equalf(t, want, got, "expected %q but got %q", want, got)
}

func makeRV(nVals int) (*tmconsensustest.StandardFixture, tmconsensus.RoundView) {
	fx := tmconsensustest.NewStandardFixture(nVals)

	rv := tmconsensus.RoundView{
		Height: 1,
		Round:  0,

		Validators: fx.Vals(),

		VoteSummary: tmconsensus.NewVoteSummary(),
	}
	rv.VoteSummary.SetAvailablePower(rv.Validators)

	return fx, rv
}
