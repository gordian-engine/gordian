package tmstate

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/trace"
	"slices"
	"time"

	"github.com/rollchains/gordian/gcrypto"
	"github.com/rollchains/gordian/gwatchdog"
	"github.com/rollchains/gordian/internal/gchan"
	"github.com/rollchains/gordian/internal/glog"
	"github.com/rollchains/gordian/tm/tmconsensus"
	"github.com/rollchains/gordian/tm/tmdriver"
	"github.com/rollchains/gordian/tm/tmengine/internal/tmeil"
	"github.com/rollchains/gordian/tm/tmengine/internal/tmemetrics"
	"github.com/rollchains/gordian/tm/tmengine/internal/tmstate/internal/tsi"
	"github.com/rollchains/gordian/tm/tmengine/tmelink"
	"github.com/rollchains/gordian/tm/tmstore"
)

type StateMachine struct {
	log *slog.Logger

	signer    gcrypto.Signer
	sigScheme tmconsensus.SignatureScheme

	hashScheme tmconsensus.HashScheme

	genesis tmconsensus.Genesis

	aStore tmstore.ActionStore
	fStore tmstore.FinalizationStore

	rt RoundTimer

	cm *tsi.ConsensusManager

	mc *tmemetrics.Collector

	wd *gwatchdog.Watchdog

	viewInCh               <-chan tmeil.StateMachineRoundView
	roundEntranceOutCh     chan<- tmeil.StateMachineRoundEntrance
	finalizeBlockRequestCh chan<- tmdriver.FinalizeBlockRequest
	blockDataArrivalCh     <-chan tmelink.BlockDataArrival

	kernelDone chan struct{}
}

type StateMachineConfig struct {
	Signer          gcrypto.Signer
	SignatureScheme tmconsensus.SignatureScheme

	HashScheme tmconsensus.HashScheme

	Genesis tmconsensus.Genesis

	ActionStore       tmstore.ActionStore
	FinalizationStore tmstore.FinalizationStore

	RoundTimer RoundTimer

	ConsensusStrategy tmconsensus.ConsensusStrategy

	RoundViewInCh      <-chan tmeil.StateMachineRoundView
	RoundEntranceOutCh chan<- tmeil.StateMachineRoundEntrance

	BlockDataArrivalCh <-chan tmelink.BlockDataArrival

	FinalizeBlockRequestCh chan<- tmdriver.FinalizeBlockRequest

	MetricsCollector *tmemetrics.Collector

	Watchdog *gwatchdog.Watchdog
}

func NewStateMachine(ctx context.Context, log *slog.Logger, cfg StateMachineConfig) (*StateMachine, error) {
	m := &StateMachine{
		log: log,

		signer:    cfg.Signer,
		sigScheme: cfg.SignatureScheme,

		hashScheme: cfg.HashScheme,

		genesis: cfg.Genesis,

		aStore: cfg.ActionStore,
		fStore: cfg.FinalizationStore,

		rt: cfg.RoundTimer,

		cm: tsi.NewConsensusManager(ctx, log.With("sm_sys", "consmgr"), cfg.ConsensusStrategy),

		mc: cfg.MetricsCollector,

		wd: cfg.Watchdog,

		viewInCh:               cfg.RoundViewInCh,
		roundEntranceOutCh:     cfg.RoundEntranceOutCh,
		finalizeBlockRequestCh: cfg.FinalizeBlockRequestCh,
		blockDataArrivalCh:     cfg.BlockDataArrivalCh,

		kernelDone: make(chan struct{}),
	}

	go m.kernel(ctx)

	if m.signer == nil {
		m.log.Info("State machine starting with nil signer; can never participate in consensus")
	}

	return m, nil
}

func (m *StateMachine) Wait() {
	m.cm.Wait()
	<-m.kernelDone
}

func (m *StateMachine) kernel(ctx context.Context) {
	defer close(m.kernelDone)

	ctx, task := trace.NewTask(ctx, "StateMachine.kernel")
	defer task.End()

	rlc, ok := m.initializeRLC(ctx)
	if !ok {
		// Failure during initialization.
		// Already logged, so just quit.
		return
	}

	wSig := m.wd.Monitor(ctx, gwatchdog.MonitorConfig{
		Name:     "StateMachine",
		Interval: 10 * time.Second, Jitter: time.Second,
		ResponseTimeout: time.Second,
	})

	defer func() {
		if !gwatchdog.IsTermination(ctx) {
			return
		}

		m.log.Info(
			"WATCHDOG TERMINATING; DUMPING STATE",
			"rlc", slog.GroupValue(
				slog.Uint64("H", rlc.H), slog.Uint64("R", uint64(rlc.R)),
				slog.Any("S", rlc.S),

				slog.Any("PrevVRV", rlc.PrevVRV),
			),
		)
	}()

	for {
		if rlc.IsReplaying() {
			if !m.handleReplayEvent(ctx, wSig, &rlc) {
				return
			}
		} else {
			if !m.handleLiveEvent(ctx, wSig, &rlc) {
				return
			}
		}
	}
}

func (m *StateMachine) handleReplayEvent(
	ctx context.Context,
	wSig <-chan gwatchdog.Signal,
	rlc *tsi.RoundLifecycle,
) (ok bool) {
	return false
}

func (m *StateMachine) handleLiveEvent(
	ctx context.Context,
	wSig <-chan gwatchdog.Signal,
	rlc *tsi.RoundLifecycle,
) (ok bool) {
	defer trace.StartRegion(ctx, "handleLiveEvent").End()

	select {
	case <-ctx.Done():
		m.log.Info(
			"Quitting due to context cancellation in kernel main loop",
			"cause", context.Cause(ctx),
			"height", rlc.H, "round", rlc.R, "step", rlc.S,
			"vote_summary", rlc.PrevVRV.VoteSummary,
		)
		return false

	case v := <-m.viewInCh:
		m.handleViewUpdate(ctx, rlc, v)

	case p := <-rlc.ProposalCh:
		if !m.recordProposedBlock(ctx, *rlc, p) {
			return false
		}

		rlc.ProposalCh = nil

	case he := <-rlc.PrevoteHashCh:
		if he.Err != nil {
			glog.HRE(m.log, rlc.H, rlc.R, he.Err).Error(
				"Consensus strategy returned error when choosing proposed block to prevote",
			)
			return false
		}

		if !m.recordPrevote(ctx, rlc, he.Hash) {
			return false
		}

		rlc.PrevoteHashCh = nil

	case he := <-rlc.PrecommitHashCh:
		if he.Err != nil {
			glog.HRE(m.log, rlc.H, rlc.R, he.Err).Error(
				"Consensus strategy returned error when deciding precommit",
			)
			return false
		}

		if !m.recordPrecommit(ctx, rlc, he.Hash) {
			return false
		}

		rlc.PrecommitHashCh = nil

	case resp := <-rlc.FinalizeRespCh:
		if !m.handleFinalization(ctx, rlc, resp) {
			return false
		}

		// The other cases unconditionally set the rlc-associated channel to nil,
		// but it is actually conditionally changed in the m.handleFinalization call.
		// If we set it to nil following a height change which may have happend in m.handleFinalization,
		// the state machine will deadlock when the app attempts to send its finalization to a nil channel.

	case <-rlc.StepTimer:
		if !m.handleTimerElapsed(ctx, rlc) {
			return false
		}

	case a := <-m.blockDataArrivalCh:
		if !m.handleBlockDataArrival(ctx, rlc, a) {
			return false
		}

	case sig := <-wSig:
		close(sig.Alive)
	}

	return true
}

