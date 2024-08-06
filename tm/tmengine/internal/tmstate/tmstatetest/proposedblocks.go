package tmstatetest

import (
	"github.com/rollchains/gordian/tm/tmconsensus"
	"github.com/rollchains/gordian/tm/tmconsensus/tmconsensustest"
)

// ProposedBlockMutator is returned by [UnacceptableProposedBlockMutations]
// to be used in table-driven tests when modifying an otherwise good proposed block
// to be ignored by the state machine.
type ProposedBlockMutator struct {
	Name   string
	Mutate func(*tmconsensus.ProposedBlock)
}

// UnacceptableProposedBlockMutations returns a slice of mutators
// that can be used in table-driven tests to ensure that the state machine
// ignores certain invalid proposed blocks.
//
// The nCurVals and nNextVals arguments are used to determine
// an incorrect number of validators to set in the proposed block,
// in order to be ignored.
func UnacceptableProposedBlockMutations(nCurVals, nNextVals int) []ProposedBlockMutator {
	return []ProposedBlockMutator{
		{
			Name: "wrong PrevAppStateHash",
			Mutate: func(pb *tmconsensus.ProposedBlock) {
				pb.Block.PrevAppStateHash = []byte("wrong")
			},
		},
		{
			Name: "extra current validator",
			Mutate: func(pb *tmconsensus.ProposedBlock) {
				pb.Block.Validators = tmconsensustest.DeterministicValidatorsEd25519(nCurVals + 1).Vals()
			},
		},
		{
			Name: "extra next validator",
			Mutate: func(pb *tmconsensus.ProposedBlock) {
				pb.Block.NextValidators = tmconsensustest.DeterministicValidatorsEd25519(nNextVals + 1).Vals()
			},
		},
	}
}
