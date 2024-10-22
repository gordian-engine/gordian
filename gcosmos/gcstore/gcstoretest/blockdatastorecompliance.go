package gcstoretest

import (
	"context"
	"testing"

	"github.com/gordian-engine/gordian/gcosmos/gcstore"
	"github.com/stretchr/testify/require"
)

type BlockDataStoreFactory func() gcstore.BlockDataStore

func TestBlockDataStoreCompliance(t *testing.T, bdsf BlockDataStoreFactory) {
	ctx := context.Background()

	t.Run("successful loading", func(t *testing.T) {
		// Assuming all these subtests are safe to run in parallel.
		// If a store comes along that violates this assumption,
		// we can adjust the outer signature to conditionally indicate parallelism.
		t.Parallel()

		s := bdsf()

		data := []byte("hello")
		const id = "greeting"

		require.NoError(t, s.SaveBlockData(ctx, 1, id, data))

		t.Run("load by height", func(t *testing.T) {
			gotID, gotData, err := s.LoadBlockDataByHeight(ctx, 1, nil)
			require.NoError(t, err)
			require.Equal(t, id, gotID)
			require.Equal(t, data, gotData)
		})

		t.Run("load by id", func(t *testing.T) {
			gotHeight, gotData, err := s.LoadBlockDataByID(ctx, id, nil)
			require.NoError(t, err)
			require.Equal(t, uint64(1), gotHeight)
			require.Equal(t, data, gotData)
		})

		t.Run("loads append to dst argument", func(t *testing.T) {
			t.Run("by height", func(t *testing.T) {
				dst := make([]byte, len(data)+1)
				for i := range dst {
					dst[i] = '!'
				}

				_, gotData, err := s.LoadBlockDataByHeight(ctx, 1, dst[:0])
				require.NoError(t, err)
				require.Equal(t, data, gotData)
				require.Equal(t, "hello!", string(dst))
			})

			t.Run("by ID", func(t *testing.T) {
				dst := make([]byte, len(data)+1)
				for i := range dst {
					dst[i] = '!'
				}

				_, gotData, err := s.LoadBlockDataByID(ctx, id, dst[:0])
				require.NoError(t, err)
				require.Equal(t, data, gotData)
				require.Equal(t, "hello!", string(dst))
			})
		})

		t.Run("saved data is independent of original", func(t *testing.T) {
			data[0] = 'j'

			_, gotData, err := s.LoadBlockDataByHeight(ctx, 1, nil)
			require.NoError(t, err)
			require.Equal(t, []byte("hello"), gotData)

			_, gotData, err = s.LoadBlockDataByID(ctx, id, nil)
			require.NoError(t, err)
			require.Equal(t, []byte("hello"), gotData)
		})
	})

	t.Run("failed loads", func(t *testing.T) {
		t.Parallel()

		s := bdsf()

		t.Run("by height", func(t *testing.T) {
			_, _, err := s.LoadBlockDataByHeight(ctx, 1, nil)
			require.ErrorIs(t, err, gcstore.ErrBlockDataNotFound)
		})

		t.Run("by id", func(t *testing.T) {
			_, _, err := s.LoadBlockDataByID(ctx, "foo", nil)
			require.ErrorIs(t, err, gcstore.ErrBlockDataNotFound)
		})
	})

	t.Run("failed saves", func(t *testing.T) {
		t.Run("duplicate height", func(t *testing.T) {
			t.Parallel()

			s := bdsf()

			require.NoError(t, s.SaveBlockData(ctx, 1, "id", []byte("data")))

			err := s.SaveBlockData(ctx, 1, "other_id", []byte("other_data"))
			require.ErrorIs(t, err, gcstore.AlreadyHaveBlockDataForHeightError{Height: 1})
		})

		t.Run("duplicate ID", func(t *testing.T) {
			t.Parallel()

			s := bdsf()

			require.NoError(t, s.SaveBlockData(ctx, 1, "id", []byte("data")))

			err := s.SaveBlockData(ctx, 2, "id", []byte("other_data"))
			require.ErrorIs(t, err, gcstore.AlreadyHaveBlockDataForIDError{ID: "id"})
		})
	})
}
