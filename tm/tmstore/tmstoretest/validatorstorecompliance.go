package tmstoretest

import (
	"context"
	"testing"

	"github.com/gordian-engine/gordian/tm/tmconsensus"
	"github.com/gordian-engine/gordian/tm/tmstore"
	"github.com/stretchr/testify/require"
)

type ValidatorStoreFactory func(cleanup func(func())) (tmstore.ValidatorStore, error)

func TestValidatorStoreCompliance(t *testing.T, f ValidatorStoreFactory, fxf FixtureFactory) {
	t.Run("pub keys", func(t *testing.T) {
		t.Run("round trip", func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			s, err := f(t.Cleanup)
			require.NoError(t, err)

			keys3 := fxf(3).PrivVals.PubKeys()

			hash3, err := s.SavePubKeys(ctx, keys3)
			require.NoError(t, err)

			got3, err := s.LoadPubKeys(ctx, hash3)
			require.NoError(t, err)
			require.Len(t, got3, 3)
			for i, got := range got3 {
				require.Truef(t, keys3[i].Equal(got), "key mismatch at index %d", i)
			}

			// And expected error is returned on re-save.
			repeat, err := s.SavePubKeys(ctx, keys3)
			require.Error(t, err)
			require.ErrorIs(t, tmstore.PubKeysAlreadyExistError{
				ExistingHash: hash3,
			}, err)
			require.Equal(t, hash3, repeat)
		})

		t.Run("no entry for hash", func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			sGood, err := f(t.Cleanup)
			require.NoError(t, err)

			keys3 := fxf(3).PrivVals.PubKeys()

			hash3, err := sGood.SavePubKeys(ctx, keys3)
			require.NoError(t, err)

			s, err := f(t.Cleanup)
			require.NoError(t, err)

			_, err = s.LoadPubKeys(ctx, hash3)
			require.Error(t, err)
			require.ErrorIs(t, err, tmstore.NoPubKeyHashError{Want: hash3})
		})
	})

	t.Run("vote powers", func(t *testing.T) {
		t.Run("round trip", func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			s, err := f(t.Cleanup)
			require.NoError(t, err)

			pows3 := []uint64{1000, 10, 1}

			hash3, err := s.SaveVotePowers(ctx, pows3)
			require.NoError(t, err)

			got3, err := s.LoadVotePowers(ctx, hash3)
			require.NoError(t, err)
			require.Equal(t, pows3, got3)

			// And expected error is returned on re-save.
			repeat, err := s.SaveVotePowers(ctx, pows3)
			require.ErrorIs(t, tmstore.VotePowersAlreadyExistError{
				ExistingHash: hash3,
			}, err)
			require.Equal(t, hash3, repeat)
		})

		t.Run("no entry for hash", func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			sGood, err := f(t.Cleanup)
			require.NoError(t, err)

			pows3 := []uint64{1000, 10, 1}

			hash3, err := sGood.SaveVotePowers(ctx, pows3)
			require.NoError(t, err)

			s, err := f(t.Cleanup)
			require.NoError(t, err)

			_, err = s.LoadVotePowers(ctx, hash3)
			require.ErrorIs(t, err, tmstore.NoVotePowerHashError{Want: hash3})
		})
	})

	t.Run("load validators", func(t *testing.T) {
		t.Run("happy path", func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			s, err := f(t.Cleanup)
			require.NoError(t, err)

			vals3 := fxf(3).PrivVals

			keyHash, err := s.SavePubKeys(ctx, vals3.PubKeys())
			require.NoError(t, err)

			pows := make([]uint64, len(vals3))
			for i, v := range vals3 {
				pows[i] = v.Val.Power
			}

			powHash, err := s.SaveVotePowers(ctx, pows)
			require.NoError(t, err)

			gotVals, err := s.LoadValidators(ctx, keyHash, powHash)
			require.NoError(t, err)

			require.Truef(
				t,
				tmconsensus.ValidatorSlicesEqual(vals3.Vals(), gotVals),
				"validator slices differed:\n%#v\n!=\n%#v", vals3.Vals(), gotVals,
			)
		})

		t.Run("missing keys", func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			sGood, err := f(t.Cleanup)
			require.NoError(t, err)

			vals3 := fxf(3).PrivVals

			keyHash, err := sGood.SavePubKeys(ctx, vals3.PubKeys())
			require.NoError(t, err)

			pows := make([]uint64, len(vals3))
			for i, v := range vals3 {
				pows[i] = v.Val.Power
			}

			powHash, err := sGood.SaveVotePowers(ctx, pows)
			require.NoError(t, err)

			s, err := f(t.Cleanup)
			require.NoError(t, err)

			gotPowHash, err := s.SaveVotePowers(ctx, pows)
			require.NoError(t, err)

			require.Equal(t, powHash, gotPowHash)

			_, err = s.LoadValidators(ctx, keyHash, powHash)
			require.Error(t, err)
			require.ErrorIs(t, err, tmstore.NoPubKeyHashError{Want: keyHash})
			require.NotErrorIs(t, err, tmstore.NoVotePowerHashError{Want: powHash})
		})

		t.Run("missing vote powers", func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			sGood, err := f(t.Cleanup)
			require.NoError(t, err)

			vals3 := fxf(3).PrivVals

			keyHash, err := sGood.SavePubKeys(ctx, vals3.PubKeys())
			require.NoError(t, err)

			pows := make([]uint64, len(vals3))
			for i, v := range vals3 {
				pows[i] = v.Val.Power
			}

			powHash, err := sGood.SaveVotePowers(ctx, pows)
			require.NoError(t, err)

			s, err := f(t.Cleanup)
			require.NoError(t, err)

			gotKeyHash, err := s.SavePubKeys(ctx, vals3.PubKeys())
			require.NoError(t, err)
			require.Equal(t, keyHash, gotKeyHash)

			_, err = s.LoadValidators(ctx, keyHash, powHash)
			require.Error(t, err)
			require.ErrorIs(t, err, tmstore.NoVotePowerHashError{Want: powHash})
			require.NotErrorIs(t, err, tmstore.NoPubKeyHashError{Want: keyHash})
		})

		t.Run("missing both", func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			sGood, err := f(t.Cleanup)
			require.NoError(t, err)

			vals3 := fxf(3).PrivVals

			keyHash, err := sGood.SavePubKeys(ctx, vals3.PubKeys())
			require.NoError(t, err)

			pows := make([]uint64, len(vals3))
			for i, v := range vals3 {
				pows[i] = v.Val.Power
			}

			powHash, err := sGood.SaveVotePowers(ctx, pows)
			require.NoError(t, err)

			s, err := f(t.Cleanup)
			require.NoError(t, err)

			_, err = s.LoadValidators(ctx, keyHash, powHash)
			require.Error(t, err)
			require.ErrorIs(t, err, tmstore.NoPubKeyHashError{Want: keyHash})
			require.ErrorIs(t, err, tmstore.NoVotePowerHashError{Want: powHash})
		})

		t.Run("validator and vote power length mismatch", func(t *testing.T) {
			t.Run("more validators than vote powers", func(t *testing.T) {
				t.Parallel()

				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()

				s, err := f(t.Cleanup)
				require.NoError(t, err)

				vals := fxf(4).PrivVals

				keyHash, err := s.SavePubKeys(ctx, vals.PubKeys())
				require.NoError(t, err)

				pows := make([]uint64, len(vals)-1)
				for i, v := range vals[:3] {
					pows[i] = v.Val.Power
				}

				powHash, err := s.SaveVotePowers(ctx, pows)
				require.NoError(t, err)

				_, err = s.LoadValidators(ctx, keyHash, powHash)
				require.Error(t, err)
				require.ErrorIs(t, err, tmstore.PubKeyPowerCountMismatchError{
					NPubKeys:   4,
					NVotePower: 3,
				})
			})

			t.Run("more vote powers than validators", func(t *testing.T) {
				t.Parallel()

				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()

				s, err := f(t.Cleanup)
				require.NoError(t, err)

				vals := fxf(4).PrivVals

				keyHash, err := s.SavePubKeys(ctx, vals[:3].PubKeys())
				require.NoError(t, err)

				pows := make([]uint64, len(vals))
				for i, v := range vals {
					pows[i] = v.Val.Power
				}

				powHash, err := s.SaveVotePowers(ctx, pows)
				require.NoError(t, err)

				_, err = s.LoadValidators(ctx, keyHash, powHash)
				require.Error(t, err)
				require.ErrorIs(t, err, tmstore.PubKeyPowerCountMismatchError{
					NPubKeys:   3,
					NVotePower: 4,
				})
			})
		})
	})
}
