package tmstate

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/trace"
	"slices"
	"time"

	"github.com/gordian-engine/gordian/gassert"
	"github.com/gordian-engine/gordian/gwatchdog"
	"github.com/gordian-engine/gordian/internal/gchan"
	"github.com/gordian-engine/gordian/internal/glog"
	"github.com/gordian-engine/gordian/tm/tmconsensus"
	"github.com/gordian-engine/gordian/tm/tmdriver"
	"github.com/gordian-engine/gordian/tm/tmengine/internal/tmeil"
	"github.com/gordian-engine/gordian/tm/tmengine/internal/tmemetrics"
	"github.com/gordian-engine/gordian/tm/tmengine/internal/tmstate/internal/tsi"
	"github.com/gordian-engine/gordian/tm/tmengine/tmelink"
	"github.com/gordian-engine/gordian/tm/tmstore"
)

type StateMachine struct {
	log *slog.Logger

	signer tmconsensus.Signer

	hashScheme tmconsensus.HashScheme

	genesis tmconsensus.Genesis

	aStore  tmstore.ActionStore
	fStore  tmstore.FinalizationStore
	smStore tmstore.StateMachineStore

	rt RoundTimer

	cm *tsi.ConsensusManager

	mc *tmemetrics.Collector

	wd *gwatchdog.Watchdog

	viewInCh               <-chan tmeil.StateMachineRoundView
	roundEntranceOutCh     chan<- tmeil.StateMachineRoundEntrance
	finalizeBlockRequestCh chan<- tmdriver.FinalizeBlockRequest
	blockDataArrivalCh     <-chan tmelink.BlockDataArrival

	assertEnv gassert.Env

	kernelDone chan struct{}
}

type StateMachineConfig struct {
	Signer tmconsensus.Signer

	HashScheme tmconsensus.HashScheme

	Genesis tmconsensus.Genesis

	ActionStore       tmstore.ActionStore
	FinalizationStore tmstore.FinalizationStore
	StateMachineStore tmstore.StateMachineStore

	RoundTimer RoundTimer

	ConsensusStrategy tmconsensus.ConsensusStrategy

	RoundViewInCh      <-chan tmeil.StateMachineRoundView
	RoundEntranceOutCh chan<- tmeil.StateMachineRoundEntrance

	BlockDataArrivalCh <-chan tmelink.BlockDataArrival

	FinalizeBlockRequestCh chan<- tmdriver.FinalizeBlockRequest

	MetricsCollector *tmemetrics.Collector

	Watchdog *gwatchdog.Watchdog

	AssertEnv gassert.Env
}

func NewStateMachine(ctx context.Context, log *slog.Logger, cfg StateMachineConfig) (*StateMachine, error) {
	m := &StateMachine{
		log: log,

		signer: cfg.Signer,

		hashScheme: cfg.HashScheme,

		genesis: cfg.Genesis,

		aStore:  cfg.ActionStore,
		fStore:  cfg.FinalizationStore,
		smStore: cfg.StateMachineStore,

		rt: cfg.RoundTimer,

		cm: tsi.NewConsensusManager(ctx, log.With("sm_sys", "consmgr"), cfg.ConsensusStrategy),

		mc: cfg.MetricsCollector,

		wd: cfg.Watchdog,

		assertEnv: cfg.AssertEnv,

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

				slog.Any("VRV", rlc.VRV),
			),
		)
	}()

	for {
		if rlc.IsReplaying() {
			if !m.handleCatchupEvent(ctx, wSig, &rlc) {
				return
			}
		} else {
			if !m.handleLiveEvent(ctx, wSig, &rlc) {
				return
			}
		}
	}
}

