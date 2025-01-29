package tmstoretest

import (
	"context"
	"testing"

	"github.com/gordian-engine/gordian/tm/tmconsensus"
	"github.com/gordian-engine/gordian/tm/tmstore"
	"github.com/stretchr/testify/require"
)

type CommittedHeaderStoreFactory func(cleanup func(func())) (tmstore.CommittedHeaderStore, error)

func TestCommittedHeaderStoreCompliance(t *testing.T, f CommittedHeaderStoreFactory, fxf FixtureFactory) {
	t.Run("happy path", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		s, err := f(t.Cleanup)
		require.NoError(t, err)

		fx := fxf(4)

		ph1 := fx.NextProposedHeader([]byte("app_data_1"), 0)
		ph1.Header.PrevAppStateHash = []byte("initial_app_state") // TODO: this should be automatically set in the fixture.

		// This tends to be nil from the store.
		// Ensure it is nil here too.
		require.Empty(t, ph1.Header.PrevCommitProof.Proofs)
		ph1.Header.PrevCommitProof.Proofs = nil

		fx.RecalculateHash(&ph1.Header)
		fx.SignProposal(ctx, &ph1, 0)

		voteMap := map[string][]int{
			string(ph1.Header.Hash): {0, 1, 2},
			"":                      {3},
		}
		precommitProofs1 := fx.PrecommitProofMap(ctx, 1, 0, voteMap)
		fx.CommitBlock(ph1.Header, []byte("app_state_height_1"), 0, precommitProofs1)

		ph2 := fx.NextProposedHeader([]byte("app_data_2"), 0)
		ph2.Round = 1

		ch := tmconsensus.CommittedHeader{
			Header: ph1.Header,
			Proof:  ph2.Header.PrevCommitProof,
		}

		require.NoError(t, s.SaveCommittedHeader(ctx, ch))

		got, err := s.LoadCommittedHeader(ctx, 1)
		require.NoError(t, err)

		require.Equal(t, ch, got)

		voteMap = map[string][]int{
			string(ph2.Header.Hash): {0, 1, 3},
			"":                      {2},
		}
		precommitProofs2 := fx.PrecommitProofMap(ctx, 2, 1, voteMap)
		fx.CommitBlock(ph2.Header, []byte("app_state_height_2"), 1, precommitProofs2)

		ph3 := fx.NextProposedHeader([]byte("app_data_3"), 0)

		ch = tmconsensus.CommittedHeader{
			Header: ph2.Header,
			Proof:  ph3.Header.PrevCommitProof,
		}

		require.NoError(t, s.SaveCommittedHeader(ctx, ch))

		got, err = s.LoadCommittedHeader(ctx, 2)
		require.NoError(t, err)

		require.Equal(t, ch, got)
	})

	t.Run("HeightUnknownError when height not found", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		s, err := f(t.Cleanup)
		require.NoError(t, err)

		_, err = s.LoadCommittedHeader(ctx, 1)
		require.Error(t, err)
		require.ErrorIs(t, err, tmconsensus.HeightUnknownError{Want: 1})

		_, err = s.LoadCommittedHeader(ctx, 5)
		require.Error(t, err)
		require.ErrorIs(t, err, tmconsensus.HeightUnknownError{Want: 5})
	})
}
