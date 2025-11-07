package tmintegration_test

import (
	"context"
	"testing"

	"github.com/gordian-engine/gordian/tm/tmconsensus"
	"github.com/gordian-engine/gordian/tm/tmconsensus/tmconsensustest"
	"github.com/gordian-engine/gordian/tm/tmengine/tmelink"
	"github.com/gordian-engine/gordian/tm/tmgossip"
	"github.com/gordian-engine/gordian/tm/tmgossip/tmgossiptest"
	"github.com/gordian-engine/gordian/tm/tmintegration"
)

type DaisyChainInmemFactory struct {
	tmintegration.DaisyChainFactory

	tmintegration.InmemStoreFactory
	tmintegration.InmemSchemeFactory
}

func TestDaisyChainInmem_p2p(t *testing.T) {
	t.Parallel()

	tmintegration.RunIntegrationTest_p2p(t, func(e *tmintegration.Env) tmintegration.Factory {
		return DaisyChainInmemFactory{
			DaisyChainFactory: tmintegration.NewDaisyChainFactory(e),
		}
	})
}

func TestDaisyChainInmem(t *testing.T) {
	t.Parallel()
	tmintegration.RunIntegrationTest(t, func(
		t *testing.T, ctx context.Context, stores []tmintegration.BlockDataStore,
	) (tmintegration.Network, tmintegration.StoreFactory) {
		return dcNet{
			net: tmgossiptest.NewDaisyChainNetwork(ctx, stores),
			fx:  tmconsensustest.NewEd25519Fixture(len(stores)),
		}, tmintegration.InmemStoreNetwork{}
	})
}

// dcNet implements [tmintegration.Network] for [TestDaisyChainInmem].
type dcNet struct {
	net *tmgossiptest.DaisyChainNetwork
	fx  *tmconsensustest.Fixture
}

func (n dcNet) Fixture() *tmconsensustest.Fixture {
	return n.fx
}

func (n dcNet) GetGossipStrategy(_ context.Context, idx int) tmgossip.Strategy {
	return n.net.Strategies[idx]
}

func (n dcNet) SetConsensusHandler(_ context.Context, idx int, h tmconsensus.ConsensusHandler) {
	n.net.Strategies[idx].SetConsensusHandler(h)
}

func (n dcNet) GetProposedHeaderInterceptor(context.Context, int) tmelink.ProposedHeaderInterceptor {
	return nil
}

func (n dcNet) Stabilize(context.Context) {}

func (n dcNet) Wait() {
	n.net.Wait()
}
