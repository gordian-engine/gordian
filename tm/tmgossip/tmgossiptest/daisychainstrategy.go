package tmgossiptest

import (
	"context"
	"errors"
	"fmt"

	"github.com/gordian-engine/gordian/tm/tmconsensus"
	"github.com/gordian-engine/gordian/tm/tmengine/tmelink"
)

// DaisyChainStrategy is an implementation of an in-process
// [github.com/gordian-engine/gordian/tm/tmgossip.Strategy]
// where each "node" is connect left-to-right
// and messages are transmitted left and right concurrently from its originator.
type DaisyChainStrategy struct {
	h tmconsensus.ConsensusHandler

	toLeft, toRight     chan<- daisyChainMessage
	fromLeft, fromRight <-chan daisyChainMessage

	startCh chan (<-chan tmelink.NetworkViewUpdate)

	done chan struct{}
}

// NewDaisyChainStrategy returns a new strategy instance.
// If left is not nil, updates are broadcast to the left.
//
// All related instances must be created together before
// [*DaisyChainStrategy.Start] is called on any instance.
// [NewDaisyChainFixture] is one function that creates the strategy instances,
// sets all the consensus handlers, and then calls Start on every instance.
func NewDaisyChainStrategy(ctx context.Context, left *DaisyChainStrategy) *DaisyChainStrategy {
	s := &DaisyChainStrategy{
		startCh: make(chan (<-chan tmelink.NetworkViewUpdate), 1),

		done: make(chan struct{}),
	}

	if left != nil {
		const bufSz = 32 // Arbitrarily picked.
		toLeft := make(chan daisyChainMessage, bufSz)
		toRight := make(chan daisyChainMessage, bufSz)

		left.toRight = toRight
		s.fromLeft = toRight

		left.fromRight = toLeft
		s.toLeft = toLeft
	}

	go s.mainLoop(ctx)
	return s
}

type daisyChainMessage struct {
	// Exactly one of these fields will be set.
	ProposedHeader *tmconsensus.ProposedHeader
	Prevote        *tmconsensus.PrevoteSparseProof
	Precommit      *tmconsensus.PrecommitSparseProof

	// BlockData will not be nil if ph is set.
	BlockData []byte
}

// SetConsensusHandler sets the consensus handler for the strategy.
// This method must be called before [*DaisyChainStrategy.Start].
func (s *DaisyChainStrategy) SetConsensusHandler(h tmconsensus.ConsensusHandler) {
	s.h = h
}

func (s *DaisyChainStrategy) mainLoop(ctx context.Context) {
	defer close(s.done)

	var updateCh <-chan tmelink.NetworkViewUpdate

	select {
	case <-ctx.Done():
		return
	case updateCh = <-s.startCh:
		// Will never use the field again, so just clear it.
		s.startCh = nil
	}

	for {
		select {
		case <-ctx.Done():
			return

		case u := <-updateCh:
			s.broadcastUpdate(ctx, u)

		case msg := <-s.fromLeft:
			s.acceptMessage(ctx, msg)
			s.forwardMessage(ctx, msg, s.toRight)

		case msg := <-s.fromRight:
			s.acceptMessage(ctx, msg)
			s.forwardMessage(ctx, msg, s.toLeft)
		}
	}
}

// Start implements [github.com/gordian-engine/gordian/tm/tmgossip.Strategy].
func (s *DaisyChainStrategy) Start(updates <-chan tmelink.NetworkViewUpdate) {
	s.startCh <- updates
}

func (s *DaisyChainStrategy) broadcastUpdate(
	ctx context.Context, u tmelink.NetworkViewUpdate,
) {
	s.broadcastView(ctx, u.Committing)
	s.broadcastView(ctx, u.Voting)
	s.broadcastView(ctx, u.NextRound)
}

func (s *DaisyChainStrategy) broadcastView(
	ctx context.Context, vrv *tmconsensus.VersionedRoundView,
) {
	if vrv == nil {
		return
	}

	// TODO: this sends way more messages than necessary.
	// We should be tracking the versions on the VRV
	// and using those to determine which subsets of messages to send.

	for _, ph := range vrv.ProposedHeaders {
		if s.toLeft != nil {
			select {
			case <-ctx.Done():
				return
			case s.toLeft <- daisyChainMessage{
				ProposedHeader: &ph,
			}:
				// Okay.
			}
		}
		if s.toRight != nil {
			select {
			case <-ctx.Done():
				return
			case s.toRight <- daisyChainMessage{
				ProposedHeader: &ph,
			}:
				// Okay.
			}
		}
	}

	if len(vrv.PrevoteProofs) > 0 {
		sparse, err := tmconsensus.PrevoteProof{
			Height: vrv.Height,
			Round:  vrv.Round,
			Proofs: vrv.PrevoteProofs,
		}.AsSparse()
		if err != nil {
			panic(fmt.Errorf(
				"TODO: handle error in getting sparse prevote proofs: %w", err,
			))
		}

		if s.toLeft != nil {
			select {
			case <-ctx.Done():
				return
			case s.toLeft <- daisyChainMessage{
				Prevote: &sparse,
			}:
				// Okay.
			}
		}
		if s.toRight != nil {
			select {
			case <-ctx.Done():
				return
			case s.toRight <- daisyChainMessage{
				Prevote: &sparse,
			}:
				// Okay.
			}
		}
	}

	if len(vrv.PrecommitProofs) > 0 {
		sparse, err := tmconsensus.PrecommitProof{
			Height: vrv.Height,
			Round:  vrv.Round,
			Proofs: vrv.PrecommitProofs,
		}.AsSparse()
		if err != nil {
			panic(fmt.Errorf(
				"TODO: handle error in getting sparse precommit proofs: %w", err,
			))
		}

		if s.toLeft != nil {
			select {
			case <-ctx.Done():
				return
			case s.toLeft <- daisyChainMessage{
				Precommit: &sparse,
			}:
				// Okay.
			}
		}
		if s.toRight != nil {
			select {
			case <-ctx.Done():
				return
			case s.toRight <- daisyChainMessage{
				Precommit: &sparse,
			}:
				// Okay.
			}
		}
	}
}

func (s *DaisyChainStrategy) acceptMessage(
	ctx context.Context, msg daisyChainMessage,
) {
	if msg.ProposedHeader != nil {
		_ = s.h.HandleProposedHeader(ctx, *msg.ProposedHeader)
		return
	}

	if msg.Prevote != nil {
		_ = s.h.HandlePrevoteProofs(ctx, *msg.Prevote)
		return
	}

	if msg.Precommit != nil {
		_ = s.h.HandlePrecommitProofs(ctx, *msg.Precommit)
		return
	}

	panic(errors.New(
		"BUG: attempted to accept message without any field set",
	))
}

func (s *DaisyChainStrategy) forwardMessage(
	ctx context.Context, msg daisyChainMessage, dst chan<- daisyChainMessage,
) {
	if dst == nil {
		return
	}

	select {
	case <-ctx.Done():
		return
	case dst <- msg:
		// Done.
	}
}

// Wait blocks until all background work in the strategy has completed.
func (s *DaisyChainStrategy) Wait() {
	<-s.done
}