func (m *StateMachine) initializeRLC(ctx context.Context) (rlc tsi.RoundLifecycle, ok bool) {
	rlc, su, ok := m.sendInitialActionSet(ctx)
	if !ok {
		// Failure. Assume it was logged.
		return rlc, false
	}

	if !su.IsVRV() {
		return rlc, false
	}

	ok = m.beginRoundLive(ctx, &rlc, su.VRV)
	return rlc, ok
}

// beginRoundLive updates some fields on rlc,
// makes appropriate calls into the consensus strategy based on the initVRV value,
// and starts any necessary timers.
func (m *StateMachine) beginRoundLive(
	ctx context.Context, rlc *tsi.RoundLifecycle, initVRV tmconsensus.VersionedRoundView,
) (ok bool) {
	// Update the state machine's height/round metric,
	// if we are tracking metrics.
	if m.mc != nil {
		m.mc.UpdateStateMachine(tmemetrics.StateMachineMetrics{
			H: initVRV.Height, R: initVRV.Round,
		})
	}

	// Only calculate the step if we are dealing with a round view,
	// not if we have a committed block.
	curStep := tsi.GetStepFromVoteSummary(initVRV.VoteSummary)
	switch curStep {
	case tsi.StepAwaitingProposal:
		// Only send the filtered proposed blocks.
		if okPBs := rejectMismatchedProposedBlocks(
			initVRV.ProposedBlocks,
			rlc.PrevFinAppStateHash,
			rlc.CurVals,
			rlc.PrevFinNextVals,
		); len(okPBs) > 0 {
			req := tsi.ConsiderProposedBlocksRequest{
				PBs:    okPBs,
				Result: rlc.PrevoteHashCh,
			}
			req.MarkReasonNewHashes(rlc)
			if !gchan.SendC(
				ctx, m.log,
				m.cm.ConsiderProposedBlocksRequests, req,
				"making consider proposed blocks request from initial state",
			) {
				// Context cancelled and logged. Quit.
				return false
			}
		}

	case tsi.StepAwaitingPrevotes:
		// See comments in GetStepFromVoteSummary.
		// If above minority prevotes but below majority,
		// we will just wait for a proposal as normal.
		// At worse, we time out on the proposal and prevote nil.
		panic(errors.New(
			"BUG: tsi.GetStepFromVoteSummary must not return tsi.StepAwaitingPrevotes",
		))

	case tsi.StepAwaitingPrecommits:
		if !gchan.SendC(
			ctx, m.log,
			m.cm.DecidePrecommitRequests, tsi.DecidePrecommitRequest{
				VS:     initVRV.VoteSummary.Clone(),
				Result: rlc.PrecommitHashCh,
			},
			"making decide precommit request from initial state",
		) {
			// Context cancelled and logged. Quit.
			return false
		}

	case tsi.StepCommitWait:
		committingHash := initVRV.VoteSummary.MostVotedPrecommitHash
		if committingHash == "" {
			// Ready to commit nil is a special case.
			// If we got here through normal flow we wouldn't be in commit wait.
			// But if the mirror gave us this information, we need to just go to the next round.
			return m.advanceRound(ctx, rlc)
		}

		// Another special case -- the beginCommit method assigns rlc.S and its timers.
		// So we don't want to go past the end of the switch statement
		// which will reassign the timers.
		if !m.beginCommit(ctx, rlc, initVRV) {
			return false
		}
		rlc.PrevVRV = &initVRV
		return true

	default:
		panic(fmt.Errorf("BUG: unhandled initial step %s", curStep))
	}

	rlc.S = curStep
	rlc.PrevVRV = &initVRV

	// Only attempt to start the timer if we have a live view.
	m.startInitialTimer(ctx, rlc)

	return true
}

func (m *StateMachine) startInitialTimer(ctx context.Context, rlc *tsi.RoundLifecycle) {
	switch rlc.S {
	case tsi.StepAwaitingProposal:
		rlc.StepTimer, rlc.CancelTimer = m.rt.ProposalTimer(ctx, rlc.H, rlc.R)
	case tsi.StepAwaitingPrevotes, tsi.StepAwaitingPrecommits:
		// No timer needed in these starting steps.
	case tsi.StepCommitWait:
		rlc.StepTimer, rlc.CancelTimer = m.rt.CommitWaitTimer(ctx, rlc.H, rlc.R)
	default:
		panic(fmt.Errorf("BUG: no initial timer configured for step %s", rlc.S))
	}
}

