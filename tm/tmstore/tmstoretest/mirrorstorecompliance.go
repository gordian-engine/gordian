package tmstoretest

import (
	"context"
	"testing"

	"github.com/gordian-engine/gordian/tm/tmstore"
	"github.com/stretchr/testify/require"
)

type MirrorStoreFactory func(cleanup func(func())) (tmstore.MirrorStore, error)

// TestMirrorStoreCompliance does not require a FixtureFactory
// because there is no representation of validators or signatures in the mirror store.
func TestMirrorStoreCompliance(t *testing.T, f MirrorStoreFactory) {
	t.Run("returns ErrStoreUninitialized before first update", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		s, err := f(t.Cleanup)
		require.NoError(t, err)

		_, _, _, _, err = s.NetworkHeightRound(ctx)
		require.Error(t, err)
		require.ErrorIs(t, err, tmstore.ErrStoreUninitialized)
	})

	t.Run("returns stored value", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		s, err := f(t.Cleanup)
		require.NoError(t, err)

		// Voting on 1/0 first.
		require.NoError(t, s.SetNetworkHeightRound(ctx, 1, 0, 0, 0))
		vh, vr, ch, cr, err := s.NetworkHeightRound(ctx)
		require.NoError(t, err)
		require.Equal(t, uint64(1), vh)
		require.Zero(t, vr)
		require.Zero(t, ch)
		require.Zero(t, cr)

		// Then voting on 1/1.
		require.NoError(t, s.SetNetworkHeightRound(ctx, 1, 1, 0, 0))
		vh, vr, ch, cr, err = s.NetworkHeightRound(ctx)
		require.NoError(t, err)
		require.Equal(t, uint64(1), vh)
		require.Equal(t, uint32(1), vr)
		require.Zero(t, ch)
		require.Zero(t, cr)

		// Now voting on 2/0, committing 1/1.
		require.NoError(t, s.SetNetworkHeightRound(ctx, 2, 0, 1, 1))
		vh, vr, ch, cr, err = s.NetworkHeightRound(ctx)
		require.NoError(t, err)
		require.Equal(t, uint64(2), vh)
		require.Zero(t, vr)
		require.Equal(t, uint64(1), ch)
		require.Equal(t, uint32(1), cr)
	})
}
