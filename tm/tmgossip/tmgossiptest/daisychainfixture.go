package tmgossiptest

import (
	"context"

	"github.com/gordian-engine/gordian/tm/tmconsensus/tmconsensustest"
	"github.com/gordian-engine/gordian/tm/tmengine/tmelink"
)

// DaisyChainFixture contains a DaisyChainNetwork,
// update channels to provide inputs
// and mock consensus handlers to confirm outputs.
type DaisyChainFixture struct {
	Network *DaisyChainNetwork

	Handlers []*tmconsensustest.ChannelConsensusHandler

	UpdateChs []chan<- tmelink.NetworkViewUpdate
}

// NewDaisyChainFixture returns a fixture with a network,
// mock consensus handlers, and input update channels.
func NewDaisyChainFixture(ctx context.Context, nStrats int) *DaisyChainFixture {
	n := NewDaisyChainNetwork(ctx, nStrats)

	handlers := make([]*tmconsensustest.ChannelConsensusHandler, nStrats)
	for i := range nStrats {
		h := tmconsensustest.NewChannelConsensusHandler(4)
		handlers[i] = h
		n.Strategies[i].SetConsensusHandler(h)
	}

	updateChs := make([]chan<- tmelink.NetworkViewUpdate, nStrats)
	for i := range updateChs {
		ch := make(chan tmelink.NetworkViewUpdate)
		updateChs[i] = ch
		n.Strategies[i].Start(ch)
	}

	return &DaisyChainFixture{
		Network: n,

		Handlers: handlers,

		UpdateChs: updateChs,
	}
}

// Wait blocks until all background work in the fixture has completed.
func (f *DaisyChainFixture) Wait() {
	f.Network.Wait()
}
