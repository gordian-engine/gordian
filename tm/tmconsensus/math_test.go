package tmconsensus_test

import (
	"math"
	"testing"

	"github.com/gordian-engine/gordian/tm/tmconsensus"
	"github.com/stretchr/testify/require"
)

func TestByzantineMajority(t *testing.T) {
	for _, tc := range []struct {
		n    uint64
		want uint64
	}{
		// Easily verified values.
		{n: 12, want: 9},
		{n: 10, want: 7},
		{n: 100, want: 67},

		// Three numbers in sequence to ensure modulo-3 cases.
		{n: 3, want: 3},
		{n: 4, want: 3},
		{n: 5, want: 4},

		// Max uint64 to ensure no overflow condition.
		{n: math.MaxUint64, want: ((math.MaxUint64 / 3) * 2) + 1},
	} {
		require.Equal(t, tc.want, tmconsensus.ByzantineMajority(tc.n))
	}

	require.Panics(t, func() {
		_ = tmconsensus.ByzantineMajority(0)
	})
}

func TestByzantineMinority(t *testing.T) {
	for _, tc := range []struct {
		n    uint64
		want uint64
	}{
		// Easy values, complementary to the ByzantineMajority tests.
		{n: 12, want: 4},
		{n: 10, want: 4},
		{n: 100, want: 34},

		// Three numbers in sequence to ensure modulo-3 cases.
		{n: 3, want: 1},
		{n: 4, want: 2},
		{n: 5, want: 2},

		// Max uint64 to ensure no overflow condition.
		{n: math.MaxUint64, want: (math.MaxUint64 / 3)},
	} {
		require.Equal(t, tc.want, tmconsensus.ByzantineMinority(tc.n))
	}

	require.Panics(t, func() {
		_ = tmconsensus.ByzantineMinority(0)
	})
}
