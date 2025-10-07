package tmelink

// RoundSessionChange is emitted from the engine,
// as the engine gathers new information from the network
// and the local state machine.
//
// The primary use case for this type is for p2p implementations
// that need to use something resembling stateful sessions.
type RoundSessionChange struct {
	Height uint64
	Round  uint32
	State  RoundSessionState
}

// RoundSessionState is the state of a session,
// used in [RoundSessionChange].
type RoundSessionState uint8

const (
	_ RoundSessionState = iota // Zero value reserved.

	// The round is now relevant,
	// because we have received a message from a peer indicating such,
	// or because the state machine has advanced to a new height or round.
	RoundSessionStateActive

	// This round is in the "grace period" before expiration.
	// This is only used for earlier rounds in the current or previous height,
	// to distinguish them from the "active" session.
	//
	// From the engine's perspective,
	// there is no limit on the number of rounds in the grace period.
	// A driver implementation may decide to put a limit on the active sessions.
	// A well-behaved driver expecting Round 0 would see the session for Round R
	// and the nil precommit for R-1 would suffice as proof of voting on R.
	RoundSessionStateGrace

	// Expired indicates that the round
	RoundSessionStateExpired
)
