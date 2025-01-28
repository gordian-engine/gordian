package tmconsensus

import (
	"context"
	"errors"
)

// Proposal is the data an application needs to provide,
// for the engine to compose a [ProposedHeader].
type Proposal struct {
	// The ID of the data inside the block.
	// This value will be used to set [Block.DataID].
	DataID string // TODO: this should switch back to []byte.

	// Respectively sets [ProposedBlock.Annotations] and [Block.Annotations].
	ProposalAnnotations, BlockAnnotations Annotations
}

// ConsiderProposedBlocksReason is an argument in [ConsensusStrategy.ConsiderProposedBlocks].
// It is a hint to the [ConsensusStrategy] about anything new to check during this call,
// compared to the previous call.
// The state of what has been sent previously is already available in the engine,
// so the consensus strategy does not need to track this state on its own.
type ConsiderProposedBlocksReason struct {
	// The hashes of the new blocks since the previous call to ConsiderProposedBlocks.
	NewProposedBlocks []string

	// Any data IDs that have been marked as updated
	// since the previous call to ConsensusStrategy.
	// The data IDs are provided in an arbitrary order.
	UpdatedBlockDataIDs []string

	// Indicates whether >2/3 of voting power is present,
	// but does not necessarily indicate that the voting power
	// is for a single block or nil; voting may be split.
	MajorityVotingPowerPresent bool
}

// ConsensusStrategy determines how a state machine proposes blocks
// and what blocks to prevote or precommit.
type ConsensusStrategy interface {
	// The state machine calls this method synchronously when it begins a round.
	// It is possible that the state machine is lagging behind the network,
	// in which case there may be existing proposed blocks or votes in rv.
	//
	// The state machine does not call EnterRound if it is catching up
	// on blocks that are already committed to the chain.
	//
	// If the application is going to propose a block for this round,
	// it must publish the proposal information to the proposalOut channel;
	// the state machine will compose that information into a proposed block.
	EnterRound(ctx context.Context, rv RoundView, proposalOut chan<- Proposal) error

	// ConsiderProposedBlocks is called when new proposed headers arrive,
	// or when new block data has arrived,
	// so long as the proposed block timeout has not yet elapsed.
	//
	// While most of the rest of the tm package refers to proposed headers,
	// this method includes the words "proposed blocks" because it is assumed that
	// the consensus strategy cannot decide whether to vote for the header
	// until it has the actual block data to go with it.
	//
	// The reason argument is a hint to the consensus strategy
	// about which information is new in this call,
	// compared to the previous call.
	//
	// If the returned error is nil, the returned string is assumed to be the block hash to prevote.
	// The empty string indicates a prevote for nil.
	//
	// If the consensus strategy wants to wait longer before making a selection,
	// it must return [ErrProposedBlockChoiceNotReady].
	// Any other error is fatal.
	ConsiderProposedBlocks(
		ctx context.Context,
		phs []ProposedHeader,
		reason ConsiderProposedBlocksReason,
	) (string, error)

	// ChooseProposedBlock is called when the state machine's proposal delay has elapsed.
	// The phs slice may be empty.
	//
	// ChooseProposedBlock must return the hash of the block to vote for.
	// Under certain circumstances (like Proof of Lock),
	// the returned hash may not be present in the slice of proposed blocks.
	//
	// The state machine calls this in a background goroutine,
	// so the method may block as long as necessary.
	// Nonetheless, the method must respect context cancellation.
	ChooseProposedBlock(ctx context.Context, phs []ProposedHeader) (string, error)

	// DecidePrecommit is called when prevoting is finished
	// and the state machine needs to submit a precommit.
	//
	// The returned string value is the block hash to precommit.
	// The empty string indicates a precommit for nil.
	// A returned error is fatal.
	//
	// NOTE: this method will likely change with vote extensions.
	DecidePrecommit(ctx context.Context, vs VoteSummary) (string, error)
}

// ErrProposedBlockChoiceNotReady is a sentinel error the [ConsensusStrategy] must return
// from its ConsiderProposedBlocks method, if it is not ready to choose a proposed block.
var ErrProposedBlockChoiceNotReady = errors.New("not ready to choose proposed block")
