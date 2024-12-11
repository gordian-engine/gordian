package gerasuretest

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"math/rand/v2"
	"testing"

	"github.com/gordian-engine/gordian/gerasure"
	"github.com/stretchr/testify/require"
)

// FixedRateFactory is the factory function used for [TestFixedRateErasureReconstructionCompliance].
type FixedRateFactory func(
	origData []byte,
	nDataShards, nParityShards int,
) (gerasure.Encoder, gerasure.Reconstructor)

// TestFixedRateErasureReconstructionCompliance is the compliance test
// for the pairing of [gerasure.Encoder] and [gerasure.Reconstructor],
// when using a fixed-rate erasure coding.
func TestFixedRateErasureReconstructionCompliance(t *testing.T, f FixedRateFactory) {
	t.Helper()

	for _, shardCounts := range [][2]int{
		// Equal counts:
		{4, 4}, {8, 8}, {10, 10}, {16, 16}, {32, 32},

		// Different counts:
		{4, 8}, {8, 4}, {10, 20}, {20, 10},
	} {
		dataCount := shardCounts[0]
		parityCount := shardCounts[1]
		t.Run(fmt.Sprintf("%d data and %d parity shards", shardCounts, shardCounts), func(t *testing.T) {
			for _, dataSize := range []int{
				// Some powers of two:
				1024, 1024 * 4, 1024 * 16, 1024 * 128, 1024 * 1024, 1024 * 1024 * 4, 1024 * 1024 * 8,

				// And some non-powers-of-two:
				300, 1000, 25_000, 100_000, 250_000, 1_000_000, 15_000_000,
			} {
				t.Run(fmt.Sprintf("data size = %d", dataSize), func(t *testing.T) {
					t.Parallel()

					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()

					// Make an RNG based on shard count and data size,
					// so each test case has different source data.
					seed := [32]byte{}
					binary.LittleEndian.PutUint32(seed[:8], uint32(dataCount))
					binary.LittleEndian.PutUint32(seed[8:16], uint32(parityCount))
					binary.LittleEndian.PutUint64(seed[16:], uint64(dataSize))
					chacha := rand.NewChaCha8(seed)

					// Create some original pseudorandom data.
					origData := make([]byte, dataSize)
					_, _ = chacha.Read(origData) // ChaCha8 seeds don't error on Read.

					// Now we can encode.
					enc, r := f(origData, dataCount, parityCount)

					allShards, err := enc.Encode(ctx, origData)
					require.NoError(t, err)

					// Now randomly iterate through the allShards until we can reconstruct.
					rng := rand.New(chacha)
					for _, idx := range rng.Perm(len(allShards)) {
						// Shadow outer error, so we can assert against the error value
						// outside fo this loop.
						err = r.ReconstructData(ctx, idx, allShards[idx])
						if err == nil {
							// Sufficient.
							break
						}

						// Otherwise a non-nil error; we only allow ErrIncompleteSet.
						require.ErrorIs(t, err, gerasure.ErrIncompleteSet)
					}

					// The shadowed inner error must have been nil when we broke the loop.
					require.NoError(t, err)

					// Now reconstruction should succeed.
					t.Run("reconstruct with new allocation", func(t *testing.T) {
						dataCopy, err := r.Data(nil, dataSize)
						require.NoError(t, err)
						require.True(t, bytes.Equal(dataCopy, origData))
					})

					t.Run("reconstruct with presized allocation", func(t *testing.T) {
						backing := make([]byte, dataSize)
						result, err := r.Data(backing[:0], dataSize)
						require.NoError(t, err)
						require.True(t, bytes.Equal(result, origData))
						require.True(t, bytes.Equal(result, backing))
					})
				})
			}
		})
	}
}
