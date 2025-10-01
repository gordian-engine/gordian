package tmi

import (
	"github.com/gordian-engine/gordian/tm/tmconsensus"
)

// RVFieldFlags is used when looking up
// a [tmconsensus.RoundView] or [tmconsensus.VersionedRoundView]
// to indicate which fields should be set in the response.
//
// We assume that it is relatively expensive to copy or clone
// the potentially large sets of proposed blocks or votes,
// so by requesting specific fields, we avoid unnecessary work
// and make a possibly non-negligible garbage reduction.
type RVFieldFlags uint8

const (
	RVValidators RVFieldFlags = 1 << iota
	RVProposedBlocks
	RVPrevotes
	RVPrecommits
	RVVoteSummary
	RVPrevCommitProof

	RVAll = (RVValidators | RVProposedBlocks | RVPrevotes | RVPrecommits | RVVoteSummary | RVPrevCommitProof)
)

// ViewLookupRequest is a request to copy a view from the kernel,
// by looking up an existing view by its height and round.
type ViewLookupRequest struct {
	H uint64
	R uint32

	// Which fields on VRV to populate.
	Fields RVFieldFlags

	// Reference to a VersionedRoundView,
	// to be populated by the kernel if there is a matching view for H and R.
	VRV *tmconsensus.VersionedRoundView

	// Reason for looking up the view; for debugging.
	// Must not be empty.
	Reason string

	// The requester must set this to a 1-buffered channel.
	// Once the kernel sends a response,
	// the VRV field may be inspected.
	Resp chan ViewLookupResponse
}

type ViewLookupResponse struct {
	// The ID of the matching view, or notFoundViewID (0) if no view matched.
	ID ViewID

	// The status is viewFound if request.VRV is populated,
	// otherwise it explains the reason why no view was found.
	Status ViewLookupStatus
}

// ViewLookupStatus indicates the reason a view lookup failed to match an existing view.
type ViewLookupStatus uint8

//go:generate go run golang.org/x/tools/cmd/stringer -type ViewLookupStatus -trimprefix=View
const (
	ViewFound ViewLookupStatus = iota

	// Earlier than the committing height and round.
	ViewBeforeCommitting

	// Correct committing height but round is too far.
	// It should be impossible for two well-behaving clients
	// to disagree on which round is being committed in a height.
	ViewWrongCommit

	// Same height as the voting view, but an earlier round.
	ViewOrphaned

	// The requested height and round is beyond NextHeight and NextRound.
	// The data may still be valid.
	ViewFuture
)
