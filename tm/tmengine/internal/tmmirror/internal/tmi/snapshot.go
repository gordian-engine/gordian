package tmi

import "github.com/gordian-engine/gordian/tm/tmconsensus"

// Snapshot is a copy of the kernel's state,
// used in methods running on other goroutines
// that need to know the kernel's current view of the world.
type Snapshot struct {
	Voting, Committing *tmconsensus.VersionedRoundView
}

// snapshotRequest is used when an external goroutine
// needs an up-to-date copy of the kernel state,
// and it knows exactly which view it is looking for.
type SnapshotRequest struct {
	Snapshot *Snapshot

	// TODO: if allocating a Ready channel every request shows up in profiles at all,
	// the mirror could manage a pool of response channels.
	Ready chan struct{}

	// Which fields to include in the request.
	Fields RVFieldFlags
}
