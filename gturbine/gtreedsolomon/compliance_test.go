package gtreedsolomon_test

import (
	"testing"

	"github.com/gordian-engine/gordian/gturbine"
	"github.com/gordian-engine/gordian/gturbine/gtreedsolomon"
	"github.com/gordian-engine/gordian/gturbine/gturbinetest"
	"github.com/klauspost/reedsolomon"
)

func TestGturbineCompliance(t *testing.T) {
	gturbinetest.TestGturbineReconstructionCompliance(
		t,
		func(origData []byte, nData, nParity int) (gturbine.Encoder, gturbine.Reconstructor) {
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

			enc := gtreedsolomon.NewEncoder(rs)

			// Separate reedsolomon encoder for the reconstructor.
			rrs, err := reedsolomon.New(nData, nParity)
			if err != nil {
				panic(err)
			}

			r := gtreedsolomon.NewReconstructor(rrs, shardSize)

			return enc, r
		},
	)
}
