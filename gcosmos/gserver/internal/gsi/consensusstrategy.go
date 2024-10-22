package gsi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"cosmossdk.io/core/transaction"
	"cosmossdk.io/server/v2/appmanager"
	"github.com/gordian-engine/gordian/gcosmos/gserver/internal/gsbd"
	"github.com/gordian-engine/gordian/gcrypto"
	"github.com/gordian-engine/gordian/internal/gchan"
	"github.com/gordian-engine/gordian/internal/glog"
	"github.com/gordian-engine/gordian/tm/tmconsensus"
)

type ConsensusStrategy struct {
	log *slog.Logger

	am appmanager.AppManager[transaction.Tx]

	txBuf *SDKTxBuf

	signerPubKey gcrypto.PubKey

	provider gsbd.Provider

	curH uint64
	curR uint32

	pbdr *PBDRetriever

	bdrCache *gsbd.RequestCache

	proposerSelection ProposerSelectionFunc
}

// ProposerSelectionFunc decides which validator
// is supposed to be proposing at a given height and round.
//
// The [ConsensusStrategy] currently only inspects when deciding whether the current process
// should propose a block.
type ProposerSelectionFunc func(
	ctx context.Context,
	height uint64, round uint32,
	curValSet tmconsensus.ValidatorSet,
) (choice tmconsensus.Validator)

// DefaultProposerSelection is the default [ProposerSelectionFunc]
// when otherwise unspecified in the [ConsensusStrategyConfig].
func DefaultProposerSelection(
	_ context.Context, h uint64, r uint32, valSet tmconsensus.ValidatorSet,
) tmconsensus.Validator {
	// Very naive round-robin-ish proposer selection.
	proposerIdx := (int(h) + int(r)) % len(valSet.Validators)

	return valSet.Validators[proposerIdx]
}

// ConsensusStrategyConfig is the configuration to pass to [NewConsensusStrategy].
type ConsensusStrategyConfig struct {
	// How to choose the proposer for a given round.
	// If nil, defaults to [DefaultProposerSelection].
	ProposerSelection ProposerSelectionFunc

	// Needed to simulate transactions.
	AppManager appmanager.AppManager[transaction.Tx]

	// To get the pending transactions when proposing a block.
	// Maybe could be nil if signer is nil
	// and we know we will never propose a block?
	TxBuf *SDKTxBuf

	// The public key of our signer.
	// May be nil.
	SignerPubKey gcrypto.PubKey

	// How to provide our proposed block data to other network participants.
	BlockDataProvider gsbd.Provider

	ProposedBlockDataRetriever *PBDRetriever

	// The request cache indicating what block data requests are in flight
	// and which ones have already been completed.
	// Not yet entirely used.
	BlockDataRequestCache *gsbd.RequestCache
}

func NewConsensusStrategy(
	ctx context.Context,
	log *slog.Logger,
	cfg ConsensusStrategyConfig,
) *ConsensusStrategy {
	cs := &ConsensusStrategy{
		log: log,
		am:  cfg.AppManager,

		txBuf: cfg.TxBuf,

		signerPubKey: cfg.SignerPubKey,

		provider: cfg.BlockDataProvider,

		pbdr: cfg.ProposedBlockDataRetriever,

		bdrCache: cfg.BlockDataRequestCache,

		proposerSelection: cfg.ProposerSelection,
	}

	if cs.proposerSelection == nil {
		cs.proposerSelection = DefaultProposerSelection
	}

	return cs
}

func (s *ConsensusStrategy) Wait() {
	// The pbdr is an implementation detail of the consensus strategy,
	// so we don't expose it directly.
	// The pbdr is the only background work created here.
	s.pbdr.Wait()
}

// BlockAnnotation is the data encoded as a block annotation.
// Block annotations are persisted on-chain,
// unlike proposal annotations which are not persisted to chain.
type BlockAnnotation struct {
	// The time that the proposer said that it proposed the block.
	// S suffix indicating string, to allow the Time method to exist without a name conflict.
	TimeS string `json:"Time"`
}

