package tsi

import (
	"context"

	"github.com/rollchains/gordian/tm/tmconsensus"
	"github.com/rollchains/gordian/tm/tmdriver"
	"github.com/rollchains/gordian/tm/tmengine/internal/tmeil"
)

// RoundLifecycle holds the values that need to exist only through a single round in the state machine.
type RoundLifecycle struct {
	Ctx    context.Context
	cancel context.CancelFunc

	H uint64
	R uint32

	S Step

	// Timer and cancel func produced from the [tmstate.RoundTimer].
	StepTimer   <-chan struct{}
	CancelTimer func()

	// The validators for this height.
	// Derived from the previous block's NextValidators field.
	// Used when proposing a block.
	CurVals []tmconsensus.Validator

	// A non-nil PrevVRV indicates that the round lifecycle is handling live votes.
	// If nil, that indicates the round lifecycle is in replay mode.
	PrevVRV             *tmconsensus.VersionedRoundView
	PrevBlockHash       string // The previous block hash as reported by the mirror when entering a round.
	PrevFinNextVals     []tmconsensus.Validator
	PrevFinAppStateHash string

	// By tracking the previously considered hashes,
	// we can easily provide a hint to the consensus strategy
	// indicating which of these proposed blocks are new.
	//
	// These only need to be the included blocks' hashes;
	// no need to include blocks that were excluded due to app hash mismatches, etc.
	PrevConsideredHashes map[string]struct{}

	// Channel to alert Mirror of actions we've taken in this round.
	// Nil when in replay mode.
	OutgoingActionsCh chan tmeil.StateMachineRoundAction

	// Channels for the consensus manager to write.
	ProposalCh      chan tmconsensus.Proposal
	PrevoteHashCh   chan HashSelection
	PrecommitHashCh chan HashSelection

	// For the driver to write directly.
	FinalizeRespCh chan tmdriver.FinalizeBlockResponse

	// Values reported by the application for the finalization of the current round.
	FinalizedValidators   []tmconsensus.Validator
	FinalizedAppStateHash string

	CommitWaitElapsed bool
}

func (rlc *RoundLifecycle) Reset(ctx context.Context, h uint64, r uint32) {
	if rlc.cancel != nil {
		// Should only be nil on first call to reset.
		rlc.cancel()
	}

	rlc.Ctx, rlc.cancel = context.WithCancel(ctx)
	rlc.H = h
	rlc.R = r

	if rlc.CancelTimer != nil {
		rlc.CancelTimer()
		rlc.CancelTimer = nil
		rlc.StepTimer = nil
	}

	// These are probably correct as 1-buffered,
	// so that an accidental stale send would not block.
	// Although if the send respects rlc.Ctx, that might achieve the same effect.
	rlc.ProposalCh = make(chan tmconsensus.Proposal, 1)
	rlc.PrevoteHashCh = make(chan HashSelection, 1)
	rlc.PrecommitHashCh = make(chan HashSelection, 1)

	rlc.FinalizeRespCh = make(chan tmdriver.FinalizeBlockResponse, 1)

	rlc.CommitWaitElapsed = false

	// The hashes may have been cleared already in some circumstances,
	// but a second clear won't hurt.
	clear(rlc.PrevConsideredHashes)
}

func (rlc RoundLifecycle) IsReplaying() bool {
	return rlc.PrevVRV == nil
}

// CycleFinalization moves the current round's finalization
// into the previous finalization fields.
func (rlc *RoundLifecycle) CycleFinalization() {
	// Cycle the validators.
	rlc.PrevFinNextVals, rlc.CurVals, rlc.FinalizedValidators =
		rlc.FinalizedValidators, rlc.PrevFinNextVals, nil

	rlc.PrevFinAppStateHash = rlc.FinalizedAppStateHash
	rlc.FinalizedAppStateHash = ""

	rlc.PrevBlockHash = rlc.PrevVRV.VoteSummary.MostVotedPrecommitHash
}
