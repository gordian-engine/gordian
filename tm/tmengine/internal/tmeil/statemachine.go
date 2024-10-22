package tmeil

import (
	"github.com/gordian-engine/gordian/gcrypto"
	"github.com/gordian-engine/gordian/tm/tmconsensus"
)

// StateMachineRoundEntrance is the value the state machine sends to the engine,
// specifically routed to the mirror kernel.
//
// The state machine indicates its current height and round,
// its public key if it has one,
// and a StateMachineRoundAction channel to send its actions.
// The channel will be 3-buffered so that each action can be sent independently
// and without blocking.
// If the state machine is not participating in that round,
// the channels may be nil.
//
// Values sent by the state machine while the height and round
// match the current committing or voting round view
// should be merged with the current state and persisted to the store.
// However, if the state machine begins to fall behind,
// the mirror may ignore values sent on an action set
// belonging to a stale round.
type StateMachineRoundEntrance struct {
	H uint64
	R uint32

	PubKey gcrypto.PubKey

	Actions chan StateMachineRoundAction

	// The mirror kernel closes this channel when its committing view
	// is shifted out, indicating the height is canonically on-chain.
	// The state machine treats this as a signal that
	// the commit wait timer is no longer required.
	HeightCommitted chan<- struct{}

	Response chan RoundEntranceResponse
}

// StateMachineRoundAction is the collection of actions that a state machine
// sends to the engine (specifically to the mirror)
// indicating its actions for the round.
//
// Exactly one of the fields must be set.
type StateMachineRoundAction struct {
	PH                 tmconsensus.ProposedHeader
	Prevote, Precommit ScopedSignature
}

// ScopedSignature is a pair of target hash and signature that have an implied height and round,
// stated explicitly in the H and R fields of a [StateMachineRoundEntrance].
type ScopedSignature struct {
	TargetHash string

	// In case this is the first vote for this hash,
	// the state machine provides the sign content so that
	// the mirror does not have to duplicate work to recalculate the same signing content.
	SignContent []byte

	Sig []byte
}

// RoundEntranceResponse is the state-synchronizing value that the mirror sends to the state machine
// following a state machine's updated [StateMachineRoundEntrance],
// which is the result of the state machine changing rounds.
//
// Only one of the VRV or CH fields will be set.
//
// After a round sync, the mirror sends [StateMachineRoundView] values.
type RoundEntranceResponse struct {
	VRV tmconsensus.VersionedRoundView

	CH tmconsensus.CommittedHeader
}

// IsVRV reports whether r contains a VersionedRoundView,
// by checking for a non-zero height on r.VRV.
func (r RoundEntranceResponse) IsVRV() bool {
	return r.VRV.Height > 0
}

// IsCH reports whether r contains a CommittedHeader,
// by checking for a non-zero height on r.CH.
func (r RoundEntranceResponse) IsCH() bool {
	return r.CH.Header.Height > 0
}

// StateMachineRoundView is the set of values the mirror sends to the state machine
// regularly throughout a single round, as the mirror receives updates from tne network.
type StateMachineRoundView struct {
	// The VRV for the state machine's current round.
	// Value, not pointer, since it is almost always set.
	VRV tmconsensus.VersionedRoundView

	// When the mirror sees sufficient votes for a future round within the same height,
	// it sets the JumpAheadRoundView to the details of that round.
	// At that point, the state machine should assume no further round view updates,
	// and it should enter the round specified by this field.
	//
	// Pointer because it is rarely set.
	JumpAheadRoundView *tmconsensus.VersionedRoundView

	// If the state machine has fallen behind to the point where
	// the height it was on has been committed,
	// the mirror will send a committed block on the round view update channel.
	//
	// Pointer because it is rarely set.
	CH *tmconsensus.CommittedHeader
}
