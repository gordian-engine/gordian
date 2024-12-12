package gereedsolomon_test

import (
	"testing"

	"github.com/gordian-engine/gordian/gerasure"
	"github.com/gordian-engine/gordian/gerasure/gerasuretest"
	"github.com/gordian-engine/gordian/gerasure/gereedsolomon"
)

func TestReconstructionCompliance(t *testing.T) {
	gerasuretest.TestFixedRateErasureReconstructionCompliance(
		t,
		func(origData []byte, nData, nParity int) (gerasure.Encoder, gerasure.Reconstructor) {
			enc, err := gereedsolomon.NewEncoder(nData, nParity)
			if err != nil {
				panic(err)
			}

			// We don't know the shard size until we encode.
			// (Or at least I don't see how to get that from the reedsolomon package.)
			allShards, err := enc.Encode(nil, origData)
			if err != nil {
				panic(err)
			}
			shardSize := len(allShards[0])

			// Separate reedsolomon encoder for the reconstructor.
			rcons, err := gereedsolomon.NewReconstructor(nData, nParity, shardSize)
			if err != nil {
				panic(err)
			}

			return enc, rcons
		},
	)
}
