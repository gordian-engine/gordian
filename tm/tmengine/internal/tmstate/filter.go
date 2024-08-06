package tmstate

import (
	"slices"

	"github.com/rollchains/gordian/tm/tmconsensus"
)

// rejectMismatchedProposedBlocks returns a copy of the input slice,
// excluding any proposed blocks that do not match
// the expected previous app state hash or current validators.
func rejectMismatchedProposedBlocks(
	in []tmconsensus.ProposedBlock,
	wantPrevAppStateHash string,
	wantCurValidators, wantNextValidators []tmconsensus.Validator,
) []tmconsensus.ProposedBlock {
	if in == nil {
		// Special case; don't bother allocating an empty slice here.
		return nil
	}

	out := make([]tmconsensus.ProposedBlock, 0, len(in))
	for _, pb := range in {
		if string(pb.Block.PrevAppStateHash) != wantPrevAppStateHash {
			continue
		}

		if !tmconsensus.ValidatorSlicesEqual(pb.Block.Validators, wantCurValidators) {
			continue
		}

		if !tmconsensus.ValidatorSlicesEqual(pb.Block.NextValidators, wantNextValidators) {
			continue
		}

		out = append(out, pb)
	}

	return slices.Clip(out)
}
