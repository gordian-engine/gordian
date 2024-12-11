package gereedsolomon_test

import (
	"testing"

	"github.com/gordian-engine/gordian/gerasure"
	"github.com/gordian-engine/gordian/gerasure/gerasuretest"
	"github.com/gordian-engine/gordian/gerasure/gereedsolomon"
	"github.com/klauspost/reedsolomon"
)

func TestReconstructionCompliance(t *testing.T) {
	gerasuretest.TestFixedRateErasureReconstructionCompliance(
		t,
		func(origData []byte, nData, nParity int) (gerasure.Encoder, gerasure.Reconstructor) {
			rs, err := reedsolomon.New(nData, nParity)
			if err != nil {
				panic(err)
			}

			// We don't know the shard size until we encode.
			// (Or at least I don't see how to get that from the reedsolomon package.)
			allShards, err := rs.Split(origData)
			if err != nil {
				panic(err)
			}
			shardSize := len(allShards[0])

			enc := gereedsolomon.NewEncoder(rs)

			// Separate reedsolomon encoder for the reconstructor.
			rrs, err := reedsolomon.New(nData, nParity)
			if err != nil {
				panic(err)
			}

			r := gereedsolomon.NewReconstructor(rrs, shardSize)

			return enc, r
		},
	)
}
