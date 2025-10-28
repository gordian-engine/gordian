package tmlibp2pintegration

import (
	"context"
	"fmt"
	"testing"

	"github.com/gordian-engine/gordian/internal/gtest"
	"github.com/gordian-engine/gordian/tm/tmcodec/tmjson"
	"github.com/gordian-engine/gordian/tm/tmconsensus/tmconsensustest"
	"github.com/gordian-engine/gordian/tm/tmgossip"
	"github.com/gordian-engine/gordian/tm/tmintegration"
	"github.com/gordian-engine/gordian/tm/tmp2p"
	"github.com/gordian-engine/gordian/tm/tmp2p/tmlibp2p"
	"github.com/gordian-engine/gordian/tm/tmp2p/tmlibp2p/tmlibp2ptest"
	"github.com/gordian-engine/gordian/tm/tmp2p/tmp2ptest"
)

// Libp2pFactory provides a Network and GossipStrategy for integration tests.
// This makes it straightforward to compose separate stores and schemes for integration tests.
type Libp2pFactory struct {
	e *tmintegration.Env
}

func NewLibp2pFactory(e *tmintegration.Env) Libp2pFactory {
	return Libp2pFactory{e: e}
}

func (f Libp2pFactory) NewNetwork(t *testing.T, ctx context.Context, nVals int) (
	tmp2ptest.Network, *tmconsensustest.Fixture, error,
) {
	fx := tmconsensustest.NewEd25519Fixture(nVals)
	codec := tmjson.MarshalCodec{
		CryptoRegistry: &fx.Registry,
	}
	n, err := tmlibp2ptest.NewNetwork(ctx, gtest.NewLogger(t), codec)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to build network: %w", err)
	}

	return &tmp2ptest.GenericNetwork[*tmlibp2p.Connection]{
		Network: n,
	}, fx, nil
}

func (f Libp2pFactory) NewGossipStrategy(ctx context.Context, idx int, conn tmp2p.Connection) (tmgossip.Strategy, error) {
	log := f.e.RootLogger.With("sys", "chattygossip", "idx", idx)
	return tmgossip.NewChattyStrategy(ctx, log, conn.ConsensusBroadcaster()), nil
}
