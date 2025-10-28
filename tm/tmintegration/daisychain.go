package tmintegration

import (
	"context"
	"testing"

	"github.com/gordian-engine/gordian/tm/tmconsensus/tmconsensustest"
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

func (f DaisyChainFactory) NewNetwork(
	t *testing.T, ctx context.Context, nVals int,
) (tmp2ptest.Network, *tmconsensustest.Fixture, error) {
	fx := tmconsensustest.NewEd25519Fixture(nVals)
	n := tmp2ptest.NewDaisyChainNetwork(t, ctx)

	return &tmp2ptest.GenericNetwork[*tmp2ptest.DaisyChainConnection]{
		Network: n,
	}, fx, nil
}

func (f DaisyChainFactory) NewGossipStrategy(ctx context.Context, idx int, conn tmp2p.Connection) (tmgossip.Strategy, error) {
	log := f.e.RootLogger.With("sys", "chattygossip", "idx", idx)
	return tmgossip.NewChattyStrategy(ctx, log, conn.ConsensusBroadcaster()), nil
}
