package tmgossip

import (
	"context"
	"errors"
	"log/slog"
	"runtime/trace"

	"github.com/bits-and-blooms/bitset"
	"github.com/gordian-engine/gordian/internal/gchan"
	"github.com/gordian-engine/gordian/tm/tmconsensus"
	"github.com/gordian-engine/gordian/tm/tmengine/tmelink"
	"github.com/gordian-engine/gordian/tm/tmp2p"
)

// ChattyStrategy is a naive [Strategy] that
// broadcasts every round state update on the p2p network.
//
// This should not be used in production,
// as it is totally inefficient in terms of bandwidth.
type ChattyStrategy struct {
	log *slog.Logger

	cb tmp2p.ConsensusBroadcaster

	startCh    chan (<-chan tmelink.NetworkViewUpdate)
	kernelDone chan struct{}
}

func NewChattyStrategy(
	ctx context.Context,
	log *slog.Logger,
	cb tmp2p.ConsensusBroadcaster,
) *ChattyStrategy {
	s := &ChattyStrategy{
		log: log,

		cb: cb,

		startCh:    make(chan (<-chan tmelink.NetworkViewUpdate), 1),
		kernelDone: make(chan struct{}),
	}

	go s.kernel(ctx)
	return s
}

func (s *ChattyStrategy) Wait() {
	<-s.kernelDone
}

func (s *ChattyStrategy) Start(link <-chan tmelink.NetworkViewUpdate) {
	s.startCh <- link
	close(s.startCh)
}

func (s *ChattyStrategy) kernel(ctx context.Context) {
	defer close(s.kernelDone)

	ctx, task := trace.NewTask(ctx, "ChattyStrategy.kernel")
	defer task.End()

	// Block for the start signal.
	updates, ok := gchan.RecvC(
		ctx, s.log,
		s.startCh,
		"waiting for start signal",
	)
	if !ok {
		// Already logged in RecvC.
		return
	}

	u, ok := gchan.RecvC(
		ctx, s.log,
		updates,
		"waiting for first update",
	)
	if !ok {
		return
	}

	var prevVotingView, prevCommittingView, prevNextRoundView tmconsensus.VersionedRoundView

	if u.Voting == nil {
		panic(errors.New("TODO: handle nil voting view on first update"))
	}

	// Broadcast everything in the first update.
	if !s.broadcastAll(ctx, *u.Voting) {
		return
	}
	prevVotingView = *u.Voting

	if u.Committing != nil {
		if !s.broadcastAll(ctx, *u.Committing) {
			return
		}
		prevCommittingView = *u.Committing
	}

	if u.NextRound != nil {
		if !s.broadcastAll(ctx, *u.NextRound) {
			return
		}
		prevNextRoundView = *u.NextRound
	}

	for {
		select {
		case <-ctx.Done():
			s.log.Info(
				"Quitting due to context cancellation",
				"cause", context.Cause(ctx),
			)
			return
		case u := <-updates:
			// Ordered from what should be earliest round to latest,
			// which ought to be more stable for any peers who are missing any of this information.
			// (Although there is no guarantee that the messages will be processed in order anyways.)

			if u.Committing != nil {
				if !s.broadcastViewDiff(ctx, prevCommittingView, *u.Committing) {
					return
				}

				prevCommittingView = *u.Committing
			}

			if u.NilVotedRound != nil {
				// We only need to share the precommits in this case;
				// the prevotes and proposed blocks are irrelevant to noting the round failed to commit.
				if !s.broadcastPrecommits(ctx, *u.NilVotedRound) {
					return
				}
			}

			if u.Voting != nil {
				if !s.broadcastViewDiff(ctx, prevVotingView, *u.Voting) {
					return
				}
				prevVotingView = *u.Voting
			}

			if u.NextRound != nil {
				if !s.broadcastViewDiff(ctx, prevNextRoundView, *u.NextRound) {
					return
				}

				prevNextRoundView = *u.NextRound
			}
		}
	}
}