// sendInitialActionSet loads any initial state from the stores
// and sends the current round action set to the mirror,
// so that the mirror can either begin sending round views or replayed blocks.
//
// If the context is cancelled when executing this method,
// it logs an appropriate error and reports false.
func (m *StateMachine) sendInitialActionSet(ctx context.Context) (
	rlc tsi.RoundLifecycle,
	rer tmeil.RoundEntranceResponse,
	ok bool,
) {
	// Assume for now that we start up with no history.
	// We have to send our height and round to the mirror.
	initRE := tmeil.StateMachineRoundEntrance{
		H: m.genesis.InitialHeight, R: 0,

		Response: make(chan tmeil.RoundEntranceResponse, 1),
	}
	if m.signer != nil {
		initRE.PubKey = m.signer.PubKey()
		initRE.Actions = make(chan tmeil.StateMachineRoundAction, 3)
	}
	rer, ok = gchan.ReqResp(
		ctx, m.log,
		m.roundEntranceOutCh, initRE,
		initRE.Response,
		"seeding initial state from mirror",
	)
	if !ok {
		return rlc, rer, false
	}
	// Closing the outgoing Response channel is not strictly necessary,
	// but it is a little helpful in tests in case a StateMachineRoundEntrance is mistakenly reused.
	close(initRE.Response)

	// Initialize this to a default size;
	// it needs to be a non-nil map regardless of the initial update.
	rlc.PrevConsideredHashes = map[string]struct{}{}

	// We have a response -- do we need to call into the consensus strategy,
	// or do we only need to replay the block?
	if rer.IsVRV() {
		rlc.Reset(ctx, initRE.H, initRE.R)
		rlc.OutgoingActionsCh = initRE.Actions // Should this be part of the Reset method instead?

		// Still assuming we are initializing at the chain's initial height,
		// which will not always be a correct assumption.
		rlc.PrevFinNextVals = slices.Clone(m.genesis.Validators)
		rlc.PrevFinAppStateHash = string(m.genesis.CurrentAppStateHash)

		rlc.CurVals = slices.Clone(m.genesis.Validators)

		// For now, set the previous block hash as the genesis pseudo-block's hash.
		// But maybe it would be better if the mirror generated this
		// and sent it as part of the state update.
		b, err := m.genesis.Block(m.hashScheme)
		if err != nil {
			panic(fmt.Errorf(
				"FATAL: failed to generate genesis block hash: %w", err,
			))
		}
		rlc.PrevBlockHash = string(b.Hash)

		// TODO: this should be setting rlc.PrevVRV somewhere.
		// We need a test in place for that.

		// Overwrite the proposed blocks we present to the consensus strategy,
		// to exclude any known invalid blocks according to the genesis we just parsed.
		rer.VRV.RoundView.ProposedBlocks = rejectMismatchedProposedBlocks(
			rer.VRV.RoundView.ProposedBlocks,
			rlc.PrevFinAppStateHash,
			rlc.CurVals, rlc.PrevFinNextVals,
		)

		// We have to synchronously enter the round,
		// but we still enter through the consensus manager for this.

		req := tsi.EnterRoundRequest{
			RV:     rer.VRV.RoundView,
			Result: make(chan error), // Unbuffered since both sides sync on this.

			ProposalOut: rlc.ProposalCh,
		}

		err, ok := gchan.ReqResp(
			ctx, m.log,
			m.cm.EnterRoundRequests, req,
			req.Result,
			"entering round on consensus strategy",
		)
		if !ok {
			// Context cancelled, we cannot continue.
			return rlc, rer, false
		}
		if err != nil {
			panic(fmt.Errorf(
				"FATAL: error when calling ConsensusStrategy.EnterRound: %v", err,
			))
		}
	} else {
		// TODO: there should be a different method for resetting
		// in expectation of handling a replayed block.
		rlc.Reset(ctx, initRE.H, initRE.R)

		// This is a replay, so we can just tell the driver to finalize it.
		finReq := tmdriver.FinalizeBlockRequest{
			Block: rer.CB.Block,
			Round: rer.CB.Proof.Round,

			Resp: rlc.FinalizeRespCh,
		}

		ok = gchan.SendC(
			ctx, m.log,
			m.finalizeBlockRequestCh, finReq,
			"sending finalize block response for replayed block",
		)
	}

	return rlc, rer, ok
}

func (m *StateMachine) handleViewUpdate(
	ctx context.Context,
	rlc *tsi.RoundLifecycle,
	v tmeil.StateMachineRoundView,
) {
	defer trace.StartRegion(ctx, "handleViewUpdate").End()

	vrv := v.VRV
	if vrv.Height == 0 {
		if v.JumpAheadRoundView == nil {
			panic(errors.New("BUG: received view update with empty VRV and nil JumpAheadRoundView"))
		}

		m.handleJumpAhead(ctx, rlc, *v.JumpAheadRoundView)
		return
	}

	if vrv.Height != rlc.H || vrv.Round != rlc.R {
		m.log.Debug(
			"Received out of bounds voting view",
			"cur_h", rlc.H, "cur_r", rlc.R,
			"recv_h", vrv.Height, "recv_r", vrv.Round,
		)
		return
	}

	if vrv.Version <= rlc.PrevVRV.Version {
		m.log.Warn(
			"Received non-increasing round view version; this is a likely bug",
			"h", rlc.H, "r", rlc.R, "step", rlc.S,
			"prev_version", rlc.PrevVRV.Version, "cur_version", vrv.Version,
		)

		m.log.Info(
			"STATE DUMP DUE TO STALE UPDATE BUG",
			"h", rlc.H, "r", rlc.R, "step", rlc.S,
			"prev_vrv", rlc.PrevVRV,
			"new_vrv", vrv,
		)

		m.wd.Terminate("state machine got non-increasing round view")
		return
	}

	switch rlc.S {
	case tsi.StepAwaitingProposal:
		m.handleProposalViewUpdate(ctx, rlc, vrv)
	case tsi.StepAwaitingPrevotes, tsi.StepPrevoteDelay:
		m.handlePrevoteViewUpdate(ctx, rlc, vrv)
	case tsi.StepAwaitingPrecommits, tsi.StepPrecommitDelay:
		m.handlePrecommitViewUpdate(ctx, rlc, vrv)
	case tsi.StepCommitWait, tsi.StepAwaitingFinalization:
		m.handleCommitWaitViewUpdate(ctx, rlc, vrv)
	default:
		panic(fmt.Errorf("TODO: handle view update for step %q", rlc.S))
	}

	if vrv.Height == rlc.PrevVRV.Height && vrv.Round == rlc.PrevVRV.Round {
		// If the view update caused a nil commit,
		// the incoming vrv's height and round will differ from the set PrevVRV.
		// So we have to check this in order to avoid "moving backwards".

		// Assuming it's okay to take ownership of vrv as opposed to copying in to our value.
		rlc.PrevVRV = &vrv
	}

	if v.JumpAheadRoundView != nil {
		// If the state machine was slow to read,
		// we may have received an update with a VRV and a jump ahead signal.
		// If it was necessary to jump ahead,
		// the VRV value would not have advanced the round,
		// so the call here should be safe.
		m.handleJumpAhead(ctx, rlc, *v.JumpAheadRoundView)
	}
}

