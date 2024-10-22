package tmintegration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"strconv"
	"sync"

	"github.com/gordian-engine/gordian/gcrypto"
	"github.com/gordian-engine/gordian/tm/tmconsensus"
	"github.com/gordian-engine/gordian/tm/tmdriver"
)

type identityApp struct {
	FinalizeResponses chan tmdriver.FinalizeBlockResponse

	log *slog.Logger

	idx int

	done chan struct{}
}

func newIdentityApp(
	ctx context.Context,
	log *slog.Logger,
	idx int,
	initChainRequests <-chan tmdriver.InitChainRequest,
	finalizeBlockRequests <-chan tmdriver.FinalizeBlockRequest,
) *identityApp {
	a := &identityApp{
		FinalizeResponses: make(chan tmdriver.FinalizeBlockResponse),

		log:  log,
		idx:  idx,
		done: make(chan struct{}),
	}

	go a.kernel(ctx, initChainRequests, finalizeBlockRequests)

	return a
}

func (a *identityApp) Wait() {
	<-a.done
}

func (a *identityApp) kernel(
	ctx context.Context,
	initChainRequests <-chan tmdriver.InitChainRequest,
	finalizeBlockRequests <-chan tmdriver.FinalizeBlockRequest,
) {
	defer close(a.done)

	var vals []tmconsensus.Validator

	// Assume we always need to initialize the chain at startup.
	select {
	case <-ctx.Done():
		a.log.Info("Stopping due to context cancellation", "cause", context.Cause(ctx))
		return

	case req := <-initChainRequests:
		// The validators don't change in this app, so just hold on to the initial ones
		// for any finalization responses later.
		vals = req.Genesis.GenesisValidatorSet.Validators

		// Ignore genesis app state, start with empty state.

		stateHash := sha256.Sum256([]byte(""))
		select {
		case req.Resp <- tmdriver.InitChainResponse{
			AppStateHash: stateHash[:],

			// Omitting validators since we want to match the input.
		}:
			// Okay.
		case <-ctx.Done():
			return
		}
	}

	// After that, we handle an indeterminate number of finalize block requests.
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

				Validators: vals,

				// TODO: this would be more meaningful if it were a more properly calculated hash.
				AppStateHash: req.Header.DataID,
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

type identityConsensusStrategy struct {
	Log    *slog.Logger
	PubKey gcrypto.PubKey

	// Round-specific values.
	mu                sync.Mutex
	expProposerPubKey gcrypto.PubKey
	expProposerIndex  int
	curH              uint64
	curR              uint32
}

func (s *identityConsensusStrategy) EnterRound(ctx context.Context, rv tmconsensus.RoundView, proposalOut chan<- tmconsensus.Proposal) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.curH = rv.Height
	s.curR = rv.Round

	s.Log.Info("Entering round", "h", s.curH, "r", s.curR)

	// Pseudo-copy of the modulo round robin proposer selection strategy that the v0.2 code used.
	s.expProposerIndex = (int(rv.Height) + int(rv.Round)) % len(rv.ValidatorSet.Validators)
	s.expProposerPubKey = rv.ValidatorSet.Validators[s.expProposerIndex].PubKey

	if s.expProposerPubKey.Equal(s.PubKey) {
		appData := fmt.Sprintf("Height: %d; Round: %d", s.curH, s.curR)
		dataHash := sha256.Sum256([]byte(appData))
		proposalOut <- tmconsensus.Proposal{
			DataID: string(dataHash[:]),

			// Just to exercise the annotations, set them to the ascii value of the proposer index,
			// prefixed with a "p" or "b" for proposal or block.
			ProposalAnnotations: tmconsensus.Annotations{
				Driver: strconv.AppendInt([]byte("p"), int64(s.expProposerIndex), 10),
			},
			BlockAnnotations: tmconsensus.Annotations{
				Driver: strconv.AppendInt([]byte("b"), int64(s.expProposerIndex), 10),
			},
		}
	}

	return nil
}

func (s *identityConsensusStrategy) ConsiderProposedBlocks(
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

		expPA := strconv.AppendInt([]byte("p"), int64(s.expProposerIndex), 10)
		if !bytes.Equal(ph.Annotations.Driver, expPA) {
			return "", nil
		}

		expBA := strconv.AppendInt([]byte("b"), int64(s.expProposerIndex), 10)
		if !bytes.Equal(ph.Header.Annotations.Driver, expBA) {
			return "", nil
		}

		// Found a proposed block from the expected proposer.
		expBlockData := fmt.Sprintf("Height: %d; Round: %d", s.curH, s.curR)
		expDataID := sha256.Sum256([]byte(expBlockData))

		if !bytes.Equal(ph.Header.DataID, expDataID[:]) {
			return "", nil
		}

		return string(ph.Header.Hash), nil
	}

	// Didn't see a proposed block from the expected proposer.
	return "", tmconsensus.ErrProposedBlockChoiceNotReady
}

func (s *identityConsensusStrategy) ChooseProposedBlock(ctx context.Context, phs []tmconsensus.ProposedHeader) (string, error) {
	// Follow the ConsiderProposedBlocks logic...
	hash, err := s.ConsiderProposedBlocks(ctx, phs, tmconsensus.ConsiderProposedBlocksReason{})
	if err == tmconsensus.ErrProposedBlockChoiceNotReady {
		// ... and if there is no choice ready, then vote nil.
		return "", nil
	}
	return hash, err
}

func (s *identityConsensusStrategy) DecidePrecommit(ctx context.Context, vs tmconsensus.VoteSummary) (string, error) {
	maj := tmconsensus.ByzantineMajority(vs.AvailablePower)
	if pow := vs.PrevoteBlockPower[vs.MostVotedPrevoteHash]; pow >= maj {
		return vs.MostVotedPrevoteHash, nil
	}

	// Didn't reach consensus on one block; automatically precommit nil.
	return "", nil
}
