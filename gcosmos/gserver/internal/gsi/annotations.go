package gsi

import "github.com/gordian-engine/gordian/gcosmos/gserver/internal/gsbd"

// ProposalDriverAnnotation is the annotation to be JSON-marshaled
// and stored on proposed blocks, in the [ConsensusStrategy].
type ProposalDriverAnnotation struct {
	// Locations is a slice of addresses on where the proposer
	// expects the blodk data to be retrievable.
	Locations []gsbd.Location
}
