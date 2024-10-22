package tmjson_test

import (
	"testing"

	"github.com/gordian-engine/gordian/gcrypto"
	"github.com/gordian-engine/gordian/tm/tmcodec"
	"github.com/gordian-engine/gordian/tm/tmcodec/tmcodectest"
	"github.com/gordian-engine/gordian/tm/tmcodec/tmjson"
)

func TestMarshalCodec(t *testing.T) {
	tmcodectest.TestMarshalCodecCompliance(t, func() tmcodec.MarshalCodec {
		reg := new(gcrypto.Registry)
		gcrypto.RegisterEd25519(reg)
		return tmjson.MarshalCodec{
			CryptoRegistry: reg,
		}
	})
}
