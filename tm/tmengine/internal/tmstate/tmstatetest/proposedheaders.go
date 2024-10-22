package tmstatetest

import (
	"github.com/gordian-engine/gordian/tm/tmconsensus"
	"github.com/gordian-engine/gordian/tm/tmconsensus/tmconsensustest"
)

// ProposedHeaderMutator is returned by [UnacceptableProposedHeaderMutations]
// to be used in table-driven tests when modifying an otherwise good proposed header
// to be ignored by the state machine.
type ProposedHeaderMutator struct {
	Name   string
	Mutate func(*tmconsensus.ProposedHeader)
}

// UnacceptableProposedHeaderMutations returns a slice of mutators
// that can be used in table-driven tests to ensure that the state machine
// ignores certain invalid proposed headers.
//
// The nCurVals and nNextVals arguments are used to determine
// an incorrect number of validators to set in the proposed header,
// in order to be ignored.
func UnacceptableProposedHeaderMutations(
	hs tmconsensus.HashScheme, nCurVals, nNextVals int,
) []ProposedHeaderMutator {
	return []ProposedHeaderMutator{
		{
			Name: "wrong PrevAppStateHash",
			Mutate: func(ph *tmconsensus.ProposedHeader) {
				ph.Header.PrevAppStateHash = []byte("wrong")
			},
		},
		{
			Name: "extra current validator",
			Mutate: func(ph *tmconsensus.ProposedHeader) {
				var err error
				ph.Header.ValidatorSet, err = tmconsensus.NewValidatorSet(
					tmconsensustest.DeterministicValidatorsEd25519(nCurVals+1).Vals(),
					hs,
				)
				if err != nil {
					panic(err)
				}
			},
		},
		{
			Name: "extra next validator",
			Mutate: func(ph *tmconsensus.ProposedHeader) {
				var err error
				ph.Header.NextValidatorSet, err = tmconsensus.NewValidatorSet(
					tmconsensustest.DeterministicValidatorsEd25519(nNextVals+1).Vals(),
					hs,
				)
				if err != nil {
					panic(err)
				}
			},
		},
	}
}
