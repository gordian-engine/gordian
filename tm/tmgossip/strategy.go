package tmgossip

import (
	"github.com/gordian-engine/gordian/tm/tmengine/tmelink"
)

// Strategy is a gossip strategy, whose purpose is to observe changes to round state
// and send messages to the p2p network.
// Therefore, when a Strategy is initialized, it should be aware of a [tmp2p.NetworkBroadcaster],
// which should already be available somewhere close to main.go where the strategy is created.
//
// The outer interface is simple.
// The engine provides the strategy with a read-only channel of tmelink.NetworkViewUpdate
// to provide round state updates as they are discovered,
// and when the engine is shutting down it will call the strategy's Wait method.
type Strategy interface {
	// Start provides the channel of NetworkViewUpdate for the strategy to begin running.
	// It is incorrect to call Start more than once.
	Start(updates <-chan tmelink.NetworkViewUpdate)

	// Wait blocks until the strategy is finished.
	// The engine calls this method when the engine itself is shutting down.
	Wait()
}
