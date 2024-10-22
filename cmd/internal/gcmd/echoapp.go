package gcmd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"sync"

	"github.com/gordian-engine/gordian/gcrypto"
	"github.com/gordian-engine/gordian/internal/glog"
	"github.com/gordian-engine/gordian/tm/tmconsensus"
	"github.com/gordian-engine/gordian/tm/tmdriver"
)

type EchoApp struct {
	log *slog.Logger

	vals []tmconsensus.Validator

	done chan struct{}
}

func NewEchoApp(
	ctx context.Context,
	log *slog.Logger,
	initChainRequests <-chan tmdriver.InitChainRequest,
	finBlockRequests <-chan tmdriver.FinalizeBlockRequest,
) *EchoApp {
	a := &EchoApp{
		log:  log,
		done: make(chan struct{}),
	}
	go a.background(ctx, initChainRequests, finBlockRequests)
	return a
}

func (a *EchoApp) background(
	ctx context.Context,
	initChainRequests <-chan tmdriver.InitChainRequest,
	finalizeBlockRequests <-chan tmdriver.FinalizeBlockRequest,
) {
	defer close(a.done)

	// Assume we always need to initialize the chain at startup.
	select {
	case <-ctx.Done():
		a.log.Info("Stopping due to context cancellation", "cause", context.Cause(ctx))
		return

	case req := <-initChainRequests:
		a.vals = req.Genesis.GenesisValidatorSet.Validators

		// Ignore genesis app state, start with empty state.

		stateHash := sha256.Sum256([]byte(""))
		select {
		case req.Resp <- tmdriver.InitChainResponse{
			AppStateHash: stateHash[:],

			// Omitting validators since we want to match the input.
		}:
			// Okay.
		case <-ctx.Done():
			a.log.Info(
				"Stopping due to context cancellation while attempting to respond to InitChainRequest",
				"cause", context.Cause(ctx),
			)
			return
		}
	}

	for {
		select {
		case <-ctx.Done():
			a.log.Info("Stopping due to context cancellation", "cause", context.Cause(ctx))
			return

		case req := <-finalizeBlockRequests:
			resp := tmdriver.FinalizeBlockResponse{
				Height:    req.Header.Height,
				Round:     req.Round,
				BlockHash: req.Header.Hash,

				// We never change validators.
				Validators: req.Header.NextValidatorSet.Validators,
			}

			blockData := fmt.Sprintf("Height: %d; Round: %d", resp.Height, resp.Round)
			appStateHash := sha256.Sum256([]byte(blockData))

			resp.AppStateHash = appStateHash[:]

			a.log.Info(
				"Finalizing block",
				"block_hash", glog.Hex(req.Header.Hash),
				"height", req.Header.Height,
			)

			select {
			case req.Resp <- resp:
				// Okay.
			case <-ctx.Done():
				a.log.Info("Stopping due to context cancellation while attempting to respond to FinalizeBlockRequest")
				return
			}
		}
	}
}

func (a *EchoApp) Wait() {
	<-a.done
}

type EchoConsensusStrategy struct {
	log    *slog.Logger
	pubKey gcrypto.PubKey

	// Round-specific values.
	mu                sync.Mutex
	expProposerPubKey gcrypto.PubKey
	curH              uint64
	curR              uint32
}

func NewEchoConsensusStrategy(log *slog.Logger, pubKey gcrypto.PubKey) *EchoConsensusStrategy {
	return &EchoConsensusStrategy{log: log, pubKey: pubKey}
}

func (s *EchoConsensusStrategy) EnterRound(ctx context.Context, rv tmconsensus.RoundView, proposalOut chan<- tmconsensus.Proposal) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.curH = rv.Height
	s.curR = rv.Round

	// Pseudo-copy of the modulo round robin proposer selection strategy that the v0.2 code used.

	expProposerIndex := (int(rv.Height) + int(rv.Round)) % len(rv.ValidatorSet.Validators)
	s.expProposerPubKey = rv.ValidatorSet.Validators[expProposerIndex].PubKey
	s.log.Info("Entering round", "height", rv.Height, "round", rv.Round, "exp_proposer_index", expProposerIndex)

	if s.expProposerPubKey.Equal(s.pubKey) {
		appData := fmt.Sprintf("Height: %d; Round: %d", s.curH, s.curR)
		dataHash := sha256.Sum256([]byte(appData))
		proposalOut <- tmconsensus.Proposal{
			DataID: string(dataHash[:]),
		}
		s.log.Info("Proposing block", "h", s.curH, "r", s.curR)
	}

	return nil
}

func (s *EchoConsensusStrategy) ConsiderProposedBlocks(
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

		// Found a proposed block from the expected proposer.
		expBlockData := fmt.Sprintf("Height: %d; Round: %d", s.curH, s.curR)
		expDataHash := sha256.Sum256([]byte(expBlockData))

		if !bytes.Equal(ph.Header.DataID, expDataHash[:]) {
			s.log.Info(
				"Rejecting proposed block from expected proposer",
				"exp_id", glog.Hex(expDataHash[:]),
				"got_id", glog.Hex(ph.Header.DataID),
			)
			return "", nil
		}

		if s.pubKey != nil && s.pubKey.Equal(ph.ProposerPubKey) {
			s.log.Info(
				"Voting on a block that we proposed",
				"h", s.curH, "r", s.curR,
				"block_hash", glog.Hex(ph.Header.Hash),
			)
		}
		return string(ph.Header.Hash), nil
	}

	// Didn't see a proposed block from the expected proposer.
	return "", tmconsensus.ErrProposedBlockChoiceNotReady
}

func (s *EchoConsensusStrategy) ChooseProposedBlock(ctx context.Context, phs []tmconsensus.ProposedHeader) (string, error) {
	// Follow the ConsiderProposedBlocks logic...
	hash, err := s.ConsiderProposedBlocks(ctx, phs, tmconsensus.ConsiderProposedBlocksReason{})
	if err == tmconsensus.ErrProposedBlockChoiceNotReady {
		// ... and if there is no choice ready, then vote nil.
		return "", nil
	}
	return hash, err
}

func (s *EchoConsensusStrategy) DecidePrecommit(ctx context.Context, vs tmconsensus.VoteSummary) (string, error) {
	maj := tmconsensus.ByzantineMajority(vs.AvailablePower)
	if pow := vs.PrevoteBlockPower[vs.MostVotedPrevoteHash]; pow >= maj {
		s.log.Info(
			"Submitting precommit",
			"h", s.curH, "r", s.curR,
			"block_hash", glog.Hex(vs.MostVotedPrevoteHash),
		)
		return vs.MostVotedPrevoteHash, nil
	}

	// Didn't reach consensus on one block; automatically precommit nil.
	s.log.Info(
		"Submitting nil precommit",
		"h", s.curH, "r", s.curR,
	)
	return "", nil
}
