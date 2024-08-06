package tmconsensustest

import (
	"context"
	"fmt"
	"sync"

	"github.com/rollchains/gordian/tm/tmconsensus"
)

type hr struct {
	H uint64
	R uint32
}

// MockConsensusStrategy is an implementation of [tmconsensus.ConsensusStrategy]
// for ease of testing.
type MockConsensusStrategy struct {
	ConsiderProposedBlocksRequests <-chan ConsiderProposedBlocksRequest // Exported receive-only.
	considerPBReqs                 chan<- ConsiderProposedBlocksRequest // Unexported send-only.

	ChooseProposedBlockRequests <-chan ChooseProposedBlockRequest // Exported receive-only.
	choosePBReqs                chan<- ChooseProposedBlockRequest // Unexported send-only.

	DecidePrecommitRequests <-chan DecidePrecommitRequest // Exported receive-only.
	decidePrecommitReqs     chan<- DecidePrecommitRequest // Unexported send-only.

	mu sync.Mutex

	expectEnters       map[hr]chan EnterRoundCall
	expectEnterReturns map[hr]error
}

// EnterRoundCall holds the arguments provided to a call to [tmconsensus.ConsensusStrategy.EnterRound].
// The test may send a value to the ProposalOut channel
// in order to simulate the application proposing data to be included in a block.
type EnterRoundCall struct {
	RV          tmconsensus.RoundView
	ProposalOut chan<- tmconsensus.Proposal
}

// ConsiderProposedBlocksRequest is sent on [MockConsensusStrategy.ConsiderProposedBlocksRequest]
// upon a call to ConsiderProposedBlocks.
//
// The test must send one value on either of the 1-buffered ChoiceHash or ChoiceError channels
// in order for the ChooseProposedBlock method to return
// (or cancel the context passed to the method).
type ConsiderProposedBlocksRequest struct {
	PBs         []tmconsensus.ProposedBlock
	Reason      tmconsensus.ConsiderProposedBlocksReason
	ChoiceHash  chan string
	ChoiceError chan error
}

// ChooseProposedBlockRequest is sent on [MockConsensusStrategy.ChooseProposedBlockRequests]
// upon a call to ChooseProposedBlock.
//
// The test must send one value on either of the 1-buffered ChoiceHash or ChoiceError channels
// in order for the ChooseProposedBlock method to return
// (or cancel the context passed to the method).
type ChooseProposedBlockRequest struct {
	Input       []tmconsensus.ProposedBlock
	ChoiceHash  chan string
	ChoiceError chan error
}

type DecidePrecommitRequest struct {
	Input       tmconsensus.VoteSummary
	ChoiceHash  chan string
	ChoiceError chan error
}

func NewMockConsensusStrategy() *MockConsensusStrategy {
	considerPBReqCh := make(chan ConsiderProposedBlocksRequest, 1)
	choosePBReqCh := make(chan ChooseProposedBlockRequest, 1)
	decidePrecommitReqCh := make(chan DecidePrecommitRequest, 1)
	return &MockConsensusStrategy{
		ConsiderProposedBlocksRequests: considerPBReqCh,
		considerPBReqs:                 considerPBReqCh,

		ChooseProposedBlockRequests: choosePBReqCh,
		choosePBReqs:                choosePBReqCh,

		DecidePrecommitRequests: decidePrecommitReqCh,
		decidePrecommitReqs:     decidePrecommitReqCh,

		expectEnters:       make(map[hr]chan EnterRoundCall),
		expectEnterReturns: make(map[hr]error),
	}
}

// ExpectEnterRound returns a channel that receives the RoundView value
// once there is a call to EnterRound with a block matching the given height and round.
func (s *MockConsensusStrategy) ExpectEnterRound(
	height uint64, round uint32,
	returnErr error,
) <-chan EnterRoundCall {
	s.mu.Lock()
	defer s.mu.Unlock()

	ch := make(chan EnterRoundCall, 1)
	hr := hr{H: height, R: round}
	s.expectEnters[hr] = ch
	s.expectEnterReturns[hr] = returnErr
	return ch
}

func (s *MockConsensusStrategy) EnterRound(
	ctx context.Context,
	rv tmconsensus.RoundView,
	proposalOut chan<- tmconsensus.Proposal,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	hr := hr{H: rv.Height, R: rv.Round}
	e, ok := s.expectEnterReturns[hr]
	if !ok {
		panic(fmt.Errorf(
			"EnterRound called with h=%d r=%d before a call to ExpectEnterRound (or it was called a second time)",
			hr.H, hr.R,
		))
	}
	delete(s.expectEnterReturns, hr)

	ch, ok := s.expectEnters[hr]
	if !ok {
		panic(fmt.Errorf(
			"EnterRound called with h=%d r=%d before a call to ExpectEnterRound (or it was called a second time)",
			hr.H, hr.R,
		))
	}
	delete(s.expectEnters, hr)

	ch <- EnterRoundCall{
		RV:          rv,
		ProposalOut: proposalOut,
	}

	return e
}

func (s *MockConsensusStrategy) ConsiderProposedBlocks(
	ctx context.Context,
	pbs []tmconsensus.ProposedBlock,
	reason tmconsensus.ConsiderProposedBlocksReason,
) (string, error) {
	req := ConsiderProposedBlocksRequest{
		PBs:         pbs,
		Reason:      reason,
		ChoiceHash:  make(chan string, 1),
		ChoiceError: make(chan error, 1),
	}

	// Using manual select instead of gchan package
	// because we don't have a logger in this type.
	select {
	case <-ctx.Done():
		return "", context.Cause(ctx)
	case s.considerPBReqs <- req:
		// Okay.
	}

	select {
	case <-ctx.Done():
		return "", context.Cause(ctx)
	case h := <-req.ChoiceHash:
		return h, nil
	case err := <-req.ChoiceError:
		return "", err
	}
}

func (s *MockConsensusStrategy) ChooseProposedBlock(
	ctx context.Context, pbs []tmconsensus.ProposedBlock,
) (string, error) {
	req := ChooseProposedBlockRequest{
		Input:       pbs,
		ChoiceHash:  make(chan string, 1),
		ChoiceError: make(chan error, 1),
	}

	// Using manual select instead of gchan package
	// because we don't have a logger in this type.
	select {
	case <-ctx.Done():
		return "", context.Cause(ctx)
	case s.choosePBReqs <- req:
		// Okay.
	}

	select {
	case <-ctx.Done():
		return "", context.Cause(ctx)
	case h := <-req.ChoiceHash:
		return h, nil
	case err := <-req.ChoiceError:
		return "", err
	}
}

func (s *MockConsensusStrategy) DecidePrecommit(
	ctx context.Context, vs tmconsensus.VoteSummary,
) (string, error) {
	req := DecidePrecommitRequest{
		Input:       vs,
		ChoiceHash:  make(chan string, 1),
		ChoiceError: make(chan error, 1),
	}

	// Using manual select instead of gchan package
	// because we don't have a logger in this type.
	select {
	case <-ctx.Done():
		return "", context.Cause(ctx)
	case s.decidePrecommitReqs <- req:
		// Okay.
	}

	select {
	case <-ctx.Done():
		return "", context.Cause(ctx)
	case h := <-req.ChoiceHash:
		return h, nil
	case err := <-req.ChoiceError:
		return "", err
	}
}
