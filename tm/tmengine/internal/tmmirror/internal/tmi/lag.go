package tmi

import (
	"github.com/gordian-engine/gordian/tm/tmengine/tmelink"
)

// lagManager holds the current lag state and whether it has been sent.
type lagManager struct {
	outCh chan<- tmelink.LagState

	state tmelink.LagState

	sent bool
}

func newLagManager(out chan<- tmelink.LagState) lagManager {
	return lagManager{outCh: out}
}

func (m *lagManager) SetState(
	s tmelink.LagStatus,
	committingHeight, needHeight uint64,
) {
	if m.outCh == nil {
		// The lag manager should rarely be unset,
		// but no need to copy a few values around if we never output anything.
		return
	}

	if m.state.Status != s {
		m.state.Status = s
		m.sent = false
	}

	m.state.CommittingHeight = committingHeight
	m.state.NeedHeight = needHeight
}

// Output returns a LagOutput,
// containing a destination channel and a LagState value to send.
//
// If the most recent lag state has already been sent,
// the output channel is nil, so the send will block forever.
func (m *lagManager) Output() LagOutput {
	if m.outCh == nil || m.sent {
		// Lag output not configured, or already up to date;
		// nil output channel will block sends forever.
		return LagOutput{}
	}

	return LagOutput{
		m:   m,
		Ch:  m.outCh,
		Val: m.state,
	}
}

// MarkSent must be called after a successful send of o.State to o.Ch.
func (o LagOutput) MarkSent() {
	o.m.sent = true
}

// LagOutput is the value returned by [*LagManager.Output].
type LagOutput struct {
	m   *lagManager
	Ch  chan<- tmelink.LagState
	Val tmelink.LagState
}
