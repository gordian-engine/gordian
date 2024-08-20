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
	"github.com/rollchains/gordian/gcosmos/gserver/internal/gsbd"
	"github.com/rollchains/gordian/gcrypto"
	"github.com/rollchains/gordian/gdriver/gdatapool"
	"github.com/rollchains/gordian/internal/gchan"
	"github.com/rollchains/gordian/internal/glog"
	"github.com/rollchains/gordian/tm/tmconsensus"
)

type ConsensusStrategy struct {
	log *slog.Logger

	d *Driver

	am appmanager.AppManager[transaction.Tx]

	txBuf *SDKTxBuf

	signer gcrypto.Signer

	provider gsbd.Provider

	curH uint64
	curR uint32

	pool *gdatapool.Pool[[]transaction.Tx]
}

func NewConsensusStrategy(
	// TODO: this needs to switch to a struct for input arguments.
	ctx context.Context,
	log *slog.Logger,
	d *Driver,
	am appmanager.AppManager[transaction.Tx],
	signer gcrypto.Signer,
	txBuf *SDKTxBuf,
	blockDataProvider gsbd.Provider,
	pool *gdatapool.Pool[[]transaction.Tx],
) *ConsensusStrategy {
	// TODO: don't hardcode nWorkers.
	const nWorkers = 4

	return &ConsensusStrategy{
		log:    log,
		d:      d,
		am:     am,
		signer: signer,

		txBuf: txBuf,

		provider: blockDataProvider,

		pool: pool,
	}
}

func (s *ConsensusStrategy) Wait() {
	// The pool is an implementation detail of the consensus strategy,
	// so we don't expose it directly.
	// The pool is the only background work created here.
	s.pool.Wait()
}

type BlockAnnotation struct {
	BlockTime string `json:"block_time"`
}

func NewBlockAnnotation(blockTime time.Time) ([]byte, error) {
	ba := BlockAnnotation{
		BlockTime: blockTime.Format(time.RFC3339Nano),
	}
	return json.Marshal(ba)
}

func BlockAnnotationFromBytes(b []byte) (BlockAnnotation, error) {
	var ba BlockAnnotation
	err := json.Unmarshal(b, &ba)
	return ba, err
}

func (ba BlockAnnotation) BlockTimeAsTime() (time.Time, error) {
	return time.Parse(time.RFC3339, ba.BlockTime)
}

func (c *ConsensusStrategy) EnterRound(
	ctx context.Context,
	rv tmconsensus.RoundView,
	proposalOut chan<- tmconsensus.Proposal,
) error {
	// Track the current height and round for later when we get to voting.
	c.curH = rv.Height
	c.curR = rv.Round

	c.pool.EnterRound(ctx, rv.Height, rv.Round)

	// Very naive round-robin-ish proposer selection.
	proposerIdx := (int(rv.Height) + int(rv.Round)) % len(rv.Validators)

	proposingVal := rv.Validators[proposerIdx]
	weShouldPropose := proposingVal.PubKey.Equal(c.signer.PubKey())
	if !weShouldPropose {
		return nil
	}

	if proposalOut == nil {
		panic(errors.New(
			"BUG: proposalOut channel was nil when we were supposed to propose",
		))
	}

	ba, err := NewBlockAnnotation(time.Now())
	if err != nil {
		return fmt.Errorf("failed to create block annotation: %w", err)
	}

	pendingTxs := c.txBuf.Buffered(ctx, nil)

	var blockDataID string
	var pda []byte
	if len(pendingTxs) == 0 {
		blockDataID = gsbd.DataID(c.curH, c.curR, nil)
	} else {
		res, err := c.provider.Provide(ctx, c.curH, c.curR, pendingTxs)
		if err != nil {
			return fmt.Errorf("failed to provide block data: %w", err)
		}

		pda, err = json.Marshal(ProposalDriverAnnotation{
			DataSize:  res.DataSize,
			Locations: res.Addrs,
		})
		if err != nil {
			return fmt.Errorf("failed to marshal proposal driver annotations: %w", err)
		}

		blockDataID = res.DataID
	}

	// We are proposing this data, so mark it as locally available.
	c.pool.SetAvailable(blockDataID, pda, pendingTxs)

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

// ConsiderProposedBlocks effectively chooses the first valid block in pbs.
func (c *ConsensusStrategy) ConsiderProposedBlocks(
	ctx context.Context,
	pbs []tmconsensus.ProposedBlock,
	_ tmconsensus.ConsiderProposedBlocksReason,
) (string, error) {
PB_LOOP:
	for _, pb := range pbs {
		// TODO: handle a particular proposed block being excluded from a round,
		// presumably because we got its data and we chose not to accept it.
		const excluded = false
		if excluded {
			continue
		}

		if pb.Block.Height != c.curH {
			c.log.Debug(
				"Ignoring proposed block due to height mismatch",
				"want", c.curH, "got", pb.Block.Height,
			)
			continue
		}
		if pb.Round != c.curR {
			c.log.Debug(
				"Ignoring proposed block due to round mismatch",
				"h", c.curH,
				"want", c.curR, "got", pb.Round,
			)
			continue
		}

		h, r, nTxs, _, err := gsbd.ParseDataID(string(pb.Block.DataID))
		if err != nil {
			c.log.Debug(
				"Ignoring proposed block due to unparseable app data ID",
				"h", c.curH, "r", c.curR,
				"block_hash", glog.Hex(pb.Block.Hash),
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
			// Unconditional call to Need first.
			// (TODO: we should only call this when the reason includes this as a new block, right?)
			c.pool.Need(h, r, string(pb.Block.DataID), pb.Annotations.Driver)

			txs, err, ready := c.pool.Have(string(pb.Block.DataID))
			if !ready {
				// Request is in flight.
				// Can't decide on this block yet.
				continue
			}

			// If it's ready and it's an error, just silently skip it for now.
			if err != nil {
				continue
			}

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
					continue PB_LOOP
				}
			}
		}

		ba, err := BlockAnnotationFromBytes(pb.Block.Annotations.Driver)
		if err != nil {
			c.log.Debug(
				"Ignoring proposed block due to error extracting block annotation",
				"h", c.curH, "r", c.curR, "err", err,
			)
			continue
		}

		bt, err := ba.BlockTimeAsTime()
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

		return string(pb.Block.Hash), nil
	}

	return "", tmconsensus.ErrProposedBlockChoiceNotReady
}

func (c *ConsensusStrategy) ChooseProposedBlock(
	ctx context.Context,
	pbs []tmconsensus.ProposedBlock,
) (string, error) {
	h, err := c.ConsiderProposedBlocks(ctx, pbs, tmconsensus.ConsiderProposedBlocksReason{})
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