// handleProposalViewUpdate is called when we receive an update from the mirror
// and the current step is awaiting proposal.
func (m *StateMachine) handleProposalViewUpdate(
	ctx context.Context,
	rlc *tsi.RoundLifecycle,
	vrv tmconsensus.VersionedRoundView,
) {
	defer trace.StartRegion(ctx, "handleProposalViewUpdate").End()

	min := tmconsensus.ByzantineMinority(vrv.VoteSummary.AvailablePower)
	maj := tmconsensus.ByzantineMajority(vrv.VoteSummary.AvailablePower)

	if vrv.VoteSummary.TotalPrecommitPower >= maj {
		// The majority of the network is on precommit and we are just expecting a proposal.

		// We can start by canceling the proposal timer.
		rlc.CancelTimer()
		rlc.StepTimer = nil
		rlc.CancelTimer = nil

		// There is no point in submitting a prevote at this point,
		// because the majority of the network has already seen enough prevotes
		// to make their precommit decision.

		// If the majority has already reached consensus then
		// we will attempt to submit our own precommit
		// based on the prevotes we have right now.
		vs := vrv.VoteSummary
		maxBlockPow := vs.PrecommitBlockPower[vs.MostVotedPrecommitHash]
		if maxBlockPow >= maj {
			// There is consensus on a hash,
			// but we have to treat nil differently from a particular block.
			if vs.MostVotedPrecommitHash == "" {
				// For now, we just advance the round without submitting our own precommit.
				// We do have sufficient information to submit a precommit,
				// but we ought to adjust the way the consensus strategy is structured
				// in order to indicate that the round is terminating
				// and that the consensus strategy is allowed to elect not to precommit.
				_ = m.advanceRound(ctx, rlc)
				return
			}

			// Otherwise it must be a particular block.
			// Just like the nil precommit case,
			// we are currently not consulting the consensus strategy.
			_ = m.beginCommit(ctx, rlc, vrv)
			return
		}

		// There was majority precommit power present but it was not for a particular block.
		// Start the precommit delay.
		rlc.S = tsi.StepPrecommitDelay
		rlc.StepTimer, rlc.CancelTimer = m.rt.PrecommitDelayTimer(ctx, rlc.H, rlc.R)

		// And we need to submit our own precommit decision still.
		_ = gchan.SendC(
			ctx, m.log,
			m.cm.DecidePrecommitRequests, tsi.DecidePrecommitRequest{
				VS:     vrv.VoteSummary.Clone(), // Clone under assumption to avoid data race.
				Result: rlc.PrecommitHashCh,     // Is it ever possible this channel is nil?
			},
			"deciding precommit following observation of majority precommit while expecting proposal",
		)

		return
	}

	if vrv.VoteSummary.TotalPrecommitPower >= min {
		// A sufficient portion of the rest of the network
		// has decided they have enough information to precommit.
		// So we will begin our precommit too.

		// We can start by canceling the proposal timer.
		rlc.CancelTimer()
		rlc.StepTimer = nil
		rlc.CancelTimer = nil

		rlc.S = tsi.StepAwaitingPrecommits

		// And we need to submit our own precommit decision still.
		_ = gchan.SendC(
			ctx, m.log,
			m.cm.DecidePrecommitRequests, tsi.DecidePrecommitRequest{
				VS:     vrv.VoteSummary.Clone(), // Clone under assumption to avoid data race.
				Result: rlc.PrecommitHashCh,     // Is it ever possible this channel is nil?
			},
			"deciding precommit following observation of minority precommit while expecting proposal",
		)

		return
	}

	if vrv.VoteSummary.TotalPrevotePower >= maj {
		// The majority has made their prevote.

		// We are switching to either awaiting precommits or prevote delay.
		// Either way, when we entered this method, we had a proposal timer,
		// so an unconditional cancel should be safe.
		rlc.CancelTimer()
		rlc.StepTimer = nil
		rlc.CancelTimer = nil

		// And we are making a request to choose or consider in either case too.
		req := tsi.ChooseProposedBlockRequest{
			PBs: rejectMismatchedProposedBlocks(
				vrv.ProposedBlocks,
				rlc.PrevFinAppStateHash,
				rlc.CurVals, rlc.PrevFinNextVals,
			),

			Result: rlc.PrevoteHashCh,
		}

		vs := vrv.VoteSummary
		maxBlockPow := vs.PrevoteBlockPower[vs.MostVotedPrevoteHash]
		if maxBlockPow >= maj {
			// If the majority power is at consensus, we submit our prevote immediately.
			rlc.S = tsi.StepAwaitingPrecommits

			select {
			case m.cm.ChooseProposedBlockRequests <- req:
				// Okay.
			default:
				panic("TODO: handle blocked send to ChooseProposedBlockRequests")
			}

			// Don't need to hold on to any of the previously sent hashes
			// after sending the choose request.
			clear(rlc.PrevConsideredHashes)

			return
		}

		// Otherwise, the majority power is present but there is not yet consensus.
		// Consider the proposed blocks and start the prevote delay.

		rlc.S = tsi.StepPrevoteDelay
		rlc.StepTimer, rlc.CancelTimer = m.rt.PrevoteDelayTimer(ctx, rlc.H, rlc.R)

		if len(req.PBs) > 0 {
			// If we filtered out invalid proposed blocks,
			// don't send the request.
			req := tsi.ConsiderProposedBlocksRequest{
				PBs:    req.PBs, // Outer declaration of req as a choose request.
				Result: rlc.PrevoteHashCh,
			}
			req.MarkReasonNewHashes(rlc)
			req.Reason.MajorityVotingPowerPresent = true
			select {
			case m.cm.ConsiderProposedBlocksRequests <- req:
				// Okay.
			default:
				panic("TODO: handle blocked send to ConsiderProposedBlocksRequests")
			}
		}
		return
	}

	// If we have only exceeded minority prevote power, we will continue waiting for proposed blocks.

	if len(vrv.ProposedBlocks) > len(rlc.PrevVRV.ProposedBlocks) {
		// At least one new proposed block.
		// We are going to inform the consensus strategy (by way of the consensus manager)
		// of the new proposed blocks,
		// but first we need to reject any invalid ones.
		//
		// The guarantee from the mirror (as of writing)
		// is that the incoming proposed block has a valid hash and signature.
		// The mirror does not assume that the state machine
		// has correct state in regard to the rest of the network.
		// So, we need to exclude any incoming proposed blocks that
		// do not match our expected state -- in particular,
		// the previous app state hash and the validator set.
		//
		// Operate on clones to avoid mutating either of the canonical slices.

		incoming := rejectMismatchedProposedBlocks(
			vrv.ProposedBlocks,
			rlc.PrevFinAppStateHash,
			rlc.CurVals,
			rlc.PrevFinNextVals,
		)
		have := rejectMismatchedProposedBlocks(
			rlc.PrevVRV.ProposedBlocks,
			rlc.PrevFinAppStateHash,
			rlc.CurVals,
			rlc.PrevFinNextVals,
		)
		// TODO: we could be more efficient than building up the have slice
		// only to check its length.
		if len(incoming) <= len(have) {
			// After filtering, no new entries.
			// Can't send a request to the consensus manager in this case.
			return
		}

		// The timer hasn't elapsed yet so it is only a Consider call at this point.
		req := tsi.ConsiderProposedBlocksRequest{
			PBs: incoming,

			Result: rlc.PrevoteHashCh,
		}
		req.MarkReasonNewHashes(rlc)
		select {
		case m.cm.ConsiderProposedBlocksRequests <- req:
			// Okay.
		default:
			panic("TODO: handle blocked send to ConsiderProposedBlocksRequests")
		}
	}
}

