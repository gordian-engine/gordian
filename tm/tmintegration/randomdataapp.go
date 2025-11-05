package tmintegration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sync"

	"github.com/gordian-engine/gordian/gcrypto"
	"github.com/gordian-engine/gordian/internal/glog"
	"github.com/gordian-engine/gordian/tm/tmconsensus"
	"github.com/gordian-engine/gordian/tm/tmdriver"
)

type randomDataApp struct {
	// When the app finalizes a block, it sends a value on this channel,
	// for the test to consume.
	FinalizeResponses chan tmdriver.FinalizeBlockResponse

	log *slog.Logger

	idx int

	done chan struct{}
}

func newRandomDataApp(
	ctx context.Context,
	log *slog.Logger,
	idx int,
	initChainRequests <-chan tmdriver.InitChainRequest,
	finalizeBlockRequests <-chan tmdriver.FinalizeBlockRequest,
) *randomDataApp {
	a := &randomDataApp{
		FinalizeResponses: make(chan tmdriver.FinalizeBlockResponse, 1),

		log: log,
		idx: idx,

		done: make(chan struct{}),
	}

	go a.mainLoop(ctx, initChainRequests, finalizeBlockRequests)

	return a
}

func (a *randomDataApp) Wait() {
	<-a.done
}

func (a *randomDataApp) mainLoop(
	ctx context.Context,
	initChainRequests <-chan tmdriver.InitChainRequest,
	finalizeBlockRequests <-chan tmdriver.FinalizeBlockRequest,
) {
	defer close(a.done)

	appStateHash := sha256.Sum256(nil)

	// Assume we always need to initialize the chain at startup.
	select {
	case <-ctx.Done():
		a.log.Info("Stopping due to context cancellation", "cause", context.Cause(ctx))
		return

	case req := <-initChainRequests:
		select {
		case <-ctx.Done():
			return
		case req.Resp <- tmdriver.InitChainResponse{
			AppStateHash: bytes.Clone(appStateHash[:]),

			// Omitting validators since we want to match the input.
		}:
			// Okay.
		}
	}

	// Now we have to finalize blocks repeatedly.
	newHashInput := make([]byte, 0, 2*sha256.Size)
	for {
		select {
		case <-ctx.Done():
			a.log.Info("Stopping due to context cancellation", "cause", context.Cause(ctx))
			return

		case req := <-finalizeBlockRequests:
			// Recalculate app state hash.
			newHashInput = append(newHashInput[:0], appStateHash[:]...)
			newHashInput = append(newHashInput, req.Header.DataID...)
			appStateHash = sha256.Sum256(newHashInput)

			resp := tmdriver.FinalizeBlockResponse{
				Height:    req.Header.Height,
				Round:     req.Round,
				BlockHash: req.Header.Hash,

				Validators: req.Header.ValidatorSet.Validators,

				AppStateHash: bytes.Clone(appStateHash[:]),
			}

			// The response channel is guaranteed to be 1-buffered.
			req.Resp <- resp

			// But we also output to the test harness, which could potentially block.
			select {
			case <-ctx.Done():
				return
			case a.FinalizeResponses <- resp:
				// Okay.
			}
		}
	}
}

type randomDataConsensusStrategy struct {
	Log    *slog.Logger
	PubKey gcrypto.PubKey

	RNG *rand.ChaCha8

	mu                sync.Mutex
	expProposerPubKey gcrypto.PubKey
	expProposerIndex  int
	curH              uint64
	curR              uint32
}

func (s *randomDataConsensusStrategy) EnterRound(
	ctx context.Context, rv tmconsensus.RoundView, proposalOut chan<- tmconsensus.Proposal,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.curH = rv.Height
	s.curR = rv.Round

	// Pseudo-copy of the modulo round robin proposer selection strategy that the v0.2 code used.
	s.expProposerIndex = (int(rv.Height) + int(rv.Round)) % len(rv.ValidatorSet.Validators)
	s.expProposerPubKey = rv.ValidatorSet.Validators[s.expProposerIndex].PubKey

	if !s.expProposerPubKey.Equal(s.PubKey) {
		// We are not the proposer.
		return nil
	}

	// We are the proposer, so generate some random data for this block.
	blockData := make([]byte, 32*1024)
	if _, err := s.RNG.Read(blockData); err != nil {
		return fmt.Errorf("failed to generate block data: %w", err)
	}

	dataID := sha256.Sum256(blockData)
	// TODO: need to store the block data for the proposer.

	proposalOut <- tmconsensus.Proposal{
		DataID: string(dataID[:]),
	}

	return nil
}

func (s *randomDataConsensusStrategy) ConsiderProposedBlocks(
	ctx context.Context,
	phs []tmconsensus.ProposedHeader,
	_ tmconsensus.ConsiderProposedBlocksReason,
) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, ph := range phs {
		if !s.expProposerPubKey.Equal(ph.ProposerPubKey) {
			continue
		}

		// TODO: look up block data and confirm it matches the data ID.
		s.Log.Info("Prevote in favor of block", "hash", glog.Hex(ph.Header.Hash), "height", s.curH)
		return string(ph.Header.Hash), nil
	}

	// Didn't see a proposed block from the expected proposer.
	s.Log.Info("Prevote not ready", "height", s.curH)
	return "", tmconsensus.ErrProposedBlockChoiceNotReady
}

func (s *randomDataConsensusStrategy) ChooseProposedBlock(ctx context.Context, phs []tmconsensus.ProposedHeader) (string, error) {
	// Follow the ConsiderProposedBlocks logic...
	hash, err := s.ConsiderProposedBlocks(ctx, phs, tmconsensus.ConsiderProposedBlocksReason{})
	if err == tmconsensus.ErrProposedBlockChoiceNotReady {
		// ... and if there is no choice ready, then vote nil.
		return "", nil
	}
	return hash, err
}

func (s *randomDataConsensusStrategy) DecidePrecommit(ctx context.Context, vs tmconsensus.VoteSummary) (string, error) {
	maj := tmconsensus.ByzantineMajority(vs.AvailablePower)
	if pow := vs.PrevoteBlockPower[vs.MostVotedPrevoteHash]; pow >= maj {
		return vs.MostVotedPrevoteHash, nil
	}

	// Didn't reach consensus on one block; automatically precommit nil.
	return "", nil
}
