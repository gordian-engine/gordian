package tmgossiptest

import "context"

// DaisyChainNetwork is a collection of [DaisyChainStrategy] instances.
//
// Depending on the use case, you may either create the network directly
// with [NewDaisyChainNetwork], or you may use [NewDaisyChainFixture]
// to also associate the strategy instances with
// consensus handlers and update channels.
type DaisyChainNetwork struct {
	Strategies []*DaisyChainStrategy
}

// NewDaisyChainNetwork returns a network that contains a sequence of
// [DaisyChainStrategy] instances.
func NewDaisyChainNetwork(ctx context.Context, nStrats int) *DaisyChainNetwork {
	strats := make([]*DaisyChainStrategy, nStrats)

	for i := range strats {
		var left *DaisyChainStrategy
		if i > 0 {
			left = strats[i-1]
		}
		strats[i] = NewDaisyChainStrategy(ctx, left)
	}

	return &DaisyChainNetwork{
		Strategies: strats,
	}
}

// Wait blocks until all background work in the network has completed.
func (n *DaisyChainNetwork) Wait() {
	for _, s := range n.Strategies {
		s.Wait()
	}
}