// handlePrevoteViewUpdate is called when we receive an update from the mirror
// and the current step is awaiting prevotes or prevote delay.
func (m *StateMachine) handlePrevoteViewUpdate(
	ctx context.Context,
	rlc *tsi.RoundLifecycle,
	vrv tmconsensus.VersionedRoundView,
) {
	defer trace.StartRegion(ctx, "handlePrevoteViewUpdate").End()

	vs := vrv.VoteSummary
	maj := tmconsensus.ByzantineMajority(vs.AvailablePower)

	if vs.TotalPrecommitPower >= maj {
		// We are going to transition out of the current step, so clear timers now.
		if rlc.S == tsi.StepPrevoteDelay {
			rlc.CancelTimer()
			rlc.StepTimer = nil
			rlc.CancelTimer = nil
		}

		// There is sufficient power for a commit -- is there a chosen block?
		maxBlockPow := vs.PrecommitBlockPower[vs.MostVotedPrecommitHash]
		if maxBlockPow >= maj {
			if vs.MostVotedPrecommitHash == "" {
				// If the consensus is for nil, advance the round.
				// Currently we do not submit our own precommit,
				// but we probably should in the future.
				_ = m.advanceRound(ctx, rlc)
				return
			}

			// Otherwise it must be a particular block.
			// Just like the nil precommit case,
			// we are currently not consulting the consensus strategy.
			_ = m.beginCommit(ctx, rlc, vrv)
			return
		}

		// Not for a block, so we need to just submit our own precommit.
		rlc.S = tsi.StepPrecommitDelay
		rlc.StepTimer, rlc.CancelTimer = m.rt.PrecommitDelayTimer(ctx, rlc.H, rlc.R)

		_ = gchan.SendC(
			ctx, m.log,
			m.cm.DecidePrecommitRequests, tsi.DecidePrecommitRequest{
				VS:     vrv.VoteSummary.Clone(), // Clone under assumption to avoid data race.
				Result: rlc.PrecommitHashCh,     // Is it ever possible this channel is nil?
			},
			"choosing precommit after observing majority precommit while expecting prevotes",
		)

		return
	}

	// If the total precommit power only exceeded the minority threshold,
	// we don't need to do anything special; continue handling prevotes as normal.

	if vs.TotalPrevotePower >= maj {
		// We have majority vote power present;
		// do we have majority vote power on a single block?
		maxPow := vs.PrevoteBlockPower[vs.MostVotedPrevoteHash]
		if maxPow >= maj {
			if rlc.S == tsi.StepPrevoteDelay {
				// Clear out the old timers.
				rlc.CancelTimer()
				rlc.StepTimer = nil
				rlc.CancelTimer = nil
			}

			rlc.S = tsi.StepAwaitingPrecommits

			_ = gchan.SendC(
				ctx, m.log,
				m.cm.DecidePrecommitRequests, tsi.DecidePrecommitRequest{
					VS:     vrv.VoteSummary.Clone(), // Clone under assumption to avoid data race.
					Result: rlc.PrecommitHashCh,     // Is it ever possible this channel is nil?
				},
				"choosing precommit following majority prevote",
			)

			return
		}

		// We have majority prevotes but not on a single block.
		// Only start the timer if we were not already in prevote delay.
		if rlc.S == tsi.StepAwaitingPrevotes {
			rlc.S = tsi.StepPrevoteDelay
			rlc.StepTimer, rlc.CancelTimer = m.rt.PrevoteDelayTimer(ctx, rlc.H, rlc.R)
		}
	}
}

func (m *StateMachine) recordPrevote(
	ctx context.Context,
	rlc *tsi.RoundLifecycle,
	targetHash string,
) (ok bool) {
	if m.signer != nil {
		// Record to the action store first.
		h, r := rlc.H, rlc.R
		vt := tmconsensus.VoteTarget{
			Height: h, Round: r,
			BlockHash: targetHash,
		}
		signContent, err := tmconsensus.PrevoteSignBytes(vt, m.sigScheme)
		if err != nil {
			glog.HRE(m.log, h, r, err).Error(
				"Failed to generate prevote sign bytes",
				"target_hash", glog.Hex(targetHash),
			)
			return false
		}

		sig, err := m.signer.Sign(ctx, signContent)
		if err != nil {
			glog.HRE(m.log, h, r, err).Error(
				"Failed to sign prevote content",
				"target_hash", glog.Hex(targetHash),
			)
			return false
		}

		if err := m.aStore.SavePrevote(ctx, m.signer.PubKey(), vt, sig); err != nil {
			glog.HRE(m.log, h, r, err).Error("Failed to save prevote to action store")
			return false
		}

		// The OutgoingActionsCh is 3-buffered so we assume this will never block.
		rlc.OutgoingActionsCh <- tmeil.StateMachineRoundAction{
			Prevote: tmeil.ScopedSignature{
				TargetHash:  targetHash,
				SignContent: signContent,
				Sig:         sig,
			},
		}
	}

	// Finally, if we were waiting for proposed blocks and we submitted our own prevote,
	// then we can advance to the next step.
	if rlc.S == tsi.StepAwaitingProposal {
		rlc.S = tsi.StepAwaitingPrevotes
		rlc.CancelTimer()
		rlc.CancelTimer = nil
		rlc.StepTimer = nil
	}

	return true
}

func (m *StateMachine) handlePrecommitViewUpdate(
	ctx context.Context,
	rlc *tsi.RoundLifecycle,
	vrv tmconsensus.VersionedRoundView,
) {
	defer trace.StartRegion(ctx, "handlePrecommitViewUpdate").End()

	vs := vrv.VoteSummary
	maj := tmconsensus.ByzantineMajority(vs.AvailablePower)
	if vs.TotalPrecommitPower >= maj {
		// We have majority vote power present;
		// do we have majority vote power on a single block?
		maxPow := vs.PrecommitBlockPower[vs.MostVotedPrecommitHash]
		if maxPow >= maj {
			if vs.MostVotedPrecommitHash == "" {
				_ = m.advanceRound(ctx, rlc)
				return
			}

			if rlc.S == tsi.StepPrecommitDelay {
				rlc.CancelTimer()
				rlc.StepTimer = nil
				rlc.CancelTimer = nil
			}

			_ = m.beginCommit(ctx, rlc, vrv)
			return
		}

		if vs.TotalPrecommitPower == vs.AvailablePower {
			// Reached 100% precommits but didn't reach consensus on a single block or nil.
			_ = m.advanceRound(ctx, rlc)
			return
		}

		// We have majority precommits but not on a single block.
		// Only start the timer if we were not already in precommit delay.
		if rlc.S == tsi.StepAwaitingPrecommits {
			rlc.S = tsi.StepPrecommitDelay
			rlc.StepTimer, rlc.CancelTimer = m.rt.PrecommitDelayTimer(ctx, rlc.H, rlc.R)
		}
	}
}

