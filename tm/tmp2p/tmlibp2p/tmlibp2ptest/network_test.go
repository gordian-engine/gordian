package tmlibp2ptest_test

import (
	"context"
	"testing"

	"github.com/gordian-engine/gordian/gcrypto"
	"github.com/gordian-engine/gordian/internal/gtest"
	"github.com/gordian-engine/gordian/tm/tmcodec/tmjson"
	"github.com/gordian-engine/gordian/tm/tmp2p/tmlibp2p"
	"github.com/gordian-engine/gordian/tm/tmp2p/tmlibp2p/tmlibp2ptest"
	"github.com/gordian-engine/gordian/tm/tmp2p/tmp2ptest"
)

func TestLibp2pNetwork_Compliance(t *testing.T) {
	tmp2ptest.TestNetworkCompliance(
		t,
		func(t *testing.T, ctx context.Context) (tmp2ptest.Network, error) {
			reg := new(gcrypto.Registry)
			gcrypto.RegisterEd25519(reg)
			codec := tmjson.MarshalCodec{
				CryptoRegistry: reg,
			}
			n, err := tmlibp2ptest.NewNetwork(ctx, gtest.NewLogger(t), codec)
			if err != nil {
				return nil, err
			}
			return &tmp2ptest.GenericNetwork[*tmlibp2p.Connection]{
				Network: n,
			}, nil
		},
	)
}
