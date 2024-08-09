package tmintegration

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sync"

	"github.com/rollchains/gordian/gcrypto"
	"github.com/rollchains/gordian/internal/glog"
	"github.com/rollchains/gordian/tm/tmconsensus"
	"github.com/rollchains/gordian/tm/tmdriver"
	"golang.org/x/crypto/blake2b"
)

type valShuffleApp struct {
	// When the app finalizes a block, it sends a value on this channel,
	// for the test to consume.
	FinalizeResponses chan tmdriver.FinalizeBlockResponse

	pickN int // Number of initial validators to pick for every height.

	log *slog.Logger

	idx int

	hs tmconsensus.HashScheme

	done chan struct{}
}

func newValShuffleApp(
	ctx context.Context,
	log *slog.Logger,
	idx int,
	hashScheme tmconsensus.HashScheme,
	pickN int,
	initChainRequests <-chan tmdriver.InitChainRequest,
	finalizeBlockRequests <-chan tmdriver.FinalizeBlockRequest,
) *valShuffleApp {
	a := &valShuffleApp{
		// If this channel is unbuffered, there is a risk the test will deadlock.
		// Making this 1-buffered allows the app to continue before
		// the test harness reads from it; advancing 1 height ahead should be safe.
		FinalizeResponses: make(chan tmdriver.FinalizeBlockResponse, 1),

		pickN: pickN,

		log:  log,
		idx:  idx,
		hs:   hashScheme,
		done: make(chan struct{}),
	}

	go a.kernel(ctx, initChainRequests, finalizeBlockRequests)

	return a
}

func (a *valShuffleApp) Wait() {
	<-a.done
}