// Time parses and returns the time value from the annotation.
func (ba BlockAnnotation) Time() (time.Time, error) {
	return time.Parse(time.RFC3339, ba.TimeS)
}

func (c *ConsensusStrategy) EnterRound(
	ctx context.Context,
	rv tmconsensus.RoundView,
	proposalOut chan<- tmconsensus.Proposal,
) error {
	// Track the current height and round for later when we get to voting.
	c.curH = rv.Height
	c.curR = rv.Round

	if c.signerPubKey == nil {
		// Not participating, stop early.
	}

	proposingVal := c.proposerSelection(ctx, rv.Height, rv.Round, rv.ValidatorSet)
	weShouldPropose := proposingVal.PubKey.Equal(c.signerPubKey)
	if !weShouldPropose {
		return nil
	}

	if proposalOut == nil {
		// A nil proposalOut channel is conditionally okay:
		// if we proposed a header, and then the process was restarted,
		// the mirror may already have our header.
		// In this case, stop early because there is nothing new to propose.
		for _, ph := range rv.ProposedHeaders {
			if proposingVal.PubKey.Equal(ph.ProposerPubKey) {
				return nil
			}
		}

		panic(errors.New(
			"BUG: proposalOut channel was nil when we were supposed to propose",
		))
	}

	ba, err := json.Marshal(BlockAnnotation{
		// TODO: this needs something much more sophisticated than just time.Now.
		TimeS: time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		return fmt.Errorf("failed to marshal block driver annotations: %w", err)
	}

	pendingTxs := c.txBuf.Buffered(ctx, nil)

	var blockDataID string
	var pda []byte
	if len(pendingTxs) == 0 {
		blockDataID = gsbd.DataID(c.curH, c.curR, 0, nil)
	} else {
		res, err := c.provider.Provide(ctx, c.curH, c.curR, pendingTxs)
		if err != nil {
			return fmt.Errorf("failed to provide block data: %w", err)
		}

		pda, err = json.Marshal(ProposalDriverAnnotation{
			Locations: res.Addrs,
		})
		if err != nil {
			return fmt.Errorf("failed to marshal proposal driver annotations: %w", err)
		}

		blockDataID = res.DataID

		// We are proposing this data, so mark it as locally available.
		c.bdrCache.SetImmediatelyAvailable(blockDataID, pendingTxs, res.Encoded)
	}

	if !gchan.SendC(
		ctx, c.log,
		proposalOut, tmconsensus.Proposal{
			DataID: blockDataID,
			BlockAnnotations: tmconsensus.Annotations{
				Driver: ba,
			},
			ProposalAnnotations: tmconsensus.Annotations{
				Driver: pda,
			},
		},
		"sending proposal to engine",
	) {
		return context.Cause(ctx)
	}

	return nil
}

// ConsiderProposedBlocks effectively chooses the first valid block in phs.
func (c *ConsensusStrategy) ConsiderProposedBlocks(
	ctx context.Context,
	phs []tmconsensus.ProposedHeader,
	_ tmconsensus.ConsiderProposedBlocksReason,
) (string, error) {
PH_LOOP:
	for _, ph := range phs {
		// TODO: handle a particular proposed block being excluded from a round,
		// presumably because we got its data and we chose not to accept it.
		const excluded = false
		if excluded {
			continue
		}

		if ph.Header.Height != c.curH {
			c.log.Debug(
				"Ignoring proposed block due to height mismatch",
				"want", c.curH, "got", ph.Header.Height,
			)
			continue
		}
		if ph.Round != c.curR {
			c.log.Debug(
				"Ignoring proposed block due to round mismatch",
				"h", c.curH,
				"want", c.curR, "got", ph.Round,
			)
			continue
		}

		h, r, nTxs, _, _, err := gsbd.ParseDataID(string(ph.Header.DataID))
		if err != nil {
			c.log.Debug(
				"Ignoring proposed block due to unparseable app data ID",
				"h", c.curH, "r", c.curR,
				"block_hash", glog.Hex(ph.Header.Hash),
				"err", err,
			)
			continue
		}
		if h != c.curH {
			c.log.Debug(
				"Ignoring proposed block due to wrong height in app data ID",
				"h", c.curH, "r", c.curR,
				"got_h", h,
			)
			continue
		}
		if r != c.curR {
			c.log.Debug(
				"Ignoring proposed block due to wrong round in app data ID",
				"h", c.curH, "r", c.curR,
				"got_r", r,
			)
			continue
		}

		if nTxs != 0 {
			bdr, ok := c.bdrCache.Get(string(ph.Header.DataID))
			if !ok {
				// This must be the first time we've encountered this data ID,
				// so let's ensure we are working on getting it.
				if err := c.pbdr.Retrieve(ctx, string(ph.Header.DataID), ph.Annotations.Driver); err != nil {
					c.log.Warn(
						"Failed to initiate retrieval of proposed data",
						"data_id", string(ph.Header.DataID),
						"err", err,
					)
				}

				// Continuing regardless of whether the retrieve call succeeded.
				continue
			}

			// There is a request. Is the data ready?
			select {
			case <-bdr.Ready:
				// Yes. Keep working.
			default:
				continue
			}

			txs := bdr.Transactions

			// We do have the transactions.
			// Can they be applied?
			// We know we have at least one transaction,
			// and we needs its result to seed subsequent transactions starting state.
			txRes, state, err := c.am.Simulate(ctx, txs[0])
			if err != nil {
				c.log.Debug(
					"Ignoring proposed block due to failure to simulate",
					"err", err,
				)
				continue
			}
			if txRes.Error != nil {
				txHash := txs[0].Hash()
				c.log.Debug(
					"Ignoring proposed block due to failure to apply transaction",
					"tx_hash", glog.Hex(txHash[:]),
					"err", err,
				)
				continue
			}

			for _, tx := range txs[1:] {
				txRes, state = c.am.SimulateWithState(ctx, state, tx)
				if txRes.Error != nil {
					txHash := tx.Hash()
					c.log.Debug(
						"Ignoring proposed block due to failure to apply transaction",
						"tx_hash", glog.Hex(txHash[:]),
						"err", err,
					)
					continue PH_LOOP
				}
			}
		}

		var ba BlockAnnotation
		if err := json.Unmarshal(ph.Header.Annotations.Driver, &ba); err != nil {
			c.log.Debug(
				"Ignoring proposed block due to error extracting block annotation",
				"h", c.curH, "r", c.curR, "err", err,
			)
			continue
		}

		bt, err := ba.Time()
		if err != nil {
			c.log.Debug(
				"Ignoring proposed block due to error extracting block time from annotation",
				"h", c.curH, "r", c.curR, "err", err,
			)
			continue
		}

		if bt.After(time.Now()) {
			c.log.Debug(
				"Ignoring proposed block due to block time in the future",
				"h", c.curH, "r", c.curR, "err", err,
			)
			continue
		}

		return string(ph.Header.Hash), nil
	}

	return "", tmconsensus.ErrProposedBlockChoiceNotReady
}

func (c *ConsensusStrategy) ChooseProposedBlock(
	ctx context.Context,
	phs []tmconsensus.ProposedHeader,
) (string, error) {
	h, err := c.ConsiderProposedBlocks(ctx, phs, tmconsensus.ConsiderProposedBlocksReason{})
	if err == tmconsensus.ErrProposedBlockChoiceNotReady {
		return "", nil
	}
	if err != nil {
		return "", err
	}

	return h, nil
}

func (c *ConsensusStrategy) DecidePrecommit(
	ctx context.Context,
	vs tmconsensus.VoteSummary,
) (string, error) {
	maj := tmconsensus.ByzantineMajority(vs.AvailablePower)
	if pow := vs.PrevoteBlockPower[vs.MostVotedPrevoteHash]; pow >= maj {
		return vs.MostVotedPrevoteHash, nil
	}

	// Didn't reach consensus on one block; automatically precommit nil.
	return "", nil
}
