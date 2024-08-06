package tsi

import "github.com/rollchains/gordian/tm/tmconsensus"

// Step is the granular step within a single height-round.
type Step uint8

//go:generate go run golang.org/x/tools/cmd/stringer -type Step -trimprefix=Step .
const (
	// Zero value is an invalid step,
	// so that "return 0" can be used where we want to return a meaningless step.
	StepInvalid Step = iota

	// We are waiting on a proposed block.
	// If allowing multiple proposed blocks,
	// we may have any number of proposed blocks,
	// but the consensus strategy has not yet chosen one.
	// This also implies that the proposal timeout has not yet elapsed.
	StepAwaitingProposal

	// We are waiting for prevotes.
	// If we have any prevotes yet,
	// we are at <= 2/3 voting power.
	StepAwaitingPrevotes

	// We have > 2/3 voting power present in prevotes,
	// but we have <= 2/3 voting power in favor of a single proposed block or nil.
	// There is an associated timer with this step.
	// The hope is that, during this delay,
	// we see further prevotes that show > 2/3 voting power
	// favoring a single proposed block or nil.
	StepPrevoteDelay

	// We are waiting for precommits.
	// If we have any precommits yet,
	// we are at <= 2/3 voting power.
	StepAwaitingPrecommits

	// We have > 2/3 voting power present in precommits,
	// but we have <= 2/3 voting power in favor of a single proposed block or nil.
	// There is an associated timer with this step.
	// The hope is that, during this delay,
	// we see further precommits that show > 2/3 voting power
	// favoring a single proposed block or nil.
	StepPrecommitDelay

	// We have > 2/3 precommits in favor of a single block,
	// so that block will be committed.
	//
	// At this point, we are waiting for both the commit timeout to elapse,
	// and the app to send the block finalization.
	// If the commit timeout elapses first, we advance to StepAwaitingFinalization.
	// If the app finalizes the block before the commit timeout elapses,
	// which is what should happen under normal circumstances,
	// we remain in StepCommitWait until the timeout elapses,
	// and then "fast-forward" through StepAwaitingFinalization.
	StepCommitWait

	// The commit wait has elapsed, but the app has not yet
	// finalized the block.
	StepAwaitingFinalization
)

// GetStepFromVoteSummary returns the appropriate Step value
// given only a VoteSummary.
// The state machine calls this upon entering a round,
// when it has no further accumulated state (such as a started timer).
// The state machine must also inspect its action store,
// as its own actions may influence moving the step forward.
func GetStepFromVoteSummary(vs tmconsensus.VoteSummary) Step {
	// We have to work backwards to determine the appropriate step.
	maj := tmconsensus.ByzantineMajority(vs.AvailablePower)
	min := tmconsensus.ByzantineMinority(vs.AvailablePower)

	if vs.TotalPrecommitPower >= maj {
		if vs.PrecommitBlockPower[vs.MostVotedPrecommitHash] >= maj {
			return StepCommitWait
		}
		// Not voting for a single block, so we must be in delay.
		return StepPrecommitDelay
	}

	if vs.TotalPrecommitPower >= min {
		return StepAwaitingPrecommits
	}

	if vs.TotalPrevotePower >= maj {
		if vs.PrevoteBlockPower[vs.MostVotedPrevoteHash] >= maj {
			return StepAwaitingPrecommits
		}
		// Not voting for a single block, so we must be in delay.
		return StepPrevoteDelay
	}

	// Neither prevotes nor precommits have crossed a corresponding threshold,
	// so we are in the default position of awaiting a proposal.
	//
	// We explicitly do not have special treatment
	// for total prevote power exceeding the minority vote.
	// A minority prevote for anything does not force us to prevote;
	// we may still wait for proposed blocks,
	// or propose our own block.
	//
	// At worst, we wait for the proposal delay before sending our prevote.
	return StepAwaitingProposal
}