func (a *valShuffleApp) kernel(
	ctx context.Context,
	initChainRequests <-chan tmdriver.InitChainRequest,
	finalizeBlockRequests <-chan tmdriver.FinalizeBlockRequest,
) {
	defer close(a.done)

	var initVals []tmconsensus.Validator

	// Assume we always need to initialize the chain at startup.
	select {
	case <-ctx.Done():
		a.log.Info("Stopping due to context cancellation", "cause", context.Cause(ctx))
		return

	case req := <-initChainRequests:
		initVals = req.Genesis.GenesisValidators

		// State hash for this app is the concatenation of the height and the validator key and power hashes.
		keyHash, powHash, err := validatorHashes(initVals, a.hs)
		if err != nil {
			panic(fmt.Errorf("failed to get validator hashes during init chain: %w", err))
		}

		stateHash := a.stateHash(0, keyHash, powHash)

		select {
		case req.Resp <- tmdriver.InitChainResponse{
			AppStateHash: stateHash,

			// Omitting validators since we want to match the input.
		}:
			// Okay.
		case <-ctx.Done():
			return
		}
	}

	// Then we have to finalize blocks repeatedly.
	pcg := rand.NewPCG(0, 0)
	rng := rand.New(pcg)
	for {
		select {
		case <-ctx.Done():
			a.log.Info("Stopping due to context cancellation", "cause", context.Cause(ctx))
			return

		case req := <-finalizeBlockRequests:
			keyHash, powHash, err := validatorHashes(req.Block.Validators, a.hs)
			if err != nil {
				panic(fmt.Errorf("failed to calculate validator hashes at finalization: %w", err))
			}

			var nextVals []tmconsensus.Validator
			pcg.Seed(req.Block.Height+2, 0)
			for _, origIdx := range rng.Perm(len(initVals))[:a.pickN] {
				nextVals = append(nextVals, initVals[origIdx])
			}

			resp := tmdriver.FinalizeBlockResponse{
				Height:    req.Block.Height,
				Round:     req.Round,
				BlockHash: req.Block.Hash,

				Validators: nextVals,

				AppStateHash: a.stateHash(req.Block.Height, keyHash, powHash),
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

func validatorHashes(vals []tmconsensus.Validator, hs tmconsensus.HashScheme) (
	keyHash, powHash []byte, err error,
) {
	keyHash, err = hs.PubKeys(tmconsensus.ValidatorsToPubKeys(vals))
	if err != nil {
		return nil, nil, err
	}

	powHash, err = hs.VotePowers(tmconsensus.ValidatorsToVotePowers(vals))
	if err != nil {
		return nil, nil, err
	}

	return keyHash, powHash, nil
}

func (a *valShuffleApp) stateHash(height uint64, keyHash, powHash []byte) []byte {
	h, err := blake2b.New(32, nil)
	if err != nil {
		panic(fmt.Errorf(
			"failed to create blake2b hasher: %w", err,
		))
	}

	// Write the height first.
	if err := binary.Write(h, binary.BigEndian, height); err != nil {
		panic(fmt.Errorf(
			"failed to write height to hasher: %w", err,
		))
	}

	// Then append the key and power hashes.
	_, _ = h.Write(keyHash)
	_, _ = h.Write(powHash)

	return h.Sum(nil)
}

type valShuffleConsensusStrategy struct {
	Log    *slog.Logger
	PubKey gcrypto.PubKey

	HashScheme tmconsensus.HashScheme

	mu                sync.Mutex
	expProposerPubKey gcrypto.PubKey
	expProposerIndex  int
	curH              uint64
	curR              uint32
}

func (s *valShuffleConsensusStrategy) EnterRound(ctx context.Context, rv tmconsensus.RoundView, proposalOut chan<- tmconsensus.Proposal) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.curH = rv.Height
	s.curR = rv.Round

	// Pseudo-copy of the modulo round robin proposer selection strategy that the v0.2 code used.
	s.expProposerIndex = (int(rv.Height) + int(rv.Round)) % len(rv.Validators)
	s.expProposerPubKey = rv.Validators[s.expProposerIndex].PubKey

	if !s.expProposerPubKey.Equal(s.PubKey) {
		// We are not the proposer.
		return nil
	}

	// If we are the proposer, we set the app data ID as the raw data of height, hash, hash
	keyHash, powHash, err := validatorHashes(rv.Validators, s.HashScheme)
	if err != nil {
		return fmt.Errorf("failed to get validator hashes: %w", err)
	}

	appData := binary.BigEndian.AppendUint64(nil, rv.Height)
	appData = append(appData, keyHash...)
	appData = append(appData, powHash...)
	proposalOut <- tmconsensus.Proposal{
		DataID: string(appData),
	}

	return nil
}

func (s *valShuffleConsensusStrategy) ConsiderProposedBlocks(
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

		keyHash, powHash, err := validatorHashes(pb.Block.Validators, s.HashScheme)
		if err != nil {
			return "", err
		}

		expDataID := binary.BigEndian.AppendUint64(nil, s.curH)
		expDataID = append(expDataID, keyHash...)
		expDataID = append(expDataID, powHash...)

		if !bytes.Equal(pb.Block.DataID, expDataID[:]) {
			return "", nil
		}

		s.Log.Info("Prevote in favor of block", "hash", glog.Hex(pb.Block.Hash), "height", s.curH)
		return string(pb.Block.Hash), nil
	}

	// Didn't see a proposed block from the expected proposer.
	s.Log.Info("Prevote not ready", "height", s.curH)
	return "", tmconsensus.ErrProposedBlockChoiceNotReady
}

func (s *valShuffleConsensusStrategy) ChooseProposedBlock(ctx context.Context, pbs []tmconsensus.ProposedBlock) (string, error) {
	// Follow the ConsiderProposedBlocks logic...
	hash, err := s.ConsiderProposedBlocks(ctx, pbs, tmconsensus.ConsiderProposedBlocksReason{})
	if err == tmconsensus.ErrProposedBlockChoiceNotReady {
		// ... and if there is no choice ready, then vote nil.
		return "", nil
	}
	return hash, err
}

func (s *valShuffleConsensusStrategy) DecidePrecommit(ctx context.Context, vs tmconsensus.VoteSummary) (string, error) {
	maj := tmconsensus.ByzantineMajority(vs.AvailablePower)
	if pow := vs.PrevoteBlockPower[vs.MostVotedPrevoteHash]; pow >= maj {
		return vs.MostVotedPrevoteHash, nil
	}

	// Didn't reach consensus on one block; automatically precommit nil.
	return "", nil
}
