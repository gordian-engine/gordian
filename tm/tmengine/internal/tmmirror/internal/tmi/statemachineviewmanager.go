package tmi

import (
	"errors"
	"fmt"

	"github.com/rollchains/gordian/gcrypto"
	"github.com/rollchains/gordian/tm/tmconsensus"
	"github.com/rollchains/gordian/tm/tmengine/internal/tmeil"
)

// stateMachineViewManager manages the View values that need to be sent to the state machine.
//
// While the views that the kernel sends to the gossip strategy are effectively stateless,
// there are certain transitionary states that the mirror will quickly discard
// but which must be sent to the state machine in order to keep it in sync.
// Specifically, if a round commits nil, the mirror kernel immediately advances to the next round,
// but the state machine needs that view update which is now referring to an "orphaned" view.
// Otherwise it would be stuck in that orphaned view
// until the kernel discovers a new view it can send to break out,
// such as the height being committed or a subsequent round surpassing a vote threshold.
//
// Also, if the mirror jumps ahead one or more rounds without seeing a complete nil commit
// in a single round, the mirror needs to explicitly share
// that information with the state machine.
type stateMachineViewManager struct {
	out chan<- tmeil.StateMachineRoundView

	// The local state machine's height and round,
	// and the channel it may use to send its actions.
	roundEntrance tmeil.StateMachineRoundEntrance

	// How we separately track the version we've sent,
	// to know if we need to send a new view.
	lastSentVersion uint32

	// There are some circumstances where the kernel needs to ensure
	// that the state machine receives a particular view
	// that the kernel is no longer going to track.
	// See the ForceSend method for more details.
	forceSend *tmconsensus.VersionedRoundView

	// The mirror may have a minority+ level of votes for a later round,
	// indicating the network is on that round;
	// but the mirror may not have sufficient information to nil-commit the current round.
	// In that event the jumpAhead field is set, via the JumpToRound method.
	// This indicates to the state machine that there is sufficient information
	// to move the round forward without "completing" the round.
	jumpAhead *tmconsensus.VersionedRoundView

	// The plain view that the mirror wants to send to the state machine,
	// given the current state machine height and round.
	outgoingView tmconsensus.VersionedRoundView
}

func newStateMachineViewManager(out chan<- tmeil.StateMachineRoundView) stateMachineViewManager {
	return stateMachineViewManager{out: out}
}

func (m *stateMachineViewManager) SetView(v tmconsensus.VersionedRoundView) {
	m.outgoingView = v.Clone()
}

func (m *stateMachineViewManager) JumpToRound(v tmconsensus.VersionedRoundView) {
	clone := v.Clone()
	m.jumpAhead = &clone
}

// Output returns a stateMachineOutput value
// which contains a channel and a value to send,
// if the state machine is due for a view update.
func (m *stateMachineViewManager) Output(s *kState) stateMachineOutput {
	if m.forceSend != nil {
		return stateMachineOutput{
			m:  m,
			Ch: m.out,
			Val: tmeil.StateMachineRoundView{
				VRV: *m.forceSend,
				// TODO: Would we ever have m.jumpAhead set during a forced send?
			},
			sentVersion: m.forceSend.Version,
		}
	}

	if m.outgoingView.Height == m.roundEntrance.H &&
		m.outgoingView.Round == m.roundEntrance.R {
		// We might send here, if we have either a new VRV or a jump ahead.
		var val tmeil.StateMachineRoundView
		var sentVersion uint32

		if m.jumpAhead != nil {
			val.JumpAheadRoundView = m.jumpAhead
			sentVersion = m.lastSentVersion
		}

		if m.outgoingView.Version > m.lastSentVersion {
			val.VRV = m.outgoingView.Clone() // TODO: this is probably a wasteful clone.
			sentVersion = m.outgoingView.Version
		}

		if sentVersion > 0 {
			return stateMachineOutput{
				m:           m,
				Ch:          m.out,
				Val:         val,
				sentVersion: sentVersion,
			}
		}
	}

	return stateMachineOutput{}
}

// ForceSend is used by the Kernel to set the Output value to a VersionedRoundView
// that kState is not going to continue tracking.
//
// More specifically, if the kernel is going to advance the voting round,
// but the state machine has not received the update containing the nil precommit,
// the Kernel uses this method to ensure the state machine receives the update
// containing the precommit details to also advance its state to the next round.
func (m *stateMachineViewManager) ForceSend(vrv *tmconsensus.VersionedRoundView) {
	if m.forceSend != nil {
		panic(fmt.Errorf(
			`BUG: (*stateMachineViewManager).ForceSend called twice

Old value: %#v

New value: %#v`,
			m.forceSend, vrv,
		))
	}

	m.forceSend = vrv
}

func (m *stateMachineViewManager) H() uint64 {
	return m.roundEntrance.H
}

func (m *stateMachineViewManager) R() uint32 {
	return m.roundEntrance.R
}

func (m *stateMachineViewManager) Actions() <-chan tmeil.StateMachineRoundAction {
	return m.roundEntrance.Actions
}

func (m *stateMachineViewManager) PubKey() gcrypto.PubKey {
	return m.roundEntrance.PubKey
}

func (m *stateMachineViewManager) Reset(re tmeil.StateMachineRoundEntrance) {
	m.roundEntrance = re
	m.lastSentVersion = 0
}

func (m *stateMachineViewManager) MarkFirstSentVersion(version uint32) {
	if m.lastSentVersion != 0 {
		panic(fmt.Errorf(
			"BUG: (*stateMachineViewManager).MarkFirstSentVersion must only be called with version == 0 (got %d)",
			m.lastSentVersion,
		))
	}

	m.lastSentVersion = version
}

// stateMachineOutput contains a channel and a value to send.
// This value should only be created through the [stateMachineViewManager.Output] method.
//
// If there is no update due, the Ch field is nil, so that using it in a select will not match.
type stateMachineOutput struct {
	m *stateMachineViewManager

	Ch  chan<- tmeil.StateMachineRoundView
	Val tmeil.StateMachineRoundView

	sentVersion uint32
}

func (o *stateMachineOutput) MarkSent() {
	if o.m == nil {
		panic(errors.New("BUG: MarkSent called on no-op stateMachineOutput"))
	}

	// Always clear the pointer values when marking sent.
	o.m.forceSend = nil
	o.m.jumpAhead = nil

	o.m.lastSentVersion = o.sentVersion
}
