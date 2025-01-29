package tmstoretest

import (
	"context"
	"testing"

	"github.com/gordian-engine/gordian/tm/tmconsensus"
	"github.com/gordian-engine/gordian/tm/tmstore"
	"github.com/stretchr/testify/require"
)

type FinalizationStoreFactory func(cleanup func(func())) (tmstore.FinalizationStore, error)

func TestFinalizationStoreCompliance(t *testing.T, f FinalizationStoreFactory, fxf FixtureFactory) {
	t.Run("round trip", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		s, err := f(t.Cleanup)
		require.NoError(t, err)

		valSet := fxf(3).ValSet()

		require.NoError(t, s.SaveFinalization(ctx, 1, 3, "my_block_hash", valSet, "my_app_state_hash"))

		round, blockHash, newValSet, appStateHash, err := s.LoadFinalizationByHeight(ctx, 1)
		require.NoError(t, err)
		require.Equal(t, uint32(3), round)
		require.Equal(t, "my_block_hash", blockHash)
		require.True(t, valSet.Equal(newValSet))
		require.Equal(t, "my_app_state_hash", appStateHash)
	})

	t.Run("returns HeightUnknownError when loading unknown height", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		s, err := f(t.Cleanup)
		require.NoError(t, err)

		_, _, _, _, err = s.LoadFinalizationByHeight(ctx, 10)
		require.Error(t, err)
		require.ErrorIs(t, err, tmconsensus.HeightUnknownError{Want: 10})
	})

	t.Run("returns FinalizationOverwriteError on a double save", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		s, err := f(t.Cleanup)
		require.NoError(t, err)

		valSet := fxf(3).ValSet()

		require.NoError(t, s.SaveFinalization(ctx, 1, 3, "my_block_hash", valSet, "my_app_state_hash"))

		// Overwrite error even with exact same values.
		expErr := tmstore.FinalizationOverwriteError{Height: 1}
		require.ErrorIs(t, s.SaveFinalization(ctx, 1, 3, "my_block_hash", valSet, "my_app_state_hash"), expErr)

		// Overwrite error with same round and different hashes.
		require.ErrorIs(t, s.SaveFinalization(ctx, 1, 3, "my_block_hash_2", valSet, "my_app_state_hash_2"), expErr)

		// Overwrite error with different round.
		require.ErrorIs(t, s.SaveFinalization(ctx, 1, 100, "my_block_hash_2", valSet, "my_app_state_hash_2"), expErr)

		// Original values unmodified.
		round, blockHash, newValSet, appStateHash, err := s.LoadFinalizationByHeight(ctx, 1)
		require.NoError(t, err)
		require.Equal(t, uint32(3), round)
		require.Equal(t, "my_block_hash", blockHash)
		require.True(t, valSet.Equal(newValSet))
		require.Equal(t, "my_app_state_hash", appStateHash)
	})
}