func (m *StateMachine) recordPrecommit(
	ctx context.Context,
	rlc *tsi.RoundLifecycle,
	targetHash string,
) (ok bool) {
	if m.signer == nil {
		return true
	}

	// Record to the action store first.
	h, r := rlc.H, rlc.R
	vt := tmconsensus.VoteTarget{
		Height: h, Round: r,
		BlockHash: targetHash,
	}
	signContent, err := tmconsensus.PrecommitSignBytes(vt, m.sigScheme)
	if err != nil {
		glog.HRE(m.log, h, r, err).Error(
			"Failed to generate precommit sign bytes",
			"target_hash", glog.Hex(targetHash),
		)
		return false
	}

	sig, err := m.signer.Sign(ctx, signContent)
	if err != nil {
		glog.HRE(m.log, h, r, err).Error(
			"Failed to sign precommit content",
			"target_hash", glog.Hex(targetHash),
		)
		return false
	}

	if err := m.aStore.SavePrecommit(ctx, m.signer.PubKey(), vt, sig); err != nil {
		glog.HRE(m.log, h, r, err).Error("Failed to save precommit to action store")
		return false
	}

	// The OutgoingActionsCh is 3-buffered so we assume this will never block.
	rlc.OutgoingActionsCh <- tmeil.StateMachineRoundAction{
		Precommit: tmeil.ScopedSignature{
			TargetHash:  targetHash,
			SignContent: signContent,
			Sig:         sig,
		},
	}

	return true
}

func (m *StateMachine) handleCommitWaitViewUpdate(
	ctx context.Context,
	rlc *tsi.RoundLifecycle,
	vrv tmconsensus.VersionedRoundView,
) {
	// Currently, the only action we may take here is creating a finalization request
	// if we lacked the proposed block before.
	// We don't currently have a way to notify an in-progress finalization request of anything.

	if rlc.FinalizeRespCh == nil {
		// The finalization has already completed,
		// so there is nothing to do.
		return
	}

	pbIdx := slices.IndexFunc(rlc.PrevVRV.ProposedBlocks, func(pb tmconsensus.ProposedBlock) bool {
		return string(pb.Block.Hash) == rlc.PrevVRV.VoteSummary.MostVotedPrecommitHash
	})
	if pbIdx >= 0 {
		// The previous VRV already had the proposed block,
		// so we can assume we already made the finalization request.
		return
	}

	// Now does the new VRV have the proposed block?
	pbIdx = slices.IndexFunc(vrv.ProposedBlocks, func(pb tmconsensus.ProposedBlock) bool {
		return string(pb.Block.Hash) == vrv.VoteSummary.MostVotedPrecommitHash
	})
	if pbIdx < 0 {
		// Not there yet, so don't do anything.
		return
	}

	// We have a valid index, so we can make the finalization request now.
	_ = gchan.SendC(
		ctx, m.log,
		m.finalizeBlockRequestCh, tmdriver.FinalizeBlockRequest{
			Block: vrv.ProposedBlocks[pbIdx].Block,
			Round: vrv.Round,

			Resp: rlc.FinalizeRespCh,
		},
		"making finalize block request from handleCommitWaitViewUpdate",
	)
}

func (m *StateMachine) recordProposedBlock(
	ctx context.Context,
	rlc tsi.RoundLifecycle,
	p tmconsensus.Proposal,
) (ok bool) {
	h, r := rlc.H, rlc.R

	pb := tmconsensus.ProposedBlock{
		Block: tmconsensus.Block{
			PrevBlockHash: []byte(rlc.PrevBlockHash),

			Height: h,

			// TODO: previous commit proof. This should come from the mirror.

			Validators:     slices.Clone(rlc.CurVals),
			NextValidators: slices.Clone(rlc.PrevFinNextVals),

			DataID: []byte(p.DataID),

			PrevAppStateHash: []byte(rlc.PrevFinAppStateHash),

			Annotations: p.BlockAnnotations,
		},

		Round: r,

		ProposerPubKey: m.signer.PubKey(),

		Annotations: p.ProposalAnnotations,
	}

	hash, err := m.hashScheme.Block(pb.Block)
	if err != nil {
		glog.HRE(m.log, h, r, err).Error("Failed to calculate hash for proposed block")
		return false
	}
	pb.Block.Hash = hash

	signContent, err := tmconsensus.ProposalSignBytes(pb.Block, r, pb.Annotations, m.sigScheme)
	if err != nil {
		glog.HRE(m.log, h, r, err).Error("Failed to produce signing content for proposed block")
		return false
	}
	sig, err := m.signer.Sign(ctx, signContent)
	if err != nil {
		glog.HRE(m.log, h, r, err).Error("Failed to sign proposed block")
		return false
	}
	pb.Signature = sig

	if err := m.aStore.SaveProposedBlock(ctx, pb); err != nil {
		glog.HRE(m.log, h, r, err).Error("Failed to save proposed block to action store")
		return false
	}

	// The OutgoingActionsCh is 3-buffered so we assume this will never block.
	rlc.OutgoingActionsCh <- tmeil.StateMachineRoundAction{
		PB: pb,
	}

	return true
}

// beginCommit emits a FinalizeBlockRequest for the most voted precommit hash.
func (m *StateMachine) beginCommit(
	ctx context.Context,
	rlc *tsi.RoundLifecycle,
	vrv tmconsensus.VersionedRoundView,
) (ok bool) {
	defer trace.StartRegion(ctx, "beginCommit").End()

	rlc.S = tsi.StepCommitWait
	rlc.StepTimer, rlc.CancelTimer = m.rt.CommitWaitTimer(ctx, rlc.H, rlc.R)

	idx := slices.IndexFunc(vrv.ProposedBlocks, func(pb tmconsensus.ProposedBlock) bool {
		return string(pb.Block.Hash) == vrv.VoteSummary.MostVotedPrecommitHash
	})
	if idx < 0 {
		m.log.Warn(
			"Reached commit wait step but don't have the proposed block to commit yet",
			"height", rlc.H,
			"round", rlc.R,
			"committing_hash", glog.Hex(vrv.VoteSummary.MostVotedPrecommitHash),
		)
		return
	}

	return gchan.SendC(
		ctx, m.log,
		m.finalizeBlockRequestCh, tmdriver.FinalizeBlockRequest{
			Block: vrv.ProposedBlocks[idx].Block,
			Round: vrv.Round,

			Resp: rlc.FinalizeRespCh,
		},
		"making finalize block request from beginCommit",
	)
}

