package tmconsensustest

import (
	"context"

	"github.com/rollchains/gordian/tm/tmconsensus"
)

// NopConsensusStrategy is a [tmconsensus.ConsensusStrategy] that always prevotes and precommits nil.
//
// This should only be used as a placeholder in tests that require the presence of,
// but do not interact with, a consensus strategy.
type NopConsensusStrategy struct{}

func (NopConsensusStrategy) EnterRound(context.Context, tmconsensus.RoundView, chan<- tmconsensus.Proposal) error {
	return nil
}

func (NopConsensusStrategy) ConsiderProposedBlocks(
	ctx context.Context,
	pbs []tmconsensus.ProposedBlock,
	reason tmconsensus.ConsiderProposedBlocksReason,
) (string, error) {
	return "", nil
}

func (NopConsensusStrategy) ChooseProposedBlock(ctx context.Context, pbs []tmconsensus.ProposedBlock) (string, error) {
	return "", nil
}

func (NopConsensusStrategy) DecidePrecommit(ctx context.Context, vs tmconsensus.VoteSummary) (string, error) {
	return "", nil
}
