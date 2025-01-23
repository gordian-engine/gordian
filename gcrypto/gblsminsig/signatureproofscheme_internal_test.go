package gblsminsig

import (
	"fmt"
	"math/big"
	"testing"

	"github.com/bits-and-blooms/bitset"
	"github.com/stretchr/testify/require"
)

func TestMainCombinationRoundTrip(t *testing.T) {
	tcs := []struct {
		n       int
		setBits func(bs *bitset.BitSet)
	}{
		{
			n: 100,
			setBits: func(bs *bitset.BitSet) {
				for i := range 80 {
					bs.Set(uint(i))
				}
			},
		},
		{
			n: 100,
			setBits: func(bs *bitset.BitSet) {
				for i := range 100 {
					if i%5 == 0 {
						continue
					}
					bs.Set(uint(i))
				}
			},
		},
		{
			n: 500,
			setBits: func(bs *bitset.BitSet) {
				bs.FlipRange(0, 500)
				bs.Clear(100)
				bs.Clear(200)
				bs.Clear(300)
				bs.Clear(400)
			},
		},
	}

	for _, tc := range tcs {
		n := tc.n

		var bs bitset.BitSet
		tc.setBits(&bs)
		k := bs.Count()

		name := fmt.Sprintf("n=%d, k=%d, bs=%x", n, k, bs.Words())
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var combIndex big.Int
			getCombinationIndex(n, &bs, &combIndex)
			t.Logf("combination index: %s", combIndex.String())

			var got bitset.BitSet
			indexToMainCombination(n, int(k), &combIndex, &got)

			// Equal has some ostensibly odd semantics, so dump the string if equality fails.
			require.Truef(t, got.Equal(&bs), "got bitset %s, differed from original bitset %s", got.String(), bs.String())
		})
	}
}
