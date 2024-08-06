package tmp2p

import (
	"context"

	"github.com/rollchains/gordian/tm/tmconsensus"
)

// Connection is the generalized connection to the p2p network.
//
// It contains different methods to work with specific layers of the p2p network,
// such as ConsensusBroadcaster to work with only broadcasting consensus messages.
//
// It also enables disconnecting from the p2p network altogether
// (which must invalidate further use of the connection).
// and dynamically changing the underlying [tmconsensus.ConsensusHandler].
type Connection interface {
	// ConsensusBroadcaster returns a ConsensusBroadcaster derived from this connection,
	// or nil if the connection does not support consensus broadcasting.
	ConsensusBroadcaster() ConsensusBroadcaster

	// Set the underlying consensus handler,
	// controlling how to respond to incoming consensus messages from the network.
	// The Connection implementation may have special handling for nil values.
	//
	// This is a method at runtime rather than a parameter to the constructor,
	// because you typically already need a connection before you can create the engine;
	// then once you have a running engine you call conn.SetConsensusHandler(e)
	// so that new messages are validated based on the engine's state.
	SetConsensusHandler(context.Context, tmconsensus.ConsensusHandler)

	// Disconnect the connection, rendering it unusable.
	Disconnect()

	// Disconnected returns a channel that is closed after Disconnect() completes.
	Disconnected() <-chan struct{}
}

// ConsensusBroadcaster is the set of methods to publish consensus messages to the network.
type ConsensusBroadcaster interface {
	OutgoingProposedBlocks() chan<- tmconsensus.ProposedBlock

	OutgoingPrevoteProofs() chan<- tmconsensus.PrevoteSparseProof
	OutgoingPrecommitProofs() chan<- tmconsensus.PrecommitSparseProof
}
