package tmi

import (
	"github.com/rollchains/gordian/tm/tmconsensus"
)

// ViewID is the identifier to distinguish View values from one another.
type ViewID uint8

//go:generate go run golang.org/x/tools/cmd/stringer -type ViewID -trimprefix=ViewID .
const (
	ViewIDNotFound ViewID = iota

	ViewIDVoting
	ViewIDCommitting
	ViewIDNextRound
	ViewIDNextHeight
)

// View holds a maintained round view and associated metadata.
type View struct {
	VRV      tmconsensus.VersionedRoundView
	Outgoing OutgoingView
}

func (v *View) UpdateOutgoing() {
	v.VRV.Version++

	if v.Outgoing.HasBeenSent() {
		// Current slices and maps could be in use by the outgoing consumer,
		// so use a whole new clone.
		v.Outgoing.VRV = v.VRV.Clone()
		return
	}

	// TODO: as a memory optimization, we should be able to modify the outgoing VRV in-place.
	// Since it hasn't been sent yet, there should be no references outside of v.
	// But for now we will continue to clone it.
	v.Outgoing.VRV = v.VRV.Clone()
}

type OutgoingView struct {
	SentH uint64 // Height.
	SentR uint32 // Round.
	SentV uint32 // RoundView version.

	VRV tmconsensus.VersionedRoundView
}

func (v OutgoingView) HasBeenSent() bool {
	return v.SentH == v.VRV.Height &&
		v.SentR == v.VRV.Round &&
		v.SentV == v.VRV.Version
}

func (v *OutgoingView) MarkSent() {
	v.SentH = v.VRV.Height
	v.SentR = v.VRV.Round
	v.SentV = v.VRV.Version
}
