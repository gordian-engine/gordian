package tmstoretest

import (
	"context"
	"testing"

	"github.com/gordian-engine/gordian/tm/tmstore"
	"github.com/stretchr/testify/require"
)

type StateMachineStoreFactory func(ctx context.Context, cleanup func(func())) (tmstore.StateMachineStore, error)

func TestStateMachineStoreCompliance(t *testing.T, f StateMachineStoreFactory) {
	t.Run("returns ErrStoreUninitialized before first update", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		s, err := f(ctx, t.Cleanup)
		require.NoError(t, err)

		_, _, err = s.StateMachineHeightRound(ctx)
		require.Error(t, err)
		require.ErrorIs(t, err, tmstore.ErrStoreUninitialized)
	})

	t.Run("returns stored value", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		s, err := f(ctx, t.Cleanup)
		require.NoError(t, err)

		require.NoError(t, s.SetStateMachineHeightRound(ctx, 1, 0))

		h, r, err := s.StateMachineHeightRound(ctx)
		require.NoError(t, err)
		require.Equal(t, uint64(1), h)
		require.Zero(t, r)

		require.NoError(t, s.SetStateMachineHeightRound(ctx, 1, 1))

		h, r, err = s.StateMachineHeightRound(ctx)
		require.NoError(t, err)
		require.Equal(t, uint64(1), h)
		require.Equal(t, uint32(1), r)

		require.NoError(t, s.SetStateMachineHeightRound(ctx, 2, 0))

		h, r, err = s.StateMachineHeightRound(ctx)
		require.NoError(t, err)
		require.Equal(t, uint64(2), h)
		require.Zero(t, r)
	})
}
