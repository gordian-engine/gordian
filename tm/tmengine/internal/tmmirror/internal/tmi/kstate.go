package tmi

import (
	"context"
	"fmt"

	"github.com/gordian-engine/gordian/gcrypto"
	"github.com/gordian-engine/gordian/tm/tmconsensus"
)

// kState holds the kernel's mutable state.
type kState struct {
	// The raw views held by the state.
	Committing, Voting tmconsensus.VersionedRoundView

	// The NextRound view is straightforward because we can be certain of the validator set,
	// and we somewhat anticipate receiving messages for that round
	// before we orphan the current voting view.
	NextRound tmconsensus.VersionedRoundView

	// The kernel makes a fetch request if a block reaches >1/3
	// prevotes or precommits, and we don't have the actual proposed header.
	// If a request is outstanding and we switch views,
	// we need to cancel those outstanding requests.
	InFlightFetchPHs map[string]context.CancelFunc

	// Certain operations on the Voting view require knowledge
	// of which header in the Committing view, is being committed.
	// The header will be the zero value if the mirror does not yet have a Committing view.
	// This is a simplified accessor compared to manually finding the committing header
	// by searching through the proposed headers in the committing view.
	CommittingHeader tmconsensus.Header

	// Dedicated manager for the views to send to the state machine.
	// While the state machine primarily is interested in the voting view,
	// the state machine is expected to at least occasionally lag the mirror's view.
	// So, the manager handles the edge cases as the state machine diverges from the mirror.
	StateMachineViewManager stateMachineViewManager

	// Manager for views to share with the gossip strategy.
	GossipViewManager gossipViewManager

	// Manager for lag state, to inform the driver
	// when we believe we are lagging the network.
	LagManager lagManager
}

// FindView finds the view in s matching the given height and round,
// if such a view exists, and returns that view and an identifier.
// If no view matches the height and round, it returns nil, 0, and an appropriate status.
func (s *kState) FindView(h uint64, r uint32, reason string) (*tmconsensus.VersionedRoundView, ViewID, ViewLookupStatus) {
	if h == s.Voting.Height {
		vr := s.Voting.Round
		if r == vr {
			return &s.Voting, ViewIDVoting, ViewFound
		}

		if r == vr+1 {
			return &s.NextRound, ViewIDNextRound, ViewFound
		}

		if r < vr {
			return nil, 0, ViewOrphaned
		}

		return nil, 0, ViewFuture
	}

	if h == s.Committing.Height {
		cr := s.Committing.Round
		if r == cr {
			return &s.Committing, ViewIDCommitting, ViewFound
		}

		if r < cr {
			return nil, 0, ViewBeforeCommitting
		}
	}

	if h < s.Committing.Height {
		return nil, 0, ViewBeforeCommitting
	}

	if h > s.Voting.Height {
		// TODO: this does not properly account for NextHeight, which is not yet implemented.
		return nil, 0, ViewFuture
	}

	panic(fmt.Errorf(
		"TODO: unhandled attempt to find view (reason: %s, request: %d/%d, voting view: %d/%d, committing view: %d/%d)",
		reason, h, r, s.Voting.Height, s.Voting.Round, s.Committing.Height, s.Committing.Round,
	))
}

// MarkCommittingViewUpdated increments the version of s's committing view,
// and informs s's view managers that the Voting view
// has updates that need to be propagated.
func (s *kState) MarkCommittingViewUpdated() {
	s.Committing.Version++

	// Unconditionally update the gossip strategy output.
	s.GossipViewManager.Committing.VRV = s.Committing.Clone()

	smh := s.StateMachineViewManager.H()
	smr := s.StateMachineViewManager.R()
	// The state machine view only needs updated if synchronized with the voting view.
	if smh == s.Committing.Height && smr == s.Committing.Round {
		s.StateMachineViewManager.SetView(s.Committing)
	} else if (smh < s.Committing.Height) ||
		(smh == s.Committing.Height && smr < s.Committing.Round) {
		s.StateMachineViewManager.JumpToRound(s.Committing)
	}
}

// MarkVotingViewUpdated increments the version of s's voting view,
// and informs s's view managers that the Voting view
// has updates that need to be propagated.
func (s *kState) MarkVotingViewUpdated() {
	s.Voting.Version++

	// Unconditionally update the gossip strategy output.
	s.GossipViewManager.Voting.VRV = s.Voting.Clone()

	// The state machine view only needs updated if synchronized with the voting view.
	if s.StateMachineViewManager.H() == s.Voting.Height &&
		s.StateMachineViewManager.R() == s.Voting.Round {
		s.StateMachineViewManager.SetView(s.Voting)
	}
}

// MarkNextRoundViewUpdated increments the version of s's next round view,
// and informs s's view managers that the NextRound view
// has updates that need to be propagated.
func (s *kState) MarkNextRoundViewUpdated() {
	s.NextRound.Version++

	// Unconditionally update the gossip strategy output.
	s.GossipViewManager.NextRound.VRV = s.NextRound.Clone()

	// No state machine updates for next round.
	// The state machine should not be able to be past the mirror state.
}

func (s *kState) MarkViewUpdated(id ViewID) {
	switch id {
	case ViewIDCommitting:
		s.MarkCommittingViewUpdated()
	case ViewIDVoting:
		s.MarkVotingViewUpdated()
	case ViewIDNextRound:
		s.MarkNextRoundViewUpdated()
	default:
		panic(fmt.Errorf("TODO: MarkViewUpdated: handle id %s", id))
	}
}

