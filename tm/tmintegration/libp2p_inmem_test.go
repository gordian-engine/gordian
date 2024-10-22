package tmintegration_test

import (
	"context"
	"log/slog"
	"testing"

	"github.com/gordian-engine/gordian/gcrypto"
	"github.com/gordian-engine/gordian/tm/tmcodec/tmjson"
	"github.com/gordian-engine/gordian/tm/tmgossip"
	"github.com/gordian-engine/gordian/tm/tmintegration"
	"github.com/gordian-engine/gordian/tm/tmp2p"
	"github.com/gordian-engine/gordian/tm/tmp2p/tmlibp2p"
	"github.com/gordian-engine/gordian/tm/tmp2p/tmlibp2p/tmlibp2ptest"
	"github.com/gordian-engine/gordian/tm/tmp2p/tmp2ptest"
)

type Libp2pInmemFactory struct {
	e *tmintegration.Env

	tmintegration.InmemStoreFactory
	tmintegration.InmemSchemeFactory
}

func (f Libp2pInmemFactory) NewNetwork(ctx context.Context, log *slog.Logger) (tmp2ptest.Network, error) {
	reg := new(gcrypto.Registry)
	gcrypto.RegisterEd25519(reg)

	codec := tmjson.MarshalCodec{
		CryptoRegistry: reg,
	}
	n, err := tmlibp2ptest.NewNetwork(ctx, log, codec)
	if err != nil {
		return nil, err
	}

	return &tmp2ptest.GenericNetwork[*tmlibp2p.Connection]{
		Network: n,
	}, nil
}

func (f Libp2pInmemFactory) NewGossipStrategy(ctx context.Context, idx int, conn tmp2p.Connection) (tmgossip.Strategy, error) {
	return tmgossip.NewChattyStrategy(ctx, f.e.RootLogger.With("sys", "chattygossip", "idx", idx), conn.ConsensusBroadcaster()), nil
}

func TestLibp2pInmem(t *testing.T) {
	tmintegration.RunIntegrationTest(t, func(e *tmintegration.Env) tmintegration.Factory {
		return Libp2pInmemFactory{e: e}
	})
}