func (m *StateMachine) handleFinalization(
	ctx context.Context,
	rlc *tsi.RoundLifecycle,
	resp tmdriver.FinalizeBlockResponse,
) (ok bool) {
	if len(resp.Validators) == 0 {
		panic(fmt.Errorf(
			"BUG: application did not set validators in finalization response (height=%d round=%d block_hash=%x)",
			resp.Height, resp.Round, resp.BlockHash,
		))
	}

	clear(rlc.FinalizedValidators)
	rlc.FinalizedValidators = append(rlc.FinalizedValidators, resp.Validators...)
	rlc.FinalizedAppStateHash = string(resp.AppStateHash)

	rlc.FinalizeRespCh = nil

	if resp.Height != rlc.H || resp.Round != rlc.R {
		panic(fmt.Errorf(
			"BUG: app sent height/round %d/%d differing from current (%d/%d)",
			resp.Height, resp.Round, rlc.H, rlc.R,
		))
	}

	if err := m.fStore.SaveFinalization(
		ctx,
		rlc.H, rlc.R,
		string(resp.BlockHash),
		rlc.CurVals,
		string(resp.AppStateHash),
	); err != nil {
		glog.HRE(m.log, rlc.H, rlc.R, err).Error(
			"Failed to save finalization to Finalization Store",
		)
		return false
	}

	// The step is AwaitingFinalization if the commit wait timer has already elapsed.
	if rlc.S == tsi.StepAwaitingFinalization {
		if !m.advanceHeight(ctx, rlc) {
			return false
		}
	}

	return true
}

func (m *StateMachine) handleTimerElapsed(ctx context.Context, rlc *tsi.RoundLifecycle) (ok bool) {
	defer trace.StartRegion(ctx, "handleTimerElapsed").End()

	switch rlc.S {
	case tsi.StepAwaitingProposal:
		if !gchan.SendC(
			ctx, m.log,
			m.cm.ChooseProposedBlockRequests, tsi.ChooseProposedBlockRequest{
				// Exclude invalid proposed blocks.
				PBs: rejectMismatchedProposedBlocks(
					rlc.PrevVRV.ProposedBlocks,
					rlc.PrevFinAppStateHash,
					rlc.CurVals,
					rlc.PrevFinNextVals,
				),
				Result: rlc.PrevoteHashCh, // Is it ever possible this channel is nil?
			},
			"choosing proposed block following proposal timeout",
		) {
			// Context cancelled and logged. Quit.
			return false
		}

		// Don't need to hold on to any of the previously sent hashes
		// after sending the choose request.
		clear(rlc.PrevConsideredHashes)

		// Move on to awaiting prevotes.
		rlc.S = tsi.StepAwaitingPrevotes

		// Call cancel anyway as a matter of cleanup.
		rlc.CancelTimer()
		rlc.StepTimer = nil
		rlc.CancelTimer = nil

	case tsi.StepPrevoteDelay:
		if !gchan.SendC(
			ctx, m.log,
			m.cm.DecidePrecommitRequests, tsi.DecidePrecommitRequest{
				VS:     rlc.PrevVRV.VoteSummary.Clone(), // Clone under assumption to avoid data race.
				Result: rlc.PrecommitHashCh,             // Is it ever possible this channel is nil?
			},
			"choosing precommit following prevote delay timeout",
		) {
			// Context cancelled and logged. Quit.
			return false
		}

		// Move on to awaiting precommits.
		rlc.S = tsi.StepAwaitingPrecommits

		rlc.CancelTimer()
		rlc.StepTimer = nil
		rlc.CancelTimer = nil

	case tsi.StepPrecommitDelay:
		rlc.CancelTimer()
		rlc.StepTimer = nil
		rlc.CancelTimer = nil

		if !m.advanceRound(ctx, rlc) {
			return false
		}

	case tsi.StepCommitWait:
		rlc.CommitWaitElapsed = true

		rlc.CancelTimer()
		rlc.StepTimer = nil
		rlc.CancelTimer = nil

		if len(rlc.FinalizedValidators) == 0 {
			// The timer has elapsed but we don't have a finalization yet.
			rlc.S = tsi.StepAwaitingFinalization
			return true
		}

		// We already finalized and now the commit wait has elapsed,
		// so now we have to advance the height.
		if !m.advanceHeight(ctx, rlc) {
			return false
		}

	default:
		panic(fmt.Errorf("BUG: unhandled timer elapse for step %s", rlc.S))
	}

	return true
}

// handleBlockDataArrival is called from the kernel when a new block data arrival value is received.
//
// It filters the incoming arrival, and then scans m.BlockDataArrivalCh for any other queued values.
// If any of those match a proposed block we already had,
// it makes a new call to consider the proposed blocks, setting the reason as appropriate.
func (m *StateMachine) handleBlockDataArrival(ctx context.Context, rlc *tsi.RoundLifecycle, a tmelink.BlockDataArrival) (ok bool) {
	defer trace.StartRegion(ctx, "handleBlockDataArrival").End()

	// We've already submitted a prevote for this round,
	// so just ignore the arrival notification.
	if rlc.PrevoteHashCh == nil {
		return true
	}

	// If the prevote hash channel is not nil,
	// then confirm that the arrived data matches rlc.
	if a.Height != rlc.H || a.Round != rlc.R {
		return true
	}

	// The height and round match, and we are able to prevote,
	// so now we need to construct the consider block request.
	okPBs := rejectMismatchedProposedBlocks(
		rlc.PrevVRV.ProposedBlocks,
		rlc.PrevFinAppStateHash,
		rlc.CurVals,
		rlc.PrevFinNextVals,
	)
	if len(okPBs) == 0 {
		return true
	}

	req := tsi.ConsiderProposedBlocksRequest{
		PBs:    okPBs,
		Result: rlc.PrevoteHashCh,
	}

	// We don't call req.MarkReasonNewHashes here, because we did not receive new hashes.
	// But we do need to construct the slice of updated block IDs.
	// Gather any other incoming data arrivals.
	dataIDMap := make(map[string]struct{}, 1+len(m.blockDataArrivalCh))
	dataIDMap[a.ID] = struct{}{}
GATHER_ARRIVALS:
	for {
		select {
		case x := <-m.blockDataArrivalCh:
			// Another arrival is queued.
			// Include it if it matches the height and round.
			if x.Height != a.Height || x.Round != a.Round {
				continue GATHER_ARRIVALS
			}
			dataIDMap[x.ID] = struct{}{}
		case <-ctx.Done():
			m.log.Info(
				"Quitting due to context cancellation while gathering block data arrivals",
				"cause", context.Cause(ctx),
			)
			return false
		default:
			// Nothing left on the channel.
			break GATHER_ARRIVALS
		}
	}

	// We have a list of data IDs that have arrived.
	// Exclude any that do not map to the proposed blocks we are re-checking.
	req.Reason.UpdatedBlockDataIDs = make([]string, 0, max(len(req.PBs), len(dataIDMap)))
	for _, pb := range req.PBs {
		_, dataArrived := dataIDMap[string(pb.Block.DataID)]
		if !dataArrived {
			continue
		}

		req.Reason.UpdatedBlockDataIDs = append(req.Reason.UpdatedBlockDataIDs, string(pb.Block.DataID))
	}

	if len(req.Reason.UpdatedBlockDataIDs) == 0 {
		// We had IDs arrive, but nothing matched the proposed blocks we have.
		return true
	}

	// We may have overallocated capacity, so trim it off to help GC.
	req.Reason.UpdatedBlockDataIDs = slices.Clip(req.Reason.UpdatedBlockDataIDs)

	// Now we can finally make the request.
	if !gchan.SendC(
		ctx, m.log,
		m.cm.ConsiderProposedBlocksRequests, req,
		"making consider proposed blocks request following block data arrival",
	) {
		// Context cancelled and logged. Quit.
		return false
	}

	return true
}

