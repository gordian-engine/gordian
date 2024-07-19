package gsi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"cosmossdk.io/core/transaction"
	"github.com/rollchains/gordian/internal/gchan"
	"github.com/rollchains/gordian/tm/tmconsensus"
)

type ConsensusStrategy[T transaction.Tx] struct {
	log *slog.Logger

	d *Driver[T]

	curH uint64
	curR uint32
}

func NewConsensusStrategy[T transaction.Tx](
	log *slog.Logger,
	d *Driver[T],
) *ConsensusStrategy[T] {
	return &ConsensusStrategy[T]{
		log: log,
		d:   d,
	}
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

func (c *ConsensusStrategy[T]) EnterRound(
	ctx context.Context,
	rv tmconsensus.RoundView,
	proposalOut chan<- tmconsensus.Proposal,
) error {
	// Track the current height and round for later when we get to voting.
	c.curH = rv.Height
	c.curR = rv.Round

	// TODO: a better system of determining whether we should propose this block.
	const weShouldPropose = true
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
		return errors.New("BUG: failed to json marshal block annotation")
	}

	if !gchan.SendC(
		ctx, c.log,
		proposalOut, tmconsensus.Proposal{
			AppDataID:       fmt.Sprintf("%d/%d", rv.Height, rv.Round),
			BlockAnnotation: ba,
		},
		"sending proposal to engine",
	) {
		return context.Cause(ctx)
	}
	return nil
}

func (c *ConsensusStrategy[T]) ConsiderProposedBlocks(
	ctx context.Context,
	pbs []tmconsensus.ProposedBlock,
) (string, error) {
	expAppDataID := fmt.Sprintf("%d/%d", c.curH, c.curR)
	for _, pb := range pbs {
		if pb.Block.Height != c.curH {
			continue
		}
		if pb.Round != c.curR {
			continue
		}

		if string(pb.Block.DataID) != expAppDataID {
			continue
		}

		ba, err := BlockAnnotationFromBytes(pb.Annotations.App)
		if err != nil {
			continue
		}

		bt, err := ba.BlockTimeAsTime()
		if err != nil {
			continue
		}

		if bt.After(time.Now()) {
			continue
		}

		return string(pb.Block.Hash), nil
	}

	return "", tmconsensus.ErrProposedBlockChoiceNotReady
}

func (c *ConsensusStrategy[T]) ChooseProposedBlock(
	ctx context.Context,
	pbs []tmconsensus.ProposedBlock,
) (string, error) {
	h, err := c.ConsiderProposedBlocks(ctx, pbs)
	if err == tmconsensus.ErrProposedBlockChoiceNotReady {
		return "", nil
	}

	return h, nil
}

func (c *ConsensusStrategy[T]) DecidePrecommit(
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
