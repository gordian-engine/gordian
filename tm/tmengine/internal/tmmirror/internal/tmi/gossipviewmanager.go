package tmi

import (
	"github.com/gordian-engine/gordian/tm/tmconsensus"
	"github.com/gordian-engine/gordian/tm/tmengine/tmelink"
)

// The gossipViewManager contains [OutgoingView] values
// corresponding to the Committing, Voting, and NextRound views.
// The [Kernel] updates those fields,
// and then calls [*gossipViewManager.Output] to get a (possibly nil) channel
// and value to send on it, from the kernel's main loop,
//
// Ultimately, the channel receiver is the
// [github.com/gordian-engine/gordian/tm/tmgossip.Strategy]
// as wired up in tmengine.
type gossipViewManager struct {
	out chan<- tmelink.NetworkViewUpdate

	// When the kernel transitions from one voting round to another,
	// we need to emit the nil-committed round to the gossip strategy.
	// This field holds that value until it is sent to the gossip strategy.
	NilVotedRound *tmconsensus.VersionedRoundView

	Committing, Voting, NextRound OutgoingView

	pendingRoundSessionChanges []tmelink.RoundSessionChange

	// Track what height-rounds we've reported to be in the grace period.
	// These are eventually marked as expired late in [*gossipStrategyOutput.MarkSent].
	inGrace map[hr]struct{}
}

type hr struct {
	H uint64
	R uint32
}

func newGossipViewManager(out chan<- tmelink.NetworkViewUpdate) gossipViewManager {
	return gossipViewManager{
		out: out,

		inGrace: make(map[hr]struct{}),
	}
}

func (m *gossipViewManager) Output() gossipStrategyOutput {
	o := gossipStrategyOutput{m: m}

	// TODO: The eager cloning here likely creates extra garbage that we accidentally can't use,
	// but we should be able to reduce it by overwriting existing values,
	// or by using pooled VRVs.

	// In each check whether the view has been sent,
	// we unconditionally (re)assign the output channel.
	// If we don't hit any of those checks, the output channel will be nil,
	// so that case will not be considered in the select.

	if !m.Committing.HasBeenSent() {
		o.Ch = m.out

		val := m.Committing.VRV.Clone()
		o.Val.Committing = &val
	}

	if !m.Voting.HasBeenSent() {
		o.Ch = m.out

		val := m.Voting.VRV.Clone()
		o.Val.Voting = &val
	}

	if !m.NextRound.HasBeenSent() {
		o.Ch = m.out

		val := m.NextRound.VRV.Clone()
		o.Val.NextRound = &val
	}

	// The nil voted round handling is a little different.
	// There is not particular version handling for a nil voted round;
	// whatever we had when we advanced the round, we send.
	if m.NilVotedRound != nil {
		o.Ch = m.out

		o.Val.NilVotedRound = m.NilVotedRound
	}

	if len(m.pendingRoundSessionChanges) > 0 {
		o.Ch = m.out

		o.Val.RoundSessionChanges = m.pendingRoundSessionChanges
	}

	return o
}

// Grace adds a grace-period round session change
// for the given height and round.
func (m *gossipViewManager) Grace(height uint64, round uint32) {
	m.pendingRoundSessionChanges = append(
		m.pendingRoundSessionChanges,
		tmelink.RoundSessionChange{
			Height: height,
			Round:  round,
			State:  tmelink.RoundSessionStateGrace,
		},
	)

	m.inGrace[hr{H: height, R: round}] = struct{}{}
}

// Activate adds an activated round session change
// for the given height and round.
func (m *gossipViewManager) Activate(height uint64, round uint32) {
	m.pendingRoundSessionChanges = append(
		m.pendingRoundSessionChanges,
		tmelink.RoundSessionChange{
			Height: height,
			Round:  round,
			State:  tmelink.RoundSessionStateActive,
		},
	)

	// No map necessary to track active rounds.
}

// Expire notes the given height and round's session is expired.
func (m *gossipViewManager) Expire(height uint64, round uint32) {
	m.pendingRoundSessionChanges = append(
		m.pendingRoundSessionChanges,
		tmelink.RoundSessionChange{
			Height: height,
			Round:  round,
			State:  tmelink.RoundSessionStateExpired,
		},
	)
}

type gossipStrategyOutput struct {
	m *gossipViewManager

	Ch  chan<- tmelink.NetworkViewUpdate
	Val tmelink.NetworkViewUpdate
}

// MarkSent updates o's GossipViewManager to indicate the values in o
// have successfully been sent.
func (o gossipStrategyOutput) MarkSent() {
	var committingHeight uint64

	if o.Val.Committing != nil {
		committingHeight = o.m.Committing.VRV.Height
		o.m.Committing.MarkSent()
	}

	if o.Val.Voting != nil {
		o.m.Voting.MarkSent()
	}

	if o.Val.NextRound != nil {
		o.m.NextRound.MarkSent()
	}

	o.m.pendingRoundSessionChanges = nil

	// Now that the gossip strategy is aware we have a particular committing round,
	// check if any grace period sessions are old,
	// and mark them expired.
	//
	// We could possibly choose to do more than two grace heights at some point.
	const graceHeightCount = 2
	if committingHeight > graceHeightCount {
		for hr := range o.m.inGrace {
			if hr.H < committingHeight-graceHeightCount {
				delete(o.m.inGrace, hr)
				o.m.pendingRoundSessionChanges = append(
					o.m.pendingRoundSessionChanges,
					tmelink.RoundSessionChange{
						Height: hr.H,
						Round:  hr.R,
						State:  tmelink.RoundSessionStateExpired,
					},
				)
			}
		}
	}

	// Always clear the NilVotedRound; no version tracking involved there.
	o.m.NilVotedRound = nil
}
