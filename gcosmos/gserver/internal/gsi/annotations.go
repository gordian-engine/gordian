package gsi

import "github.com/rollchains/gordian/gcosmos/gserver/internal/gsbd"

// ProposalDriverAnnotation is the annotation to be JSON-marshaled
// and stored on proposed blocks, in the [ConsensusStrategy].
type ProposalDriverAnnotation struct {
	DataSize  int
	Locations []gsbd.Location
}