func (s *ChattyStrategy) broadcastViewDiff(ctx context.Context, prev, cur tmconsensus.VersionedRoundView) bool {
	if cur.Height == prev.Height && cur.Round == prev.Round {
		return s.broadcastUpdatesOnly(ctx, prev, cur)
	}

	return s.broadcastAll(ctx, cur)
}

func (s *ChattyStrategy) broadcastProposedBlocks(ctx context.Context, view tmconsensus.VersionedRoundView) bool {
	for _, ph := range view.ProposedHeaders {
		if !gchan.SendC(
			ctx, s.log,
			s.cb.OutgoingProposedHeaders(), ph,
			"sending proposed blocks",
		) {
			return false
		}
	}

	return true
}

func (s *ChattyStrategy) broadcastPrevotes(ctx context.Context, view tmconsensus.VersionedRoundView) bool {
	if len(view.PrevoteProofs) == 0 {
		return true
	}

	prevoteProof := tmconsensus.PrevoteProof{
		Height: view.Height,
		Round:  view.Round,

		Proofs: view.PrevoteProofs,
	}

	sparse, err := prevoteProof.AsSparse()
	if err != nil {
		s.log.Warn(
			"Failed to produce sparse prevote proofs",
			"err", err,
		)
		return false
	}

	return gchan.SendC(
		ctx, s.log,
		s.cb.OutgoingPrevoteProofs(), sparse,
		"sending prevote proofs",
	)
}

func (s *ChattyStrategy) broadcastPrecommits(ctx context.Context, view tmconsensus.VersionedRoundView) bool {
	if len(view.PrecommitProofs) == 0 {
		return true
	}

	precommitProof := tmconsensus.PrecommitProof{
		Height: view.Height,
		Round:  view.Round,

		Proofs: view.PrecommitProofs,
	}

	sparse, err := precommitProof.AsSparse()
	if err != nil {
		s.log.Warn(
			"Failed to produce sparse precommit proofs",
			"err", err,
		)
		return false
	}

	return gchan.SendC(
		ctx, s.log,
		s.cb.OutgoingPrecommitProofs(), sparse,
		"sending precommit proofs",
	)
}

func (s *ChattyStrategy) broadcastAll(ctx context.Context, view tmconsensus.VersionedRoundView) bool {
	return s.broadcastProposedBlocks(ctx, view) &&
		s.broadcastPrevotes(ctx, view) &&
		s.broadcastPrecommits(ctx, view)
}

func (s *ChattyStrategy) broadcastUpdatesOnly(ctx context.Context, prev, cur tmconsensus.VersionedRoundView) bool {
	if len(cur.ProposedHeaders) != len(prev.ProposedHeaders) {
		if !s.broadcastProposedBlocks(ctx, cur) {
			return false
		}
	}

	// Compare the count of set bits in the signature bitsets
	// to determine if we need to broadcast updates for those.

	prevPrevoteBitset := bitset.New(0)
	var bs bitset.BitSet
	for _, p := range prev.PrevoteProofs {
		p.SignatureBitSet(&bs)
		prevPrevoteBitset.InPlaceUnion(&bs)
	}
	curPrevoteBitset := bitset.New(0)
	for _, p := range cur.PrevoteProofs {
		p.SignatureBitSet(&bs)
		curPrevoteBitset.InPlaceUnion(&bs)
	}
	if curPrevoteBitset.Count() != prevPrevoteBitset.Count() {
		if !s.broadcastPrevotes(ctx, cur) {
			return false
		}
	}

	prevPrecommitBitset := bitset.New(0)
	for _, p := range prev.PrecommitProofs {
		p.SignatureBitSet(&bs)
		prevPrecommitBitset.InPlaceUnion(&bs)
	}
	curPrecommitBitset := bitset.New(0)
	for _, p := range cur.PrecommitProofs {
		p.SignatureBitSet(&bs)
		curPrecommitBitset.InPlaceUnion(&bs)
	}
	if curPrecommitBitset.Count() != prevPrecommitBitset.Count() {
		if !s.broadcastPrecommits(ctx, cur) {
			return false
		}
	}

	return true
}
