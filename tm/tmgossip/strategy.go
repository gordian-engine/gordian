package tmgossip

import (
	"github.com/gordian-engine/gordian/tm/tmconsensus"
	"github.com/gordian-engine/gordian/tm/tmengine/tmelink"
)

// Strategy is a gossip strategy, whose purpose is to observe changes to round state
// and send messages to the p2p network.
// Therefore, when a Strategy is initialized, it must internally understand
// how to broadcast to peers on the network.
//
// The outer interface is simple.
// The engine provides the strategy with a consensus handler
// (in production, the [github.com/gordian-engine/gordian/tm/tmengine.Engine] itself)
// and read-only channel of tmelink.NetworkViewUpdate
// to provide round state updates as they are discovered.
// When the engine is shutting down it will call the strategy's Wait method.
type Strategy interface {
	// Start provides the consensus handler to handle the business logic of each received message,
	// and a channel of NetworkViewUpdate for the strategy to consume.
	// It is incorrect to call Start more than once.
	Start(handler tmconsensus.FineGrainedConsensusHandler, updates <-chan tmelink.NetworkViewUpdate)

	// Wait blocks until the strategy is finished.
	// The engine calls this method when the engine itself is shutting down.
	Wait()
}