// nextHeightDetails is the parameter type for [*kState.ShiftVotingToCommitting].
type nextHeightDetails struct {
	ValidatorSet tmconsensus.ValidatorSet

	VotedHeader tmconsensus.Header

	Round0NilPrevote, Round0NilPrecommit,
	Round1NilPrevote, Round1NilPrecommit gcrypto.CommonMessageSignatureProof
}

// ShiftVotingToCommitting shifts the voting view to committing.
// The existing committing view is marked to be in the grace period.
func (s *kState) ShiftVotingToCommitting(nhd nextHeightDetails) {
	// If the state machine was pointing at the committing height,
	// we want to close the HeightCommitted channel
	// to signal the state machine to not spend time in commit wait.
	// But we won't send that signal until we're at the end of the shift.
	h, heightCommittedCh := s.StateMachineViewManager.HeightCommittedChan()
	if h != s.Committing.Height {
		heightCommittedCh = nil
	}

	// Easy part: move the voting view over the committing view.
	if s.Committing.Height != 0 {
		// It could be zero if we haven't committed yet.
		s.GossipViewManager.Grace(s.Committing.Height, s.Committing.Round)
	}
	s.Committing = s.Voting
	s.MarkCommittingViewUpdated()

	s.GossipViewManager.Expire(s.NextRound.Height, s.NextRound.Round)

	newHeight := s.Voting.Height + 1

	commitProofs := make(map[string][]gcrypto.SparseSignature, len(s.Committing.PrecommitProofs))
	for hash, proof := range s.Committing.PrecommitProofs {
		commitProofs[hash] = proof.AsSparse().Signatures
	}

	// If we had NextHeight, we might use that here.
	// But we don't yet, so just clear out the voting view.
	s.Voting = tmconsensus.VersionedRoundView{
		RoundView: tmconsensus.RoundView{
			Height: newHeight,
			Round:  0,

			ValidatorSet: nhd.ValidatorSet,

			PrevCommitProof: tmconsensus.CommitProof{
				Round:      s.Committing.Round,
				PubKeyHash: string(s.Committing.ValidatorSet.PubKeyHash),
				Proofs:     commitProofs,
			},

			// Empty but not nil maps.
			// TODO: this needs to load from the store,
			// as we may have received future votes earlier.
			PrevoteProofs:   map[string]gcrypto.CommonMessageSignatureProof{},
			PrecommitProofs: map[string]gcrypto.CommonMessageSignatureProof{},

			VoteSummary: tmconsensus.NewVoteSummary(),
		},

		PrevoteVersion:   1,
		PrecommitVersion: 1,
	}

	s.Voting.VoteSummary.SetAvailablePower(nhd.ValidatorSet.Validators)
	s.MarkVotingViewUpdated()
	s.GossipViewManager.Activate(newHeight, 0)

	// Now for the next round.
	s.NextRound.Reset()
	s.NextRound.Height = newHeight
	s.NextRound.Round = 1
	s.NextRound.ValidatorSet = nhd.ValidatorSet
	s.NextRound.PrevCommitProof = s.Voting.PrevCommitProof.Clone()
	s.NextRound.PrevoteVersion = 1
	s.NextRound.PrecommitVersion = 1
	s.NextRound.VoteSummary.AvailablePower = s.Voting.VoteSummary.AvailablePower

	s.MarkNextRoundViewUpdated()
	s.GossipViewManager.Activate(newHeight, 1)

	s.CommittingHeader = nhd.VotedHeader

	// As mentioned at the top,
	// we conditionally signal to the state machine that the height has been committed.
	if heightCommittedCh != nil {
		close(heightCommittedCh)
	}
}

// AdvanceVotingRound increments the voting round by one.
func (s *kState) AdvanceVotingRound() {
	// Always set the NilVotedRound here,
	// because we have to assume nobody else has sufficient information to advance.
	//
	// It doesn't matter if there was an existing value for NilVotedRound.
	// If there was one somehow, it would have been out of date.
	vClone := s.Voting.Clone()
	s.GossipViewManager.NilVotedRound = &vClone

	s.incrementVotingRound()

	// Grace the previous round,
	// and the next round was previously active,
	// so we also need to active the new next round.
	s.GossipViewManager.Grace(s.Voting.Height, s.Voting.Round-1)
	s.GossipViewManager.Activate(s.Voting.Height, s.Voting.Round+1)
}

func (s *kState) JumpVotingRound() {
	// In AdvanceVotingRound we set GossipViewManager.NilVotedRound
	// so we could share the terminal details with the network.
	// But here since we are jumping forward,
	// we have to share extra information with the state machine.

	s.incrementVotingRound()

	// After incrementing the voting round, see if the state machine
	// is still pointing at the prior voting round.
	// NOTE: for now this assumes that the state machine and mirror
	// can only be off by one.
	// In the future, the mirror will support jumping ahead
	// more than one round at a time.
	if s.StateMachineViewManager.H() == s.Voting.Height &&
		s.StateMachineViewManager.R() == s.Voting.Round-1 {
		s.StateMachineViewManager.JumpToRound(s.Voting)
	}
}

func (s *kState) incrementVotingRound() {
	// Swap NextRound and Voting.
	// Keep the new Voting value but clear out all the new NextRound values.
	s.Voting, s.NextRound = s.NextRound, s.Voting
	s.MarkVotingViewUpdated()

	s.NextRound.ResetForSameHeight()
	s.NextRound.Round = s.Voting.Round + 1

	s.MarkNextRoundViewUpdated()
}