func (m *StateMachine) handleCatchupEvent(
	ctx context.Context,
	wSig <-chan gwatchdog.Signal,
	rlc *tsi.RoundLifecycle,
) (ok bool) {
	defer trace.StartRegion(ctx, "handleCatchupEvent").End()

	// Handle the minimal set of events that can happen during catchup.
	// While at this height, we are in replay at least until we enter the next round.

	for {
		select {
		case <-ctx.Done():
			m.log.Info(
				"State machine kernel quitting due to context cancellation in main loop (catchup)",
				"cause", context.Cause(ctx),
				"height", rlc.H, "round", rlc.R, "step", rlc.S,
			)
			return false

		case resp := <-rlc.FinalizeRespCh:
			// During a replay, we are blocked waiting for a finalization.

			// The RLC step is kind of meaningless during replay,
			// but handleFinalization expects the step to be awaiting finalization
			// in order to advance to the next height,
			// so we just fake it here.
			rlc.S = tsi.StepAwaitingFinalization
			if !m.handleFinalization(ctx, rlc, resp) {
				return false
			}
		}
	}
}

func (m *StateMachine) handleLiveEvent(
	ctx context.Context,
	wSig <-chan gwatchdog.Signal,
	rlc *tsi.RoundLifecycle,
) (ok bool) {
	defer trace.StartRegion(ctx, "handleLiveEvent").End()

	// TODO: we can increase throughput in handling live events
	// by refactoring the channels interfacing with the consensus manager,
	// and accumulating buffered values to send to the consensus manager
	// any time a send is blocked.
	// But, that will add complexity to the handling here.

	select {
	case <-ctx.Done():
		m.log.Info(
			"State machine kernel quitting due to context cancellation in main loop (live events)",
			"cause", context.Cause(ctx),
			"height", rlc.H, "round", rlc.R, "step", rlc.S,
			"vote_summary", rlc.VRV.VoteSummary,
		)
		return false

	case v := <-m.viewInCh:
		m.handleViewUpdate(ctx, rlc, v)

	case p := <-rlc.ProposalCh:
		if !m.recordProposedHeader(ctx, *rlc, p) {
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

	case <-rlc.HeightCommitted:
		if !m.handleHeightCommitted(ctx, rlc) {
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

// handleHeightCommitted is called when the mirror sends a HeightCommitted signal.
// Essentially we treat that the same as a commit wait timer elapse.
func (m *StateMachine) handleHeightCommitted(ctx context.Context, rlc *tsi.RoundLifecycle) (ok bool) {
	// Don't read from the channel again, especially since it's closed.
	rlc.HeightCommitted = nil

	rlc.CommitWaitElapsed = true

	if rlc.CancelTimer != nil {
		rlc.CancelTimer()
	}
	rlc.StepTimer = nil
	rlc.CancelTimer = nil

	if rlc.S == tsi.StepAwaitingFinalization {
		// We were already awaiting finalization,
		// so nothing to do here.
		return true
	}

	if rlc.S != tsi.StepCommitWait {
		// It's probably also acceptable to be on tsi.StepAwaitingFinalization?
		panic(fmt.Errorf(
			"BUG: expected to be on step commit wait, got %s", rlc.S,
		))
	}

	if len(rlc.FinalizedValSet.Validators) == 0 {
		// We don't have a finalization yet,
		// so that's the step we are waiting on.
		rlc.S = tsi.StepAwaitingFinalization
		return true
	}

	// We already had a finalization and now the commit wait has effectively elapsed,
	// so now we have to advance the height.
	return m.advanceHeight(ctx, rlc)
}

func (m *StateMachine) initializeRLC(ctx context.Context) (rlc tsi.RoundLifecycle, ok bool) {
	rlc, su, ok := m.sendInitialActionSet(ctx)
	if !ok {
		// Failure. Assume it was logged.
		return rlc, false
	}

	// TODO: this should be loading from the action store and re-sending any matching actions.
	// This would cover an unlikely instance where:
	//   1. The state machine recorded its action.
	//   2. The process crashed before broadcasting the action.
	//   3. The process immediately restarted at the same height/round/step.
	//   4. The mirror could request the same action again, which could potentially cause
	//      a double sign, or perhaps another crash due to an attempt at a duplicate action.

	if !su.IsVRV() {
		// We are replaying, so we don't need special begin round handling.
		// sendInitialActionSet could already made a finalization request.
		return rlc, true
	}

	// But, we do need to make sure we suppress an unnecessary attempt
	// to propose a header.
	if m.signer != nil {
		// Did the mirror already receive a proposed header from us?
		// We check this first so we don't have to consult a store.
		for _, ph := range su.VRV.ProposedHeaders {
			if m.signer.PubKey().Equal(ph.ProposerPubKey) {
				// Now if the consensus strategy is consulted,
				// it won't have an outgoing proposal channel for this round.
				// TODO: this is missing a test
				rlc.ProposalCh = nil
				break
			}
		}

		// It is remotely possible, at startup,
		// that we have recorded a proposed header to the action store
		// but did not successfully send it to the mirror.
		if rlc.ProposalCh != nil {
			ra, err := m.aStore.LoadActions(ctx, rlc.H, rlc.R)
			if err != nil && !errors.Is(err, tmconsensus.RoundUnknownError{
				WantHeight: rlc.H,
				WantRound:  rlc.R,
			}) {
				m.log.Error(
					"Failed to load existing actions during startup",
					"height", rlc.H,
					"round", rlc.R,
					"err", err,
				)
				return rlc, false
			}

			if ra.ProposedHeader.Header.Height != 0 {
				// We had a header in our recorded actions,
				// but it wasn't part of the round view that the mirror sent us.
				rlc.ProposalCh = nil
				if !gchan.SendC(
					ctx, m.log,
					rlc.OutgoingActionsCh, tmeil.StateMachineRoundAction{
						PH: ra.ProposedHeader,
					},
					"sending previously recorded proposed header to mirror",
				) {
					return rlc, false
				}

				su.VRV.ProposedHeaders = append(su.VRV.ProposedHeaders, ra.ProposedHeader)
			}
		}
	}

	// The state update was a VRV.
	// We need to send the enter round request to the consensus strategy,
	// now that we have potentially modified the proposal out channel.
	req := tsi.EnterRoundRequest{
		RV:     su.VRV.RoundView,
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
		return rlc, false
	}
	if err != nil {
		m.log.Error(
			"Error when calling ConsensusStrategy.EnterRound",
			"err", err,
		)
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
		if okPHs := m.rejectMismatchedProposedHeaders(initVRV.ProposedHeaders, rlc); len(okPHs) > 0 {
			req := tsi.ConsiderProposedBlocksRequest{
				PHs:    okPHs,
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
		rlc.VRV = &initVRV
		return true

	default:
		panic(fmt.Errorf("BUG: unhandled initial step %s", curStep))
	}

	rlc.S = curStep
	rlc.VRV = &initVRV

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
	// We send the initial round entrance before we reset the RLC,
	// so only in this case do we make the HeightCommitted channel out of band,
	// and then re-assign it into rlc.
	// (In all other cases, we rely on rlc.Reset to create the channel.)
	hc := make(chan struct{})

	h, r, err := m.smStore.StateMachineHeightRound(ctx)
	if err != nil {
		if err == tmstore.ErrStoreUninitialized {
			// This is fine -- it's the first run ever.
			// Set the height and round here, but there is no point in saving them
			// until we actually switch rounds.
			// If we were to crash before the next round,
			// we would still be at the genesis height anyway.
			h = m.genesis.InitialHeight
			r = 0
		} else {
			m.log.Error("Failed to get state machine height and round from store", "err", err)
			return rlc, rer, false
		}
	}

	// Before we inform the mirror that we are at a given height and round,
	// check if we have already stored a finalization for this height.
	// This could have happened if we applied the finalization,
	// were in the commit wait timeout,
	// and then the process ended.
	//
	// During normal flow, this is a possible but rare event.
	// We will simply assume that the commit wait elapsed while we were offline.
	// At worst, we propose our block early,
	// but the other validators in the network need to be resilient to that anyway.
	if _, _, _, _, err := m.fStore.LoadFinalizationByHeight(ctx, h); err == nil {
		h++
		r = 0

		// We do have a finalization, so we are entering h+1 instead.
		// Log at warning level because this is unusual but probably not problematic.
		m.log.Warn(
			"During initialization, store reported height that contained finalization",
			"new_height", h,
		)
	}

	initRE := tmeil.StateMachineRoundEntrance{
		H: h, R: r,

		HeightCommitted: hc,

		Response: make(chan tmeil.RoundEntranceResponse, 1),
	}
	// We always send the pubkey if we have one.
	if m.signer != nil {
		initRE.PubKey = m.signer.PubKey()
	}

	isGenesis := h == m.genesis.InitialHeight && r == 0

	// And we only populate the actions channel if we are participating,
	// i.e. we are a validator in the current set.
	// Although we set the rest of the rlc values later,
	// we need the current validator set now to determine participation.
	if isGenesis {
		rlc.CurValSet = m.genesis.ValidatorSet
		rlc.PrevValSet = m.genesis.ValidatorSet
	} else {
		// If we are past genesis,
		// it should be safe to assume we have a finalization for two heights back.
		_, _, rlc.CurValSet, _, err = m.fStore.LoadFinalizationByHeight(ctx, h-2)
		if err != nil {
			m.log.Error(
				"Failed to load finalization for current validator set",
				"load_height", h-2,
				"err", err,
			)
			return rlc, rer, false
		}

		if h-3 > m.genesis.InitialHeight {
			// If the current validator set was declared at h-2,
			// then the previous validator set must have been declared at h-3.
			// If we don't have that finalization then we need to fall back to genesis.
			_, _, rlc.PrevValSet, _, err = m.fStore.LoadFinalizationByHeight(ctx, h-3)
			if err != nil {
				m.log.Error(
					"Failed to load finalization for previous validator set",
					"load_height", h-3,
					"err", err,
				)
				return rlc, rer, false
			}
		} else {
			rlc.PrevValSet = m.genesis.ValidatorSet
		}

	}
	if m.isParticipating(&rlc) {
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

	rlc.AssertEnv = m.assertEnv

	// Initialize this to a default size;
	// it needs to be a non-nil map regardless of the initial update.
	rlc.PrevConsideredHashes = map[string]struct{}{}

	// We have a response -- do we need to call into the consensus strategy,
	// or do we only need to replay the block?
	if rer.IsVRV() {
		rlc.Reset(ctx, initRE.H, initRE.R)
		rlc.HeightCommitted = hc
		rlc.OutgoingActionsCh = initRE.Actions // Should this be part of the Reset method instead?

		if isGenesis {
			// Assuming it's safe to take the reference of the genesis validators.
			rlc.PrevFinNextValSet = m.genesis.ValidatorSet
			rlc.PrevFinAppStateHash = string(m.genesis.CurrentAppStateHash)

			// For now, set the previous block hash as the genesis pseudo-block's hash.
			// But maybe it would be better if the mirror generated this
			// and sent it as part of the state update.
			b, err := m.genesis.Header(m.hashScheme)
			if err != nil {
				panic(fmt.Errorf(
					"FATAL: failed to generate genesis block hash: %w", err,
				))
			}
			rlc.PrevBlockHash = string(b.Hash)
		} else {
			// TODO: this path does not yet have unit test coverage,
			// only gcosmos integration test coverage as of writing.
			_, rlc.PrevBlockHash, rlc.PrevFinNextValSet, rlc.PrevFinAppStateHash, err =
				m.fStore.LoadFinalizationByHeight(ctx, h-1)
			if err != nil {
				m.log.Error(
					"Failed to load finalization when initializing round lifecycle",
					"finalization_height", h,
					"err", err,
				)
				return rlc, rer, false
			}

			vrvClone := rer.VRV.Clone()
			rlc.VRV = &vrvClone
		}

		// Overwrite the proposed blocks we present to the consensus strategy,
		// to exclude any known invalid blocks according to the genesis we just parsed.
		rer.VRV.RoundView.ProposedHeaders = m.rejectMismatchedProposedHeaders(
			rer.VRV.RoundView.ProposedHeaders, &rlc,
		)

		ok = true
	} else {
		// TODO: there should be a different method for resetting
		// in expectation of handling a replayed block.
		rlc.Reset(ctx, initRE.H, initRE.R)
		rlc.HeightCommitted = hc

		// This is a replay, so we can just tell the driver to finalize it.
		finReq := tmdriver.FinalizeBlockRequest{
			Header: rer.CH.Header,
			Round:  rer.CH.Proof.Round,

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

// handleViewUpdate updates the state machine
// in response to an updated view from the mirror.
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

	if vrv.Version <= rlc.VRV.Version {
		m.log.Warn(
			"Received non-increasing round view version; this is a likely bug",
			"h", rlc.H, "r", rlc.R, "step", rlc.S,
			"prev_version", rlc.VRV.Version, "cur_version", vrv.Version,
		)

		m.log.Info(
			"STATE DUMP DUE TO STALE UPDATE BUG",
			"h", rlc.H, "r", rlc.R, "step", rlc.S,
			"vrv", rlc.VRV,
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

	if vrv.Height == rlc.VRV.Height && vrv.Round == rlc.VRV.Round {
		// If the view update caused a nil commit,
		// the incoming vrv's height and round will differ from the set VRV.
		// So we have to check this in order to avoid "moving backwards".

		// Assuming it's okay to take ownership of vrv as opposed to copying in to our value.
		rlc.VRV = &vrv
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
			PHs: m.rejectMismatchedProposedHeaders(vrv.ProposedHeaders, rlc),

			Result: rlc.PrevoteHashCh,
		}

		vs := vrv.VoteSummary
		maxBlockPow := vs.PrevoteBlockPower[vs.MostVotedPrevoteHash]
		if maxBlockPow >= maj {
			// If the majority power is at consensus, we submit our prevote immediately.
			rlc.S = tsi.StepAwaitingPrecommits

			// TODO: this timer is intended to be a temporary workaround
			// following the change to unbuffered channels for the consensus manager.
			// We should still attempt the immediate send here,
			// but instead of panicking or using a timeout,
			// we should accumulate into a buffer and re-attempt in the handleLiveEvent loop.
			t := time.NewTimer(100 * time.Millisecond)
			defer t.Stop()
			select {
			case m.cm.ChooseProposedBlockRequests <- req:
				// Okay.
			case <-t.C:
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

		if len(req.PHs) > 0 {
			// If we filtered out invalid proposed blocks,
			// don't send the request.
			req := tsi.ConsiderProposedBlocksRequest{
				PHs:    req.PHs, // Outer declaration of req as a choose request.
				Result: rlc.PrevoteHashCh,
			}
			req.MarkReasonNewHashes(rlc)
			req.Reason.MajorityVotingPowerPresent = true

			// TODO: this timer is intended to be a temporary workaround
			// following the change to unbuffered channels for the consensus manager.
			// We should still attempt the immediate send here,
			// but instead of panicking or using a timeout,
			// we should accumulate into a buffer and re-attempt in the handleLiveEvent loop.
			t := time.NewTimer(100 * time.Millisecond)
			defer t.Stop()
			select {
			case m.cm.ConsiderProposedBlocksRequests <- req:
				// Okay.
			case <-t.C:
				panic("TODO: handle blocked send to ConsiderProposedBlocksRequests")
			}
		}
		return
	}

	// If we have only exceeded minority prevote power, we will continue waiting for proposed blocks.

	if len(vrv.ProposedHeaders) > len(rlc.VRV.ProposedHeaders) {
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

		incoming := m.rejectMismatchedProposedHeaders(vrv.ProposedHeaders, rlc)
		have := m.rejectMismatchedProposedHeaders(rlc.VRV.ProposedHeaders, rlc)
		// TODO: we could be more efficient than building up the have slice
		// only to check its length.
		if len(incoming) <= len(have) {
			// After filtering, no new entries.
			// Can't send a request to the consensus manager in this case.
			return
		}

		// The timer hasn't elapsed yet so it is only a Consider call at this point.
		req := tsi.ConsiderProposedBlocksRequest{
			PHs: incoming,

			Result: rlc.PrevoteHashCh,
		}
		req.MarkReasonNewHashes(rlc)

		// TODO: this timer is intended to be a temporary workaround
		// following the change to unbuffered channels for the consensus manager.
		// We should still attempt the immediate send here,
		// but instead of panicking or using a timeout,
		// we should accumulate into a buffer and re-attempt in the handleLiveEvent loop.
		t := time.NewTimer(100 * time.Millisecond)
		defer t.Stop()
		select {
		case m.cm.ConsiderProposedBlocksRequests <- req:
			// Okay.
		case <-t.C:
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
	if m.isParticipating(rlc) {
		// Record to the action store first.
		h, r := rlc.H, rlc.R
		vt := tmconsensus.VoteTarget{
			Height: h, Round: r,
			BlockHash: targetHash,
		}
		signContent, sig, err := m.signer.Prevote(ctx, vt)
		if err != nil {
			glog.HRE(m.log, h, r, err).Error(
				"Failed to sign prevote",
				"target_hash", glog.Hex(targetHash),
			)
			return false
		}

		if err := m.aStore.SavePrevoteAction(ctx, m.signer.PubKey(), vt, sig); err != nil {
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
	if !m.isParticipating(rlc) {
		return true
	}

	// Record to the action store first.
	h, r := rlc.H, rlc.R
	vt := tmconsensus.VoteTarget{
		Height: h, Round: r,
		BlockHash: targetHash,
	}
	signContent, sig, err := m.signer.Precommit(ctx, vt)
	if err != nil {
		glog.HRE(m.log, h, r, err).Error(
			"Failed to sign precommit content",
			"target_hash", glog.Hex(targetHash),
		)
		return false
	}

	if err := m.aStore.SavePrecommitAction(ctx, m.signer.PubKey(), vt, sig); err != nil {
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

	pbIdx := slices.IndexFunc(rlc.VRV.ProposedHeaders, func(ph tmconsensus.ProposedHeader) bool {
		return string(ph.Header.Hash) == rlc.VRV.VoteSummary.MostVotedPrecommitHash
	})
	if pbIdx >= 0 {
		// The previous VRV already had the proposed block,
		// so we can assume we already made the finalization request.
		return
	}

	// Now does the new VRV have the proposed block?
	pbIdx = slices.IndexFunc(vrv.ProposedHeaders, func(ph tmconsensus.ProposedHeader) bool {
		return string(ph.Header.Hash) == vrv.VoteSummary.MostVotedPrecommitHash
	})
	if pbIdx < 0 {
		// Not there yet, so don't do anything.
		return
	}

	// We have a valid index, so we can make the finalization request now.
	_ = gchan.SendC(
		ctx, m.log,
		m.finalizeBlockRequestCh, tmdriver.FinalizeBlockRequest{
			Header: vrv.ProposedHeaders[pbIdx].Header,
			Round:  vrv.Round,

			Resp: rlc.FinalizeRespCh,
		},
		"making finalize block request from handleCommitWaitViewUpdate",
	)
}

func (m *StateMachine) recordProposedHeader(
	ctx context.Context,
	rlc tsi.RoundLifecycle,
	p tmconsensus.Proposal,
) (ok bool) {
	h, r := rlc.H, rlc.R

	ph := tmconsensus.ProposedHeader{
		Header: tmconsensus.Header{
			PrevBlockHash: []byte(rlc.PrevBlockHash),

			Height: h,

			PrevCommitProof: rlc.VRV.RoundView.PrevCommitProof,

			ValidatorSet:     rlc.CurValSet,
			NextValidatorSet: rlc.PrevFinNextValSet,

			DataID: []byte(p.DataID),

			PrevAppStateHash: []byte(rlc.PrevFinAppStateHash),

			Annotations: p.BlockAnnotations,
		},

		Round: r,

		ProposerPubKey: m.signer.PubKey(),

		Annotations: p.ProposalAnnotations,
	}

	hash, err := m.hashScheme.Block(ph.Header)
	if err != nil {
		glog.HRE(m.log, h, r, err).Error("Failed to calculate hash for proposed block")
		return false
	}
	ph.Header.Hash = hash

	if err := m.signer.SignProposedHeader(ctx, &ph); err != nil {
		glog.HRE(m.log, h, r, err).Error("Failed to sign proposed block")
		return false
	}

	if err := m.aStore.SaveProposedHeaderAction(ctx, ph); err != nil {
		glog.HRE(m.log, h, r, err).Error("Failed to save proposed block to action store")
		return false
	}

	// The OutgoingActionsCh is 3-buffered so we assume this will never block.
	rlc.OutgoingActionsCh <- tmeil.StateMachineRoundAction{
		PH: ph,
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

	idx := slices.IndexFunc(vrv.ProposedHeaders, func(ph tmconsensus.ProposedHeader) bool {
		return string(ph.Header.Hash) == vrv.VoteSummary.MostVotedPrecommitHash
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
			Header: vrv.ProposedHeaders[idx].Header,
			Round:  vrv.Round,

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

	var err error
	rlc.FinalizedValSet, err = tmconsensus.NewValidatorSet(resp.Validators, m.hashScheme)
	if err != nil {
		glog.HRE(m.log, rlc.H, rlc.R, err).Error(
			"Failed to calculate hashes for newly finalized validator set",
		)
		return false
	}
	rlc.FinalizedAppStateHash = string(resp.AppStateHash)
	rlc.FinalizedBlockHash = string(resp.BlockHash)

	rlc.FinalizeRespCh = nil

	if resp.Height != rlc.H || resp.Round != rlc.R {
		panic(fmt.Errorf(
			"BUG: driver sent height/round %d/%d differing from current (%d/%d)",
			resp.Height, resp.Round, rlc.H, rlc.R,
		))
	}

	if err := m.fStore.SaveFinalization(
		ctx,
		rlc.H, rlc.R,
		string(resp.BlockHash),
		rlc.FinalizedValSet,
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
				PHs:    m.rejectMismatchedProposedHeaders(rlc.VRV.ProposedHeaders, rlc),
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
				VS:     rlc.VRV.VoteSummary.Clone(), // Clone under assumption to avoid data race.
				Result: rlc.PrecommitHashCh,         // Is it ever possible this channel is nil?
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

		if len(rlc.FinalizedValSet.Validators) == 0 {
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
	okPHs := m.rejectMismatchedProposedHeaders(rlc.VRV.ProposedHeaders, rlc)
	if len(okPHs) == 0 {
		return true
	}

	req := tsi.ConsiderProposedBlocksRequest{
		PHs:    okPHs,
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
	req.Reason.UpdatedBlockDataIDs = make([]string, 0, max(len(req.PHs), len(dataIDMap)))
	for _, ph := range req.PHs {
		_, dataArrived := dataIDMap[string(ph.Header.DataID)]
		if !dataArrived {
			continue
		}

		req.Reason.UpdatedBlockDataIDs = append(req.Reason.UpdatedBlockDataIDs, string(ph.Header.DataID))
	}

	if len(req.Reason.UpdatedBlockDataIDs) == 0 {
		// We had IDs arrive, but nothing matched the proposed blocks we have.
		return true
	}

	// We may have overallocated capacity, so trim it off to help GC.
	req.Reason.UpdatedBlockDataIDs = slices.Clip(req.Reason.UpdatedBlockDataIDs)

	// Now we can finally make the request.
	return gchan.SendC(
		ctx, m.log,
		m.cm.ConsiderProposedBlocksRequests, req,
		"making consider proposed blocks request following block data arrival",
	)
}

func (m *StateMachine) advanceHeight(ctx context.Context, rlc *tsi.RoundLifecycle) (ok bool) {
	rlc.CycleFinalization()
	rlc.Reset(ctx, rlc.H+1, 0)

	if err := m.smStore.SetStateMachineHeightRound(ctx, rlc.H, 0); err != nil {
		m.log.Error(
			"Failed to set state machine height/round when advancing height",
			"h", rlc.H,
			"err", err,
		)
		return false
	}

	re := tmeil.StateMachineRoundEntrance{
		H: rlc.H,
		R: 0,

		HeightCommitted: rlc.HeightCommitted,

		Response: make(chan tmeil.RoundEntranceResponse, 1),
	}
	return m.advance(ctx, rlc, re)
}

func (m *StateMachine) advanceRound(ctx context.Context, rlc *tsi.RoundLifecycle) (ok bool) {
	// TODO: do we need to do anything with the finalizations?
	rlc.Reset(ctx, rlc.H, rlc.R+1)

	if err := m.smStore.SetStateMachineHeightRound(ctx, rlc.H, rlc.R); err != nil {
		m.log.Error(
			"Failed to set state machine height/round when advancing round",
			"h", rlc.H,
			"r", rlc.R,
			"err", err,
		)
		return false
	}

	re := tmeil.StateMachineRoundEntrance{
		H: rlc.H,
		R: rlc.R,

		HeightCommitted: rlc.HeightCommitted,

		Response: make(chan tmeil.RoundEntranceResponse, 1),
	}

	return m.advance(ctx, rlc, re)
}

// advance handles advancing to a new height or round,
// depending on the content in re.
// This is the common code between advanceHeight and advanceRound.
func (m *StateMachine) advance(
	ctx context.Context, rlc *tsi.RoundLifecycle, re tmeil.StateMachineRoundEntrance,
) (ok bool) {
	// TODO: assert re.H > 0 and Response is not nil, buffered at 1.

	// We are assuming we are up to date,
	// but we might find out otherwise when we receive the round entrance response.
	if m.signer != nil {
		re.PubKey = m.signer.PubKey()
	}
	if m.isParticipating(rlc) {
		re.Actions = make(chan tmeil.StateMachineRoundAction, 3)
	}

	rer, ok := gchan.ReqResp(
		ctx, m.log,
		m.roundEntranceOutCh, re,
		re.Response,
		"receiving round entrance response while advancing height",
	)
	if !ok {
		return false
	}

	// This is similar to the handling in sendInitialActionSet,
	// but it differs because at this point it is impossible for us to be on the initial height;
	// and the rlc has some state we are reusing through the earlier call to rlc.CycleFinalization.
	if rer.IsVRV() {
		rlc.OutgoingActionsCh = re.Actions // Should this be part of the Reset method instead?

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

		if !m.beginRoundLive(ctx, rlc, rer.VRV) {
			return false
		}
	} else {
		// The state machine is still catching up with the mirror.
		rlc.MarkCatchingUp()

		// In replay, we just directly make a finalize block request.
		finReq := tmdriver.FinalizeBlockRequest{
			Header: rer.CH.Header,
			Round:  rer.CH.Proof.Round,

			Resp: rlc.FinalizeRespCh,
		}

		if !gchan.SendC(
			ctx, m.log,
			m.finalizeBlockRequestCh, finReq,
			"sending finalize block response for replayed block",
		) {
			return false
		}
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

// rejectMismatchedProposedHeaders returns a copy of the input slice,
// excluding any proposed blocks that do not match
// the expected previous app state hash or current validators.
func (m *StateMachine) rejectMismatchedProposedHeaders(
	in []tmconsensus.ProposedHeader, rlc *tsi.RoundLifecycle,
) []tmconsensus.ProposedHeader {
	if in == nil {
		// Special case; don't bother allocating an empty slice here.
		return nil
	}

	out := make([]tmconsensus.ProposedHeader, 0, len(in))
	for _, ph := range in {
		if string(ph.Header.PrevAppStateHash) != rlc.PrevFinAppStateHash {
			continue
		}

		if !ph.Header.ValidatorSet.Equal(rlc.CurValSet) {
			continue
		}
		if !ph.Header.NextValidatorSet.Equal(rlc.PrevFinNextValSet) {
			continue
		}

		out = append(out, ph)
	}

	return slices.Clip(out)
}

// isParticipating reports whether m has a signer that is part of the current validator set
// according to rlc.
func (m *StateMachine) isParticipating(rlc *tsi.RoundLifecycle) bool {
	if m.signer == nil {
		// Can't participate if we can't sign.
		return false
	}

	key := m.signer.PubKey()
	return slices.ContainsFunc(rlc.CurValSet.Validators, func(v tmconsensus.Validator) bool {
		eq := v.PubKey.Equal(key)
		return eq
	})
}