func (m *StateMachine) advanceHeight(ctx context.Context, rlc *tsi.RoundLifecycle) (ok bool) {
	rlc.CycleFinalization()
	rlc.Reset(ctx, rlc.H+1, 0)

	re := tmeil.StateMachineRoundEntrance{
		H: rlc.H,
		R: 0,

		Response: make(chan tmeil.RoundEntranceResponse, 1),
	}

	if m.signer != nil {
		re.PubKey = m.signer.PubKey()
		re.Actions = make(chan tmeil.StateMachineRoundAction, 3)
	}

	update, ok := gchan.ReqResp(
		ctx, m.log,
		m.roundEntranceOutCh, re,
		re.Response,
		"seeding initial state from mirror",
	)
	if !ok {
		return false
	}

	// This is similar to the handling in sendInitialActionSet,
	// but it differs because at this point it is impossible for us to be on the initial height;
	// and the rlc has some state we are reusing through the earlier call to rlc.CycleFinalization.
	if update.IsVRV() {
		rlc.OutgoingActionsCh = re.Actions // Should this be part of the Reset method instead?

		// We have to synchronously enter the round,
		// but we still enter through the consensus manager for this.
		req := tsi.EnterRoundRequest{
			RV:     update.VRV.RoundView,
			Result: make(chan error), // Unbuffered since both sides sync on this.

			ProposalOut: rlc.ProposalCh,
		}

		err, ok := gchan.ReqResp(
			ctx, m.log,
			m.cm.EnterRoundRequests, req,
			req.Result,
			"entering round on consensus strategy while advancing height",
		)
		if !ok {
			// Context cancelled, we cannot continue.
			return false
		}
		if err != nil {
			panic(fmt.Errorf(
				"FATAL: error when calling ConsensusStrategy.EnterRound while advancing height: %v", err,
			))
		}

		if !m.beginRoundLive(ctx, rlc, update.VRV) {
			return false
		}
	} else {
		// This case could happen if we took more than an entire block to finalize,
		// due to an app bug or whatever reason.
		panic("TODO: handle replayed block upon finalization")
	}

	return true
}

func (m *StateMachine) advanceRound(ctx context.Context, rlc *tsi.RoundLifecycle) (ok bool) {
	// TODO: do we need to do anything with the finalizations?
	rlc.Reset(ctx, rlc.H, rlc.R+1)

	re := tmeil.StateMachineRoundEntrance{
		H: rlc.H,
		R: rlc.R,

		Response: make(chan tmeil.RoundEntranceResponse, 1),
	}

	if m.signer != nil {
		re.PubKey = m.signer.PubKey()
		re.Actions = make(chan tmeil.StateMachineRoundAction, 3)
	}

	update, ok := gchan.ReqResp(
		ctx, m.log,
		m.roundEntranceOutCh, re,
		re.Response,
		"seeding initial round state from mirror",
	)
	if !ok {
		return false
	}
	// Closing the outgoing Response channel is not strictly necessary,
	// but it is a little helpful in tests in case a StateMachineRoundEntrance is mistakenly reused.
	close(re.Response)

	if update.IsVRV() {
		// TODO: this is probably missing some setup to handle initial height.

		rlc.OutgoingActionsCh = re.Actions // Should this be part of the Reset method instead?

		// We have to synchronously enter the round,
		// but we still enter through the consensus manager for this.
		req := tsi.EnterRoundRequest{
			RV:     update.VRV.RoundView,
			Result: make(chan error), // Unbuffered since both sides sync on this.

			ProposalOut: rlc.ProposalCh,
		}

		err, ok := gchan.ReqResp(
			ctx, m.log,
			m.cm.EnterRoundRequests, req,
			req.Result,
			"entering round on consensus strategy while advancing height",
		)
		if !ok {
			// Context cancelled, we cannot continue.
			return false
		}
		if err != nil {
			panic(fmt.Errorf(
				"FATAL: error when calling ConsensusStrategy.EnterRound while advancing height: %v", err,
			))
		}

		if !m.beginRoundLive(ctx, rlc, update.VRV) {
			return false
		}
	} else {
		// This case could happen if we took more than an entire block to finalize,
		// due to an app bug or whatever reason.
		panic("TODO: handle replayed block upon advancing round")
	}

	return true
}

func (m *StateMachine) handleJumpAhead(
	ctx context.Context,
	rlc *tsi.RoundLifecycle,
	vrv tmconsensus.VersionedRoundView,
) {
	if vrv.Height != rlc.H {
		panic(fmt.Errorf(
			"BUG: attempted to jump ahead to height %d when on height %d",
			vrv.Height, rlc.H,
		))
	}

	if vrv.Round <= rlc.R {
		panic(fmt.Errorf(
			"BUG: attempted to jump ahead to round %d when on height %d, round %d",
			vrv.Round, rlc.H, rlc.R,
		))
	}

	// It's a valid round-forward move.
	oldRound := rlc.R
	_ = m.advanceRound(ctx, rlc)
	m.log.Info(
		"Jumped ahead following signal from mirror",
		"height", rlc.H,
		"old_round", oldRound, "new_round", rlc.R,
	)
}
