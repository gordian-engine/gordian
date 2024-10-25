package tsi

import (
	"context"
	"log/slog"
	"runtime/trace"

	"github.com/gordian-engine/gordian/internal/gchan"
	"github.com/gordian-engine/gordian/tm/tmconsensus"
)

// ConsensusManager is a subsystem of the state machine
// that is reponsible for handling [tmconsensus.ConsensusStrategy] calls
// in a dedicated goroutine so as to avoid blocking the state machine kernel.
type ConsensusManager struct {
	log *slog.Logger

	strat tmconsensus.ConsensusStrategy

	EnterRoundRequests             chan EnterRoundRequest
	ConsiderProposedBlocksRequests chan ConsiderProposedBlocksRequest
	ChooseProposedBlockRequests    chan ChooseProposedBlockRequest

	DecidePrecommitRequests chan DecidePrecommitRequest

	done chan struct{}
}

// EnterRoundRequest is the request type sent by the state machine
// requesting a call to [tmconsensus.ConsensusStrategy.EnterRound].
type EnterRoundRequest struct {
	RV     tmconsensus.RoundView
	Result chan error

	// If the strategy is going to propose a block for this round,
	// the proposal data must be sent on this channel.
	ProposalOut chan tmconsensus.Proposal
}

// ConsiderProposedBlocksRequest is the request type sent by the state machine
// requesting a call to [tmconsensus.ConsensusStrategy.ConsiderProposedBlocks].
type ConsiderProposedBlocksRequest struct {
	PHs    []tmconsensus.ProposedHeader
	Reason tmconsensus.ConsiderProposedBlocksReason
	Result chan HashSelection
}

// MarkNewHashes compares the incoming PBs on r with rlc.PrevConsideredHashes.
// Any blocks that are not in rlc.PrevConsideredHashes are noted in r.Reason.NewProposedBlocks,
// and marked on rlc.PrevConsideredHashes.
func (r *ConsiderProposedBlocksRequest) MarkReasonNewHashes(rlc *RoundLifecycle) {
	for _, ph := range r.PHs {
		// Casting a byte slice to a string in a map access,
		// as a memory optimization, seems to still be relevant in Go 1.22.
		if _, ok := rlc.PrevConsideredHashes[string(ph.Header.Hash)]; ok {
			continue
		}
		h := string(ph.Header.Hash)
		r.Reason.NewProposedBlocks = append(r.Reason.NewProposedBlocks, h)
		rlc.PrevConsideredHashes[h] = struct{}{}
	}
}

// ChooseProposedBlockRequest is the request type sent by the state machine
// requesting a call to [tmconsensus.ConsensusStrategy.ChooseProposedBlock].
type ChooseProposedBlockRequest struct {
	PHs    []tmconsensus.ProposedHeader
	Result chan HashSelection
}

// DecidePrecommitRequest is the request type sent by the state machine
// requesting a call to [tmconsensus.ConsensusStrategy.DecidePrecommit].
type DecidePrecommitRequest struct {
	VS     tmconsensus.VoteSummary
	Result chan HashSelection
}

// HashSelection is the result type inside [ChooseProposedBlockRequest]
// containing either a selected hash or an error.
type HashSelection struct {
	Hash string
	Err  error
}

// NewConsensusManager returns an initialized ConsensusManager.
func NewConsensusManager(
	ctx context.Context, log *slog.Logger, strat tmconsensus.ConsensusStrategy,
) *ConsensusManager {
	m := &ConsensusManager{
		log:   log,
		strat: strat,

		// Currently, the state machine needs to synchronize
		// with all of the consensus strategy interactions;
		// therefore all of these channels are unbuffered.
		//
		// The mirror layer can handle the state machine being blocked
		// from receiving on the mirror-to-state-machine channels,
		// so the system should overall be safe.
		// However, we should be able to introduce buffering
		// into the state machine, such that we handle mirror messages
		// while waiting on consensus manager responses.
		EnterRoundRequests:             make(chan EnterRoundRequest),
		ConsiderProposedBlocksRequests: make(chan ConsiderProposedBlocksRequest),
		ChooseProposedBlockRequests:    make(chan ChooseProposedBlockRequest),
		DecidePrecommitRequests:        make(chan DecidePrecommitRequest),

		done: make(chan struct{}),
	}

	go m.kernel(ctx)

	return m
}

// Wait blocks until m's background goroutines finish.
// Initiate a shutdown by canceling the context passed to NewConsensusManager.
func (m *ConsensusManager) Wait() {
	<-m.done
}

func (m *ConsensusManager) kernel(ctx context.Context) {
	defer close(m.done)

	ctx, task := trace.NewTask(ctx, "ConsensusManager.kernel")
	defer task.End()

	for {
		select {
		case <-ctx.Done():
			m.log.Info("Stopping due to context cancellation", "cause", context.Cause(ctx))
			return

		case req := <-m.EnterRoundRequests:
			m.handleEnterRound(ctx, req)

		case req := <-m.ConsiderProposedBlocksRequests:
			m.handleConsiderPBs(ctx, req)

		case req := <-m.ChooseProposedBlockRequests:
			m.handleChoosePB(ctx, req)

		case req := <-m.DecidePrecommitRequests:
			m.handleDecidePrecommit(ctx, req)
		}
	}
}

func (m *ConsensusManager) handleEnterRound(ctx context.Context, req EnterRoundRequest) {
	defer trace.StartRegion(ctx, "handleEnterRound").End()

	err := m.strat.EnterRound(ctx, req.RV, req.ProposalOut)

	_ = gchan.SendC(
		ctx, m.log,
		req.Result, err,
		"sending EnterRound result",
	)
}

func (m *ConsensusManager) handleConsiderPBs(ctx context.Context, req ConsiderProposedBlocksRequest) {
	defer trace.StartRegion(ctx, "handleConsiderPBs").End()

	hash, err := m.strat.ConsiderProposedBlocks(ctx, req.PHs, req.Reason)
	if err == tmconsensus.ErrProposedBlockChoiceNotReady {
		// Don't bother with a send if we aren't choosing yet.
		return
	}

	_ = gchan.SendC(
		ctx, m.log,
		req.Result, HashSelection{Hash: hash, Err: err},
		"sending ConsiderProposedBlocks result",
	)
}

func (m *ConsensusManager) handleChoosePB(ctx context.Context, req ChooseProposedBlockRequest) {
	defer trace.StartRegion(ctx, "handleChoosePB").End()

	hash, err := m.strat.ChooseProposedBlock(ctx, req.PHs)
	_ = gchan.SendC(
		ctx, m.log,
		req.Result, HashSelection{Hash: hash, Err: err},
		"sending ChooseProposedBlock result",
	)
}

func (m *ConsensusManager) handleDecidePrecommit(ctx context.Context, req DecidePrecommitRequest) {
	defer trace.StartRegion(ctx, "handleDecidePrecommit").End()

	hash, err := m.strat.DecidePrecommit(ctx, req.VS)
	_ = gchan.SendC(
		ctx, m.log,
		req.Result, HashSelection{Hash: hash, Err: err},
		"sending DecidePrecommit result",
	)
}
