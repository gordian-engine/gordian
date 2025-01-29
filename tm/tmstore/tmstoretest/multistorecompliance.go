package tmstoretest

import (
	"context"
	"fmt"
	"testing"

	"github.com/gordian-engine/gordian/tm/tmconsensus"
	"github.com/gordian-engine/gordian/tm/tmstore"
	"github.com/stretchr/testify/require"
)

type MultiStoreFactory[S any] func(cleanup func(func())) (S, error)

// TestMultiStoreCompliance validates inter-store behavior,
// when a single type satisfies multiple store interfaces.
//
// Most of these tests are covering actual discovered bugs with real store implementations.
func TestMultiStoreCompliance[S any](
	t *testing.T,
	f MultiStoreFactory[S],
	fxf FixtureFactory,
) {
	confirmStoreInterfaces[S]()

	// Pairs of stores: organized alphabetically.

	t.Run("ActionStore and CommittedHeaderStore", func(t *testing.T) {
		type store interface {
			tmstore.ActionStore
			tmstore.CommittedHeaderStore
		}
		var s S
		if _, ok := any(s).(store); !ok {
			t.Skipf("implementation %T does not satisfy both ActionStore and CommittedHeaderStore", s)
		}

		t.Run("saving a proposed header action does not appear as a committed header", func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			plain, err := f(t.Cleanup)
			require.NoError(t, err)
			s := any(plain).(store)

			fx := fxf(2)

			ph1 := fx.NextProposedHeader([]byte("app_data_1"), 0)
			fx.SignProposal(ctx, &ph1, 0)

			require.NoError(t, s.SaveProposedHeaderAction(ctx, ph1))

			_, err = s.LoadCommittedHeader(ctx, 1)
			require.Error(t, err)
			require.ErrorIs(t, err, tmconsensus.HeightUnknownError{Want: 1})
		})

		t.Run("saving a proposed header action does not prevent saving a committed header", func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			plain, err := f(t.Cleanup)
			require.NoError(t, err)
			s := any(plain).(store)

			fx := fxf(2)

			ph1 := fx.NextProposedHeader([]byte("app_data_1"), 0)
			require.Empty(t, ph1.Header.PrevCommitProof.Proofs)
			ph1.Header.PrevCommitProof.Proofs = nil
			fx.SignProposal(ctx, &ph1, 0)

			require.NoError(t, s.SaveProposedHeaderAction(ctx, ph1))

			voteMap := map[string][]int{
				string(ph1.Header.Hash): {0, 1},
			}
			precommitProofs1 := fx.PrecommitProofMap(ctx, 1, 0, voteMap)
			fx.CommitBlock(ph1.Header, []byte("app_state_height_1"), 0, precommitProofs1)

			ph2 := fx.NextProposedHeader([]byte("app_data_2"), 0)

			ch := tmconsensus.CommittedHeader{
				Header: ph1.Header,
				Proof:  ph2.Header.PrevCommitProof,
			}

			require.NoError(t, s.SaveCommittedHeader(ctx, ch))

			got, err := s.LoadCommittedHeader(ctx, 1)
			require.NoError(t, err)

			require.Equal(t, ch, got)
		})
	})

	t.Run("CommittedHeaderStore and RoundStore", func(t *testing.T) {
		type store interface {
			tmstore.CommittedHeaderStore
			tmstore.RoundStore
		}
		var s S
		if _, ok := any(s).(store); !ok {
			t.Skipf("implementation %T does not satisfy both CommittedHeaderStore and RoundStore", s)
		}

		t.Run("saving a round proposed header does not appear as a committed header", func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			plain, err := f(t.Cleanup)
			require.NoError(t, err)
			s := any(plain).(store)

			fx := fxf(2)

			ph1 := fx.NextProposedHeader([]byte("app_data_1"), 0)
			fx.SignProposal(ctx, &ph1, 0)

			require.NoError(t, s.SaveRoundProposedHeader(ctx, ph1))

			_, err = s.LoadCommittedHeader(ctx, 1)
			require.Error(t, err)
			require.ErrorIs(t, err, tmconsensus.HeightUnknownError{Want: 1})
		})

		t.Run("saving a round proposed header does not prevent saving a committed header", func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			plain, err := f(t.Cleanup)
			require.NoError(t, err)
			s := any(plain).(store)

			fx := fxf(2)

			ph1 := fx.NextProposedHeader([]byte("app_data_1"), 0)
			require.Empty(t, ph1.Header.PrevCommitProof.Proofs)
			ph1.Header.PrevCommitProof.Proofs = nil
			fx.SignProposal(ctx, &ph1, 0)

			require.NoError(t, s.SaveRoundProposedHeader(ctx, ph1))

			voteMap := map[string][]int{
				string(ph1.Header.Hash): {0, 1},
			}
			precommitProofs1 := fx.PrecommitProofMap(ctx, 1, 0, voteMap)
			fx.CommitBlock(ph1.Header, []byte("app_state_height_1"), 0, precommitProofs1)

			ph2 := fx.NextProposedHeader([]byte("app_data_2"), 0)

			ch := tmconsensus.CommittedHeader{
				Header: ph1.Header,
				Proof:  ph2.Header.PrevCommitProof,
			}

			require.NoError(t, s.SaveCommittedHeader(ctx, ch))

			got, err := s.LoadCommittedHeader(ctx, 1)
			require.NoError(t, err)

			require.Equal(t, ch, got)
		})
	})
}

// confirmStoreInterfaces panics if S satifies less than two store interfaces.
func confirmStoreInterfaces[S any]() {
	var s S
	var n int

	if _, ok := any(s).(tmstore.ActionStore); ok {
		n++
	}
	if _, ok := any(s).(tmstore.CommittedHeaderStore); ok {
		n++
	}
	if _, ok := any(s).(tmstore.FinalizationStore); ok {
		n++
	}
	if _, ok := any(s).(tmstore.MirrorStore); ok {
		n++
	}
	if _, ok := any(s).(tmstore.RoundStore); ok {
		n++
	}
	if _, ok := any(s).(tmstore.StateMachineStore); ok {
		n++
	}
	if _, ok := any(s).(tmstore.ValidatorStore); ok {
		n++
	}

	switch n {
	case 0:
		panic(fmt.Errorf(
			"INVALID: type %T does not satisfy any store interfaces", s,
		))
	case 1:
		panic(fmt.Errorf(
			"INVALID: type %T only satisfies one store interface, but need two or more for TestMultiStoreCompliance", s,
		))
	}
}
