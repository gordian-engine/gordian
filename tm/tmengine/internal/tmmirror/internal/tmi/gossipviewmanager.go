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
}

func newGossipViewManager(out chan<- tmelink.NetworkViewUpdate) gossipViewManager {
	return gossipViewManager{out: out}
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

	return o
}

type gossipStrategyOutput struct {
	m *gossipViewManager

	Ch  chan<- tmelink.NetworkViewUpdate
	Val tmelink.NetworkViewUpdate
}

// MarkSent updates o's GossipViewManager to indicate the values in o
// have successfully been sent.
func (o gossipStrategyOutput) MarkSent() {
	if o.Val.Committing != nil {
		o.m.Committing.MarkSent()
	}

	if o.Val.Voting != nil {
		o.m.Voting.MarkSent()
	}

	if o.Val.NextRound != nil {
		o.m.NextRound.MarkSent()
	}

	// Always clear the NilVotedRound; no version tracking involved there.
	o.m.NilVotedRound = nil
}
