package tmintegration

import (
	"context"
	"log/slog"

	"github.com/gordian-engine/gordian/gcrypto"
	"github.com/gordian-engine/gordian/tm/tmgossip"
	"github.com/gordian-engine/gordian/tm/tmp2p"
	"github.com/gordian-engine/gordian/tm/tmp2p/tmp2ptest"
)

type DaisyChainFactory struct {
	e *Env
}

func NewDaisyChainFactory(e *Env) DaisyChainFactory {
	return DaisyChainFactory{e: e}
}

func (f DaisyChainFactory) NewNetwork(ctx context.Context, log *slog.Logger, reg *gcrypto.Registry) (tmp2ptest.Network, error) {
	// We don't need the gcrypto registry for the daisy chain network,
	// because we only transmit in-memory values,
	// without serializing and deserializing across the network.
	n := tmp2ptest.NewDaisyChainNetwork(ctx, log)

	return &tmp2ptest.GenericNetwork[*tmp2ptest.DaisyChainConnection]{
		Network: n,
	}, nil
}

func (f DaisyChainFactory) NewGossipStrategy(ctx context.Context, idx int, conn tmp2p.Connection) (tmgossip.Strategy, error) {
	log := f.e.RootLogger.With("sys", "chattygossip", "idx", idx)
	return tmgossip.NewChattyStrategy(ctx, log, conn.ConsensusBroadcaster()), nil
}
