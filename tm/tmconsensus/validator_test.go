package tmconsensus_test

import (
	"testing"

	"github.com/gordian-engine/gordian/tm/tmconsensus"
	"github.com/gordian-engine/gordian/tm/tmconsensus/tmconsensustest"
	"github.com/stretchr/testify/require"
)

func TestValidatorSlicesEqual(t *testing.T) {
	t.Parallel()

	t.Run("equivalent slices are equal", func(t *testing.T) {
		fx1 := tmconsensustest.NewEd25519Fixture(2)
		fx2 := tmconsensustest.NewEd25519Fixture(2)

		require.True(t, tmconsensus.ValidatorSlicesEqual(fx1.Vals(), fx2.Vals()))
	})

	t.Run("equivalent items but different order are not equal", func(t *testing.T) {
		fx1 := tmconsensustest.NewEd25519Fixture(2)
		fx2 := tmconsensustest.NewEd25519Fixture(2)
		vals2 := fx2.Vals()
		vals2[0], vals2[1] = vals2[1], vals2[0]

		require.False(t, tmconsensus.ValidatorSlicesEqual(fx1.Vals(), vals2))
	})

	t.Run("different lengths are not equal", func(t *testing.T) {
		fx1 := tmconsensustest.NewEd25519Fixture(2)
		fx2 := tmconsensustest.NewEd25519Fixture(3)

		require.False(t, tmconsensus.ValidatorSlicesEqual(fx1.Vals(), fx2.Vals()))
		require.False(t, tmconsensus.ValidatorSlicesEqual(fx2.Vals(), fx1.Vals()))
	})

	t.Run("same length with different entries are not equal", func(t *testing.T) {
		fx1 := tmconsensustest.NewEd25519Fixture(2)
		fx2 := tmconsensustest.NewEd25519Fixture(3)

		vals1 := fx1.Vals()
		vals2 := fx2.Vals()[1:]

		require.False(t, tmconsensus.ValidatorSlicesEqual(vals1, vals2))
		require.False(t, tmconsensus.ValidatorSlicesEqual(vals2, vals1))
	})
}

func TestCanTrustValidators(t *testing.T) {
	t.Parallel()

	t.Run("equal set", func(t *testing.T) {
		fx := tmconsensustest.NewEd25519Fixture(4)

		vals := fx.Vals()

		pubKeys := tmconsensus.ValidatorsToPubKeys(vals)

		require.True(t, tmconsensus.CanTrustValidators(vals, pubKeys))
	})

	t.Run("one new validator, still majority voting power", func(t *testing.T) {
		fx := tmconsensustest.NewEd25519Fixture(4)
		pubKeys := tmconsensus.ValidatorsToPubKeys(fx.Vals())

		newVals := tmconsensustest.NewEd25519Fixture(5).Vals()

		require.True(t, tmconsensus.CanTrustValidators(newVals, pubKeys))
	})

	t.Run("exactly one third trustable, reports false", func(t *testing.T) {
		fx := tmconsensustest.NewEd25519Fixture(1)
		pubKeys := tmconsensus.ValidatorsToPubKeys(fx.Vals())

		newVals := tmconsensustest.NewEd25519Fixture(3).Vals()

		require.False(t, tmconsensus.CanTrustValidators(newVals, pubKeys))
	})

	t.Run("no overlap, reports false", func(t *testing.T) {
		fx := tmconsensustest.NewEd25519Fixture(1)
		pubKeys := tmconsensus.ValidatorsToPubKeys(fx.Vals())

		newVals := tmconsensustest.NewEd25519Fixture(4).Vals()[1:]

		require.False(t, tmconsensus.CanTrustValidators(newVals, pubKeys))
	})

	t.Run("validator set shrinks", func(t *testing.T) {
		fx := tmconsensustest.NewEd25519Fixture(5)
		pubKeys := tmconsensus.ValidatorsToPubKeys(fx.Vals())

		newVals := tmconsensustest.NewEd25519Fixture(2).Vals()

		require.True(t, tmconsensus.CanTrustValidators(newVals, pubKeys))
	})
}
