package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"sync"

	"github.com/rollchains/gordian/gcrypto"
	"github.com/rollchains/gordian/internal/glog"
	"github.com/rollchains/gordian/tm/tmconsensus"
	"github.com/rollchains/gordian/tm/tmdriver"
)

type echoConfig struct {
	RemoteAddrs []string

	ValidatorPubKeys []string // hex-encoded ed25519 public keys
}

type echoApp struct {
	log *slog.Logger

	vals []tmconsensus.Validator

	done chan struct{}
}

func newEchoApp(
	ctx context.Context,
	log *slog.Logger,
	initChainRequests <-chan tmdriver.InitChainRequest,
	finBlockRequests <-chan tmdriver.FinalizeBlockRequest,
) *echoApp {
	a := &echoApp{
		log:  log,
		done: make(chan struct{}),
	}
	go a.background(ctx, initChainRequests, finBlockRequests)
	return a
}

func (a *echoApp) background(
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
		a.vals = req.Genesis.GenesisValidators

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
				Height:    req.Block.Height,
				Round:     req.Round,
				BlockHash: req.Block.Hash,

				// We never change validators.
				Validators: req.Block.NextValidators,
			}

			blockData := fmt.Sprintf("Height: %d; Round: %d", resp.Height, resp.Round)
			appStateHash := sha256.Sum256([]byte(blockData))

			resp.AppStateHash = appStateHash[:]

			a.log.Info(
				"Finalizing block",
				"block_hash", glog.Hex(req.Block.Hash),
				"height", req.Block.Height,
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

func (a *echoApp) Wait() {
	<-a.done
}

type echoConsensusStrategy struct {
	Log    *slog.Logger
	PubKey gcrypto.PubKey

	// Round-specific values.
	mu                sync.Mutex
	expProposerPubKey gcrypto.PubKey
	curH              uint64
	curR              uint32
}

func (s *echoConsensusStrategy) EnterRound(ctx context.Context, rv tmconsensus.RoundView, proposalOut chan<- tmconsensus.Proposal) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.curH = rv.Height
	s.curR = rv.Round

	// Pseudo-copy of the modulo round robin proposer selection strategy that the v0.2 code uses.

	expProposerIndex := (int(rv.Height) + int(rv.Round)) % len(rv.Validators)
	s.expProposerPubKey = rv.Validators[expProposerIndex].PubKey
	s.Log.Info("Entering round", "height", rv.Height, "round", rv.Round, "exp_proposer_index", expProposerIndex)

	if s.expProposerPubKey.Equal(s.PubKey) {
		appData := fmt.Sprintf("Height: %d; Round: %d", s.curH, s.curR)
		dataHash := sha256.Sum256([]byte(appData))
		proposalOut <- tmconsensus.Proposal{
			DataID: string(dataHash[:]),
		}
		s.Log.Info("Proposing block", "h", s.curH, "r", s.curR)
	}

	return nil
}

func (s *echoConsensusStrategy) ConsiderProposedBlocks(
	ctx context.Context,
	pbs []tmconsensus.ProposedBlock,
	_ tmconsensus.ConsiderProposedBlocksReason,
) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, pb := range pbs {
		if !s.expProposerPubKey.Equal(pb.ProposerPubKey) {
			continue
		}

		// Found a proposed block from the expected proposer.
		expBlockData := fmt.Sprintf("Height: %d; Round: %d", s.curH, s.curR)
		expDataHash := sha256.Sum256([]byte(expBlockData))

		if !bytes.Equal(pb.Block.DataID, expDataHash[:]) {
			s.Log.Info("Rejecting proposed block from expected proposer",
				"exp_id", glog.Hex(expDataHash[:]),
				"got_id", glog.Hex(pb.Block.DataID),
			)
			return "", nil
		}

		if s.PubKey != nil && s.PubKey.Equal(pb.ProposerPubKey) {
			s.Log.Info("Voting on a block that we proposed",
				"h", s.curH, "r", s.curR,
				"block_hash", glog.Hex(pb.Block.Hash),
			)
		}
		return string(pb.Block.Hash), nil
	}

	// Didn't see a proposed block from the expected proposer.
	return "", tmconsensus.ErrProposedBlockChoiceNotReady
}

func (s *echoConsensusStrategy) ChooseProposedBlock(ctx context.Context, pbs []tmconsensus.ProposedBlock) (string, error) {
	// Follow the ConsiderProposedBlocks logic...
	hash, err := s.ConsiderProposedBlocks(ctx, pbs, tmconsensus.ConsiderProposedBlocksReason{})
	if err == tmconsensus.ErrProposedBlockChoiceNotReady {
		// ... and if there is no choice ready, then vote nil.
		return "", nil
	}
	return hash, err
}

func (s *echoConsensusStrategy) DecidePrecommit(ctx context.Context, vs tmconsensus.VoteSummary) (string, error) {
	maj := tmconsensus.ByzantineMajority(vs.AvailablePower)
	if pow := vs.PrevoteBlockPower[vs.MostVotedPrevoteHash]; pow >= maj {
		s.Log.Info(
			"Submitting precommit",
			"h", s.curH, "r", s.curR,
			"block_hash", glog.Hex(vs.MostVotedPrevoteHash),
		)
		return vs.MostVotedPrevoteHash, nil
	}

	// Didn't reach consensus on one block; automatically precommit nil.
	s.Log.Info(
		"Submitting nil precommit",
		"h", s.curH, "r", s.curR,
	)
	return "", nil
}
