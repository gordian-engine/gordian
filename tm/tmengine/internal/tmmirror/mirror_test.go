package tmmirror_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/bits-and-blooms/bitset"
	"github.com/gordian-engine/gordian/gcrypto"
	"github.com/gordian-engine/gordian/internal/gtest"
	"github.com/gordian-engine/gordian/tm/tmconsensus"
	"github.com/gordian-engine/gordian/tm/tmconsensus/tmconsensustest"
	"github.com/gordian-engine/gordian/tm/tmengine/internal/tmeil"
	"github.com/gordian-engine/gordian/tm/tmengine/internal/tmemetrics"
	"github.com/gordian-engine/gordian/tm/tmengine/internal/tmmirror"
	"github.com/gordian-engine/gordian/tm/tmengine/internal/tmmirror/internal/tmi"
	"github.com/gordian-engine/gordian/tm/tmengine/internal/tmmirror/tmmirrortest"
	"github.com/gordian-engine/gordian/tm/tmengine/tmelink"
	"github.com/gordian-engine/gordian/tm/tmengine/tmelink/tmelinktest"
	"github.com/stretchr/testify/require"
)

// voteTypes is a package-level variable to be used in tests that
// need to run an otherwise-identical test, only alternating between the two vote types.
var voteTypes = []struct {
	Name      string
	VoterFunc func(*tmmirrortest.Fixture, *tmmirror.Mirror) tmmirrortest.Voter
}{
	{
		Name: "prevote",
		VoterFunc: func(mfx *tmmirrortest.Fixture, m *tmmirror.Mirror) tmmirrortest.Voter {
			return mfx.Prevoter(m)
		},
	},
	{
		Name: "precommit",
		VoterFunc: func(mfx *tmmirrortest.Fixture, m *tmmirror.Mirror) tmmirrortest.Voter {
			return mfx.Precommitter(m)
		},
	},
}

func TestMirror_Initialization(t *testing.T) {
	t.Run("sets voting height to initial height when store is empty", func(t *testing.T) {
		for _, initialHeight := range []uint64{1, 5} {
			initialHeight := initialHeight

			t.Run(fmt.Sprintf("when initial height = %d", initialHeight), func(t *testing.T) {
				t.Parallel()

				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()

				mfx := tmmirrortest.NewFixture(ctx, t, 2)
				mfx.Cfg.InitialHeight = initialHeight

				m := mfx.NewMirror()
				defer m.Wait()
				defer cancel()

				wantNHR := tmmirror.NetworkHeightRound{
					VotingHeight: initialHeight,
					VotingRound:  0,

					// Commiting HR is explicitly zero at initial height.
					CommittingHeight: 0,
					CommittingRound:  0,
				}
				// Reports the initial height.
				nhr, err := tmi.NetworkHeightRoundFromStore(mfx.Store().NetworkHeightRound(ctx))
				require.NoError(t, err)
				require.Equal(t, wantNHR, nhr)
			})
		}
	})

	t.Run("does not modify store height if already past initial height", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		mfx := tmmirrortest.NewFixture(ctx, t, 2)
		mfx.CommitInitialHeight(ctx, []byte("app_state_1"), 0, []int{0, 1})
		// Now adjust the mirror store directly to say we are on round 1.
		// The CommitInitialHeight helper fixes the round to zero.
		// Round 1 will now be an "orphaned" view which we should be able to safely ignore.
		require.NoError(t, mfx.Cfg.Store.SetNetworkHeightRound(tmmirror.NetworkHeightRound{
			VotingHeight: 2,
			VotingRound:  1,

			CommittingHeight: 1,
			CommittingRound:  0,
		}.ForStore(ctx)))

		m := mfx.NewMirror()
		defer m.Wait()
		defer cancel()

		wantNHR := tmmirror.NetworkHeightRound{
			VotingHeight: 2,
			VotingRound:  1,

			CommittingHeight: 1,
			CommittingRound:  0,
		}

		// Reports the initial height.
		nhr, err := tmi.NetworkHeightRoundFromStore(mfx.Store().NetworkHeightRound(ctx))
		require.NoError(t, err)
		require.Equal(t, wantNHR, nhr)
	})

	t.Run("adds validator hashes to store if not yet present", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		mfx := tmmirrortest.NewFixture(ctx, t, 2)

		valSet := mfx.Fx.ValSet()
		keyHash := valSet.PubKeyHash
		powHash := valSet.VotePowerHash

		vs := mfx.ValidatorStore()

		// Not present before starting the mirror.
		noVals, err := vs.LoadPubKeys(ctx, string(keyHash))
		require.Error(t, err)
		require.Nil(t, noVals)
		noPows, err := vs.LoadVotePowers(ctx, string(powHash))
		require.Error(t, err)
		require.Nil(t, noPows)

		m := mfx.NewMirror()
		defer m.Wait()
		defer cancel()

		// After starting, the hashes are added to the validator store.
		gotVals, err := vs.LoadValidators(ctx, string(keyHash), string(powHash))
		require.NoError(t, err)
		require.True(t, tmconsensus.ValidatorSlicesEqual(valSet.Validators, gotVals))
	})

	t.Run("starts correctly if the validator hashes are already in the store", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		mfx := tmmirrortest.NewFixture(ctx, t, 2)
		valSet := mfx.Fx.ValSet()

		vs := mfx.ValidatorStore()
		_, err := vs.SavePubKeys(
			ctx,
			tmconsensus.ValidatorsToPubKeys(valSet.Validators),
		)
		require.NoError(t, err)
		_, err = vs.SaveVotePowers(
			ctx,
			tmconsensus.ValidatorsToVotePowers(valSet.Validators),
		)
		require.NoError(t, err)

		m := mfx.NewMirror()
		defer m.Wait()
		defer cancel()

		// Nothing to assert here -- if we could construct the mirror through the fixture,
		// it did not fail.
	})

	t.Run("initial gossip strategy outputs", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		mfx := tmmirrortest.NewFixture(ctx, t, 2)
		valSet := mfx.Fx.ValSet()

		m := mfx.NewMirror()
		defer m.Wait()
		defer cancel()

		gso := gtest.ReceiveSoon(t, mfx.GossipStrategyOut)
		require.Nil(t, gso.Committing)

		t.Run("voting view", func(t *testing.T) {
			v := gso.Voting
			require.NotNil(t, v)
			require.Equal(t, uint32(1), v.Version)
			require.Equal(t, uint64(1), v.Height)
			require.Zero(t, v.Round)

			// Vote versions are 1, indicating a deliberate empty set.
			require.Equal(t, uint32(1), v.PrevoteVersion)
			require.Equal(t, uint32(1), v.PrecommitVersion)

			// Validator details match.
			require.True(t, valSet.Equal(v.ValidatorSet))

			// Proposed headers are empty, and nil proofs are ready.
			require.Empty(t, v.ProposedHeaders)
			require.Empty(t, v.PrevoteProofs, 1)
			require.Empty(t, v.PrecommitProofs, 1)

			s := v.VoteSummary
			var expAvailPow uint64
			for _, val := range v.ValidatorSet.Validators {
				expAvailPow += val.Power
			}
			require.Equal(t, expAvailPow, s.AvailablePower)
		})

		t.Run("next round view", func(t *testing.T) {
			v := gso.NextRound
			require.NotNil(t, v)
			require.Equal(t, uint32(1), v.Version)
			require.Equal(t, uint64(1), v.Height)
			require.Equal(t, uint32(1), v.Round)

			// Vote versions are 1, indicating a deliberate empty set.
			require.Equal(t, uint32(1), v.PrevoteVersion)
			require.Equal(t, uint32(1), v.PrecommitVersion)

			// Validator details match.
			require.True(t, valSet.Equal(v.ValidatorSet))

			// Proposed headers are empty, and nil proofs are ready.
			require.Empty(t, v.ProposedHeaders)
			require.Empty(t, v.PrevoteProofs, 1)
			require.Empty(t, v.PrecommitProofs, 1)

			s := v.VoteSummary
			var expAvailPow uint64
			for _, val := range v.ValidatorSet.Validators {
				expAvailPow += val.Power
			}
			require.Equal(t, expAvailPow, s.AvailablePower)
		})
	})
}

// Checking values on the outputs that don't necessarily fit with other tests.
func TestMirror_Outputs(t *testing.T) {
	t.Run("at genesis", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		mfx := tmmirrortest.NewFixture(ctx, t, 4)

		m := mfx.NewMirror()
		defer m.Wait()
		defer cancel()

		keyHash, powHash := mfx.Fx.ValidatorHashes()

		gso := gtest.ReceiveSoon(t, mfx.GossipStrategyOut)

		require.Nil(t, gso.Committing)

		require.Equal(t, keyHash, string(gso.Voting.ValidatorSet.PubKeyHash))
		require.Equal(t, powHash, string(gso.Voting.ValidatorSet.VotePowerHash))

		require.Equal(t, keyHash, string(gso.NextRound.ValidatorSet.PubKeyHash))
		require.Equal(t, powHash, string(gso.NextRound.ValidatorSet.VotePowerHash))
	})

	t.Run("starting past initial height", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		mfx := tmmirrortest.NewFixture(ctx, t, 4)

		mfx.CommitInitialHeight(ctx, []byte("app_data_1"), 0, []int{0, 1, 2, 3})

		m := mfx.NewMirror()
		defer m.Wait()
		defer cancel()

		keyHash, powHash := mfx.Fx.ValidatorHashes()

		gso := gtest.ReceiveSoon(t, mfx.GossipStrategyOut)

		require.Equal(t, keyHash, string(gso.Committing.ValidatorSet.PubKeyHash))
		require.Equal(t, powHash, string(gso.Committing.ValidatorSet.VotePowerHash))

		require.Equal(t, keyHash, string(gso.Voting.ValidatorSet.PubKeyHash))
		require.Equal(t, powHash, string(gso.Voting.ValidatorSet.VotePowerHash))

		require.Equal(t, keyHash, string(gso.NextRound.ValidatorSet.PubKeyHash))
		require.Equal(t, powHash, string(gso.NextRound.ValidatorSet.VotePowerHash))
	})

	t.Run("after a live commit", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		mfx := tmmirrortest.NewFixture(ctx, t, 4)

		m := mfx.NewMirror()
		defer m.Wait()
		defer cancel()

		// Drain initial gossip strategy output.
		_ = gtest.ReceiveSoon(t, mfx.GossipStrategyOut)

		ph1 := mfx.Fx.NextProposedHeader([]byte("app_data_1"), 0)
		mfx.Fx.SignProposal(ctx, &ph1, 0)
		require.Equal(t, tmconsensus.HandleProposedHeaderAccepted, m.HandleProposedHeader(ctx, ph1))
		keyHash, powHash := mfx.Fx.ValidatorHashes()

		voteMap := map[string][]int{
			string(ph1.Header.Hash): {0, 1, 2, 3},
		}
		precommitProof := tmconsensus.PrecommitSparseProof{
			Height:     1,
			Round:      0,
			PubKeyHash: keyHash,
			Proofs:     mfx.Fx.SparsePrecommitProofMap(ctx, 1, 0, voteMap),
		}
		require.Equal(t, tmconsensus.HandleVoteProofsAccepted, m.HandlePrecommitProofs(ctx, precommitProof))

		gso := gtest.ReceiveSoon(t, mfx.GossipStrategyOut)

		require.Equal(t, keyHash, string(gso.Committing.ValidatorSet.PubKeyHash))
		require.Equal(t, powHash, string(gso.Committing.ValidatorSet.VotePowerHash))

		require.Equal(t, keyHash, string(gso.Voting.ValidatorSet.PubKeyHash))
		require.Equal(t, powHash, string(gso.Voting.ValidatorSet.VotePowerHash))

		require.Equal(t, keyHash, string(gso.NextRound.ValidatorSet.PubKeyHash))
		require.Equal(t, powHash, string(gso.NextRound.ValidatorSet.VotePowerHash))
	})
}

func TestMirror_HandleProposedHeader(t *testing.T) {
	t.Run("adds valid proposed header in voting round to gossip strategy output and round store", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		mfx := tmmirrortest.NewFixture(ctx, t, 2)

		m := mfx.NewMirror()
		defer m.Wait()
		defer cancel()

		// Drain the gossip strategy output.
		_ = gtest.ReceiveSoon(t, mfx.GossipStrategyOut)

		// Sign proposed header, because the mirror actually validates this.
		ph1 := mfx.Fx.NextProposedHeader([]byte("app_data_1"), 0)
		mfx.Fx.SignProposal(ctx, &ph1, 0)

		require.Equal(t, tmconsensus.HandleProposedHeaderAccepted, m.HandleProposedHeader(ctx, ph1))

		// The proposed header is added in the background while HandleProposedHeader returns,
		// so synchronize on the voting view update before inspecting the store.
		gso := gtest.ReceiveSoon(t, mfx.GossipStrategyOut)
		// Version should be bumped to 2, because we can be sure the additional proposed header
		// was a single update operation.
		require.Equal(t, uint32(2), gso.Voting.Version)
		require.Equal(t, []tmconsensus.ProposedHeader{ph1}, gso.Voting.ProposedHeaders)

		// Present in the network view inspection.
		var vrv tmconsensus.VersionedRoundView
		require.NoError(t, m.VotingView(ctx, &vrv))

		require.Empty(t, vrv.PrevoteProofs)
		require.Empty(t, vrv.PrecommitProofs)

		// Coerce empty maps to nil for this test.
		// Possibly not correct in the long run to do this.
		vrv.PrevoteProofs = nil
		vrv.PrecommitProofs = nil

		vs := tmconsensus.NewVoteSummary()
		vs.SetAvailablePower(mfx.Fx.Vals())

		require.Equal(t, tmconsensus.VersionedRoundView{
			RoundView: tmconsensus.RoundView{
				Height: 1,
				Round:  0,

				ValidatorSet: mfx.Fx.ValSet(),

				PrevCommitProof: tmconsensus.CommitProof{
					// At initial height, needs an empty but non-nil proof.
					Proofs: map[string][]gcrypto.SparseSignature{},
				},

				ProposedHeaders: []tmconsensus.ProposedHeader{ph1},

				VoteSummary: vs,
			},
			Version: 2, // Initially 1, then add a prevote (2).
		}, vrv)

		// And present in the round store.
		phs, _, _, err := mfx.Cfg.RoundStore.LoadRoundState(ctx, 1, 0)
		require.NoError(t, err)
		require.Equal(t, []tmconsensus.ProposedHeader{ph1}, phs)
	})

	t.Run("only latest proposed header update sent on Voting output channel", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		mfx := tmmirrortest.NewFixture(ctx, t, 2)

		m := mfx.NewMirror()
		defer m.Wait()
		defer cancel()

		initGSO := gtest.ReceiveSoon(t, mfx.GossipStrategyOut)
		require.Equal(t, uint32(1), initGSO.Voting.Version)
		require.Empty(t, initGSO.Voting.ProposedHeaders)

		ph1 := mfx.Fx.NextProposedHeader([]byte("app_data_1"), 0)
		mfx.Fx.SignProposal(ctx, &ph1, 0)
		require.Equal(t, tmconsensus.HandleProposedHeaderAccepted, m.HandleProposedHeader(ctx, ph1))

		ph2 := mfx.Fx.NextProposedHeader([]byte("app_data_2"), 1)
		mfx.Fx.SignProposal(ctx, &ph2, 1)
		require.Equal(t, tmconsensus.HandleProposedHeaderAccepted, m.HandleProposedHeader(ctx, ph2))

		gso := gtest.ReceiveSoon(t, mfx.GossipStrategyOut)
		// Not asserting the explicit version here -- 3 makes more sense than 2,
		// but we don't need to enforce that,
		// in case we condense those two updates into one in the future for some reason.
		require.Greater(t, gso.Voting.Version, initGSO.Voting.Version)
		require.Equal(t, []tmconsensus.ProposedHeader{ph1, ph2}, gso.Voting.ProposedHeaders)
	})

	t.Run("accepts proposed header to committing view", func(t *testing.T) {
		// If one validator is running slightly behind and proposes a header that reaches the committing view,
		// it should still be included in updates.
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		mfx := tmmirrortest.NewFixture(ctx, t, 4)

		ph03 := mfx.Fx.NextProposedHeader([]byte("app_data_0_3"), 3)
		mfx.Fx.SignProposal(ctx, &ph03, 3)

		mfx.CommitInitialHeight(ctx, []byte("app_state_1"), 3, []int{0, 1, 2, 3})

		m := mfx.NewMirror()
		defer m.Wait()
		defer cancel()

		gso := gtest.ReceiveSoon(t, mfx.GossipStrategyOut)
		require.NotNil(t, gso.Committing)
		require.NotNil(t, gso.Voting)
		require.NotNil(t, gso.NextRound)

		before := gso.Committing.Clone()

		require.Equal(t, tmconsensus.HandleProposedHeaderAccepted, m.HandleProposedHeader(ctx, ph03))

		gso = gtest.ReceiveSoon(t, mfx.GossipStrategyOut)
		require.NotNil(t, gso.Committing)
		require.Nil(t, gso.Voting)
		require.Nil(t, gso.NextRound)

		after := gso.Committing.Clone()

		require.Greater(t, after.Version, before.Version)
		require.Subset(t, after.ProposedHeaders, before.ProposedHeaders)
		require.Contains(t, after.ProposedHeaders, ph03)
	})

	t.Run("proposed header for next height backfills commit into voting round", func(t *testing.T) {
		t.Run("when the voting view already has a proposed header matching", func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			mfx := tmmirrortest.NewFixture(ctx, t, 4)

			ph1 := mfx.Fx.NextProposedHeader([]byte("app_data_1"), 1)
			mfx.Fx.SignProposal(ctx, &ph1, 1)

			m := mfx.NewMirror()
			defer m.Wait()
			defer cancel()

			require.Equal(t, tmconsensus.HandleProposedHeaderAccepted, m.HandleProposedHeader(ctx, ph1))

			keyHash, _ := mfx.Fx.ValidatorHashes()
			voteMap := map[string][]int{
				string(ph1.Header.Hash): {0, 1, 2, 3},
			}
			sparsePrevoteProofMap := mfx.Fx.SparsePrevoteProofMap(ctx, 1, 0, voteMap)
			prevoteProof := tmconsensus.PrevoteSparseProof{
				Height:     1,
				Round:      0,
				PubKeyHash: keyHash,
				Proofs:     sparsePrevoteProofMap,
			}

			// Give the mirror the prevotes, not that it particularly matters here.
			require.Equal(t, tmconsensus.HandleVoteProofsAccepted, m.HandlePrevoteProofs(ctx, prevoteProof))

			// Then generate the precommits and only advance the fixture.
			precommitProofs1 := mfx.Fx.PrecommitProofMap(ctx, 1, 0, voteMap)
			mfx.Fx.CommitBlock(ph1.Header, []byte("app_state_height_1"), 0, precommitProofs1)

			// Now the fixture can produce the proposed header for height 2.
			ph2 := mfx.Fx.NextProposedHeader([]byte("app_data_2"), 1)
			mfx.Fx.SignProposal(ctx, &ph2, 1)

			// Drain the gossip strategy output before applying the proposed header.
			before := gtest.ReceiveSoon(t, mfx.GossipStrategyOut)
			require.Nil(t, before.Committing) // No committing view yet.
			require.NotNil(t, before.Voting)

			require.Equal(t, tmconsensus.HandleProposedHeaderAccepted, m.HandleProposedHeader(ctx, ph2))

			// Height 1 should be in the committing view, and the voting view should have the new proposed header.
			after := gtest.ReceiveSoon(t, mfx.GossipStrategyOut)
			require.NotNil(t, after.Committing)
			require.Equal(t, uint64(1), after.Committing.Height)
			require.Zero(t, after.Committing.Round)

			require.NotNil(t, after.Voting)
			require.Equal(t, uint64(2), after.Voting.Height)
			require.Zero(t, after.Voting.Round)
			require.Equal(t, []tmconsensus.ProposedHeader{ph2}, after.Voting.ProposedHeaders)
		})
	})

	t.Run("rejected when there is a double signature", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		mfx := tmmirrortest.NewFixture(ctx, t, 4)

		m := mfx.NewMirror()
		defer m.Wait()
		defer cancel()

		// Proposed header and full precommits at height 1.
		ph1 := mfx.Fx.NextProposedHeader([]byte("app_data_1"), 0)
		mfx.Fx.SignProposal(ctx, &ph1, 0)
		require.Equal(t, tmconsensus.HandleProposedHeaderAccepted, m.HandleProposedHeader(ctx, ph1))
		keyHash, _ := mfx.Fx.ValidatorHashes()
		voteMap := map[string][]int{
			string(ph1.Header.Hash): {0, 1, 2, 3},
		}
		precommitProof := tmconsensus.PrecommitSparseProof{
			Height:     1,
			Round:      0,
			PubKeyHash: keyHash,
			Proofs:     mfx.Fx.SparsePrecommitProofMap(ctx, 1, 0, voteMap),
		}
		require.Equal(t, tmconsensus.HandleVoteProofsAccepted, m.HandlePrecommitProofs(ctx, precommitProof))

		// Now the mirror should be ready for the proposed header at height 2.
		// So, if we set the previous commit proof to include a double signature for validator 3,
		// the mirror should reject the proposed header on account of the double signature.

		ph2 := mfx.Fx.NextProposedHeader([]byte("app_data_2"), 1)
		voteMap[""] = []int{3}
		precommitProof = tmconsensus.PrecommitSparseProof{
			Height:     1,
			Round:      0,
			PubKeyHash: keyHash,
			Proofs:     mfx.Fx.SparsePrecommitProofMap(ctx, 1, 0, voteMap),
		}

		ph2.Header.PrevCommitProof.Proofs[""] = precommitProof.Proofs[""]
		mfx.Fx.RecalculateHash(&ph2.Header)
		mfx.Fx.SignProposal(ctx, &ph2, 0)
		require.Equal(t, tmconsensus.HandleProposedHeaderBadPrevCommitProofDoubleSigned, m.HandleProposedHeader(ctx, ph2))
	})

	t.Run("rejected for missing proposer pubkey", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		mfx := tmmirrortest.NewFixture(ctx, t, 4)

		m := mfx.NewMirror()
		defer m.Wait()
		defer cancel()

		ph1 := mfx.Fx.NextProposedHeader([]byte("app_data_1"), 0)

		// Sign the proposal, but then remove the pubkey.
		mfx.Fx.SignProposal(ctx, &ph1, 0)
		ph1.ProposerPubKey = nil

		require.Equal(t, tmconsensus.HandleProposedHeaderMissingProposerPubKey, m.HandleProposedHeader(ctx, ph1))
	})

	t.Run("ignored when proposed header round is too old", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		mfx := tmmirrortest.NewFixture(ctx, t, 4)

		m := mfx.NewMirror()
		defer m.Wait()
		defer cancel()

		// Proposed header and full precommits at height 1.
		ph1 := mfx.Fx.NextProposedHeader([]byte("app_data_1"), 0)
		mfx.Fx.SignProposal(ctx, &ph1, 0)
		require.Equal(t, tmconsensus.HandleProposedHeaderAccepted, m.HandleProposedHeader(ctx, ph1))
		keyHash, _ := mfx.Fx.ValidatorHashes()
		voteMap := map[string][]int{
			string(ph1.Header.Hash): {0, 1, 2, 3},
		}
		precommitProof := tmconsensus.PrecommitSparseProof{
			Height:     1,
			Round:      0,
			PubKeyHash: keyHash,
			Proofs:     mfx.Fx.SparsePrecommitProofMap(ctx, 1, 0, voteMap),
		}
		require.Equal(t, tmconsensus.HandleVoteProofsAccepted, m.HandlePrecommitProofs(ctx, precommitProof))

		// Next for height 2.
		// Adjust the fixture first, but we are going to make Height 2 advance to Round 1.
		mfx.Fx.CommitBlock(
			ph1.Header, []byte("app_state_height_1"), 0,
			mfx.Fx.PrecommitProofMap(ctx, 1, 0, voteMap),
		)

		// Full precommit at round 0 to advance to round 1.
		voteMap = map[string][]int{
			"": {0, 1, 2, 3},
		}
		precommitProof = tmconsensus.PrecommitSparseProof{
			Height:     2,
			Round:      0,
			PubKeyHash: keyHash,
			Proofs:     mfx.Fx.SparsePrecommitProofMap(ctx, 2, 0, voteMap),
		}
		require.Equal(t, tmconsensus.HandleVoteProofsAccepted, m.HandlePrecommitProofs(ctx, precommitProof))

		// Now we are on round 1, so a new proposed header at round 0 should be too old.
		t.Run("older round within voting height", func(t *testing.T) {
			ph2 := mfx.Fx.NextProposedHeader([]byte("app_data_2"), 0)
			mfx.Fx.SignProposal(ctx, &ph2, 0)
			require.Equal(t, uint64(2), ph2.Header.Height)
			require.Zero(t, ph2.Round)
			require.Equal(t, tmconsensus.HandleProposedHeaderRoundTooOld, m.HandleProposedHeader(ctx, ph2))
		})

		ph2 := mfx.Fx.NextProposedHeader([]byte("app_data_2"), 0)
		ph2.Round = 1
		mfx.Fx.SignProposal(ctx, &ph2, 0)

		require.Equal(t, tmconsensus.HandleProposedHeaderAccepted, m.HandleProposedHeader(ctx, ph2))
		voteMap = map[string][]int{
			string(ph2.Header.Hash): {0, 1, 2, 3},
		}
		precommitProof = tmconsensus.PrecommitSparseProof{
			Height:     2,
			Round:      1,
			PubKeyHash: keyHash,
			Proofs:     mfx.Fx.SparsePrecommitProofMap(ctx, 2, 1, voteMap),
		}
		require.Equal(t, tmconsensus.HandleVoteProofsAccepted, m.HandlePrecommitProofs(ctx, precommitProof))

		// And now height 3, to push height 2 into committing and height 1 out of view.
		mfx.Fx.CommitBlock(
			ph2.Header, []byte("app_state_height_1"), 0,
			mfx.Fx.PrecommitProofMap(ctx, 2, 0, voteMap),
		)
		ph3 := mfx.Fx.NextProposedHeader([]byte("app_data_3"), 0)
		mfx.Fx.SignProposal(ctx, &ph3, 0)

		t.Run("older round within committing height", func(t *testing.T) {
			// Just reuse the existing ph2, overwriting its round to zero.
			ph2.Round = 0
			mfx.Fx.SignProposal(ctx, &ph2, 0)
			require.Equal(t, tmconsensus.HandleProposedHeaderRoundTooOld, m.HandleProposedHeader(ctx, ph2))
		})

		require.Equal(t, tmconsensus.HandleProposedHeaderAccepted, m.HandleProposedHeader(ctx, ph3))
		voteMap = map[string][]int{
			string(ph3.Header.Hash): {0, 1, 2, 3},
		}
		precommitProof = tmconsensus.PrecommitSparseProof{
			Height:     3,
			Round:      0,
			PubKeyHash: keyHash,
			Proofs:     mfx.Fx.SparsePrecommitProofMap(ctx, 3, 0, voteMap),
		}
		require.Equal(t, tmconsensus.HandleVoteProofsAccepted, m.HandlePrecommitProofs(ctx, precommitProof))

		t.Run("height that is no longer in view", func(t *testing.T) {
			require.Equal(t, tmconsensus.HandleProposedHeaderRoundTooOld, m.HandleProposedHeader(ctx, ph1))
		})
	})
}

func TestMirror_HandlePrevoteProofs(t *testing.T) {
	t.Run("happy path - available in network view and round store", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		mfx := tmmirrortest.NewFixture(ctx, t, 2)

		m := mfx.NewMirror()
		defer m.Wait()
		defer cancel()

		// Sign proposed header, because the mirror actually validates this.
		ph1 := mfx.Fx.NextProposedHeader([]byte("app_data_1"), 0)
		mfx.Fx.SignProposal(ctx, &ph1, 0)

		require.Equal(t, tmconsensus.HandleProposedHeaderAccepted, m.HandleProposedHeader(ctx, ph1))

		keyHash, _ := mfx.Fx.ValidatorHashes()

		// One active and one nil prevote.
		voteMap := map[string][]int{
			string(ph1.Header.Hash): {0},
			"":                      {1},
		}
		sparsePrevoteProofMap := mfx.Fx.SparsePrevoteProofMap(ctx, 1, 0, voteMap)
		prevoteProof := tmconsensus.PrevoteSparseProof{
			Height:     1,
			Round:      0,
			PubKeyHash: keyHash,
			Proofs:     sparsePrevoteProofMap,
		}
		fullPrevoteProofMap := mfx.Fx.PrevoteProofMap(ctx, 1, 0, voteMap)

		require.Equal(t, tmconsensus.HandleVoteProofsAccepted, m.HandlePrevoteProofs(ctx, prevoteProof))

		var vnv tmconsensus.VersionedRoundView
		require.NoError(t, m.VotingView(ctx, &vnv))

		vs := tmconsensus.NewVoteSummary()
		vs.SetAvailablePower(mfx.Fx.Vals())
		vs.SetPrevotePowers(mfx.Fx.Vals(), fullPrevoteProofMap)

		require.Equal(t, tmconsensus.VersionedRoundView{
			RoundView: tmconsensus.RoundView{
				Height:       1,
				Round:        0,
				ValidatorSet: mfx.Fx.ValSet(),

				PrevCommitProof: tmconsensus.CommitProof{Proofs: map[string][]gcrypto.SparseSignature{}},

				ProposedHeaders: []tmconsensus.ProposedHeader{ph1},

				PrevoteProofs:   fullPrevoteProofMap,
				PrecommitProofs: mfx.Fx.PrecommitProofMap(ctx, 1, 0, map[string][]int{}),
				VoteSummary:     vs,
			},
			Version: 3, // Started at 1, added a proposed header (2), then bulk added prevotes (3).
			PrevoteBlockVersions: map[string]uint32{
				"":                      1,
				string(ph1.Header.Hash): 1,
			},
		}, vnv)

		_, prevotes, _, err := mfx.Cfg.RoundStore.LoadRoundState(ctx, 1, 0)
		require.NoError(t, err)

		newFullPrevotes, err := prevotes.ToFullPrevoteProofMap(
			1, 0,
			mfx.Fx.ValSet(),
			mfx.Fx.SignatureScheme, mfx.Fx.CommonMessageSignatureProofScheme,
		)
		require.NoError(t, err)
		require.Equal(t, fullPrevoteProofMap, newFullPrevotes)
	})

	t.Run("concurrent independent updates accepted", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		const size = 10

		mfx := tmmirrortest.NewFixture(ctx, t, size)

		m := mfx.NewMirror()
		defer m.Wait()
		defer cancel()

		// Sign proposed header, because the mirror actually validates this.
		ph1 := mfx.Fx.NextProposedHeader([]byte("app_data_1"), 0)
		mfx.Fx.SignProposal(ctx, &ph1, 0)

		require.Equal(t, tmconsensus.HandleProposedHeaderAccepted, m.HandleProposedHeader(ctx, ph1))

		keyHash, _ := mfx.Fx.ValidatorHashes()

		// Start an independent goroutine for each prevote to submit.
		start := make(chan struct{})
		feedbackCh := make(chan tmconsensus.HandleVoteProofsResult, size)
		for i := range size {
			go func(i int) {
				voteMap := map[string][]int{
					string(ph1.Header.Hash): {i},
				}
				sparsePrevoteProofMap := mfx.Fx.SparsePrevoteProofMap(ctx, 1, 0, voteMap)
				prevoteProof := tmconsensus.PrevoteSparseProof{
					Height:     1,
					Round:      0,
					PubKeyHash: keyHash,
					Proofs:     sparsePrevoteProofMap,
				}
				// Synchronize all goroutines on the start signal.
				<-start

				// Channel is buffered properly so don't need to check blocking send.
				feedbackCh <- m.HandlePrevoteProofs(ctx, prevoteProof)
			}(i)
		}

		// Build the full prevote map for later assertion.
		fullVoteMap := map[string][]int{
			string(ph1.Header.Hash): {0, 1, 2, 3, 4, 5, 6, 7, 8, 9}, // TODO: decouple from size constant.
		}
		fullPrevoteProofMap := mfx.Fx.PrevoteProofMap(ctx, 1, 0, fullVoteMap)

		// Now assert all 10 goroutines have had their message accepted.
		close(start)
		for range size {
			require.Equal(t, tmconsensus.HandleVoteProofsAccepted, gtest.ReceiveSoon(t, feedbackCh))
		}

		// Only asserting against the RoundView content, ignoring the versioned details.
		expNV := tmconsensus.RoundView{
			Height:       1,
			Round:        0,
			ValidatorSet: mfx.Fx.ValSet(),

			PrevCommitProof: tmconsensus.CommitProof{Proofs: map[string][]gcrypto.SparseSignature{}},

			ProposedHeaders: []tmconsensus.ProposedHeader{ph1},

			PrevoteProofs:   fullPrevoteProofMap,
			PrecommitProofs: mfx.Fx.PrecommitProofMap(ctx, 1, 0, map[string][]int{}),
		}

		vs := tmconsensus.NewVoteSummary()
		vs.SetAvailablePower(expNV.ValidatorSet.Validators)
		vs.SetPrevotePowers(expNV.ValidatorSet.Validators, expNV.PrevoteProofs)
		expNV.VoteSummary = vs

		var vnv tmconsensus.VersionedRoundView
		require.NoError(t, m.VotingView(ctx, &vnv))
		require.Equal(t, expNV, vnv.RoundView)

		_, prevotes, _, err := mfx.Cfg.RoundStore.LoadRoundState(ctx, 1, 0)
		require.NoError(t, err)

		newFullPrevotes, err := prevotes.ToFullPrevoteProofMap(
			1, 0,
			mfx.Fx.ValSet(),
			mfx.Fx.SignatureScheme, mfx.Fx.CommonMessageSignatureProofScheme,
		)
		require.NoError(t, err)

		require.Equal(t, fullPrevoteProofMap, newFullPrevotes)

		gso := gtest.ReceiveSoon(t, mfx.GossipStrategyOut)

		// The GossipStrategyOut values have empty maps set to nil.
		// Even though an earlier assertion required an empty map for PrecommitProofs,
		// in this case we clear out the field.
		require.Empty(t, expNV.PrecommitProofs)
		expNV.PrecommitProofs = nil
		require.Equal(t, expNV, gso.Voting.RoundView)
	})
}

func TestMirror_HandlePrecommitProofs(t *testing.T) {
	t.Run("happy path - available in network view and round store", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		mfx := tmmirrortest.NewFixture(ctx, t, 4)

		m := mfx.NewMirror()
		defer m.Wait()
		defer cancel()

		// Sign proposed header, because the mirror actually validates this.
		ph1 := mfx.Fx.NextProposedHeader([]byte("app_data_1"), 0)
		mfx.Fx.SignProposal(ctx, &ph1, 0)

		require.Equal(t, tmconsensus.HandleProposedHeaderAccepted, m.HandleProposedHeader(ctx, ph1))

		keyHash, _ := mfx.Fx.ValidatorHashes()

		// One active and one nil precommit.
		// Still below 2/3 voting power present.
		voteMap := map[string][]int{
			string(ph1.Header.Hash): {0},
			"":                      {1},
		}
		sparsePrecommitProofMap := mfx.Fx.SparsePrecommitProofMap(ctx, 1, 0, voteMap)
		precommitProof := tmconsensus.PrecommitSparseProof{
			Height:     1,
			Round:      0,
			PubKeyHash: keyHash,
			Proofs:     sparsePrecommitProofMap,
		}
		fullPrecommitProofMap := mfx.Fx.PrecommitProofMap(ctx, 1, 0, voteMap)

		require.Equal(t, tmconsensus.HandleVoteProofsAccepted, m.HandlePrecommitProofs(ctx, precommitProof))

		var vnv tmconsensus.VersionedRoundView
		require.NoError(t, m.VotingView(ctx, &vnv))

		vs := tmconsensus.NewVoteSummary()
		vs.SetAvailablePower(mfx.Fx.Vals())
		vs.SetPrecommitPowers(mfx.Fx.Vals(), fullPrecommitProofMap)

		require.Equal(t, tmconsensus.VersionedRoundView{
			RoundView: tmconsensus.RoundView{
				Height:       1,
				Round:        0,
				ValidatorSet: mfx.Fx.ValSet(),

				PrevCommitProof: tmconsensus.CommitProof{Proofs: map[string][]gcrypto.SparseSignature{}},

				ProposedHeaders: []tmconsensus.ProposedHeader{ph1},

				PrevoteProofs:   mfx.Fx.PrevoteProofMap(ctx, 1, 0, map[string][]int{}),
				PrecommitProofs: fullPrecommitProofMap,
				VoteSummary:     vs,
			},
			Version: 3, // Started at 1, added a proposed header (2), then bulk added precommits (3).
			PrecommitBlockVersions: map[string]uint32{
				"":                      1,
				string(ph1.Header.Hash): 1,
			},
		}, vnv)

		_, _, precommits, err := mfx.Cfg.RoundStore.LoadRoundState(ctx, 1, 0)
		require.NoError(t, err)

		newFullPrecommits, err := precommits.ToFullPrecommitProofMap(
			1, 0,
			mfx.Fx.ValSet(),
			mfx.Fx.SignatureScheme, mfx.Fx.CommonMessageSignatureProofScheme,
		)
		require.NoError(t, err)

		require.Equal(t, fullPrecommitProofMap, newFullPrecommits)
	})
}

func TestMirror_FullRound(t *testing.T) {
	for _, tc := range []struct {
		targetName string
		blockHash  func(tmconsensus.ProposedHeader) string
		wantNHR    tmmirror.NetworkHeightRound
	}{
		{
			targetName: "block",
			blockHash: func(ph tmconsensus.ProposedHeader) string {
				return string(ph.Header.Hash)
			},
			wantNHR: tmmirror.NetworkHeightRound{
				VotingHeight:     2,
				VotingRound:      0,
				CommittingHeight: 1,
				CommittingRound:  0,
			},
		},
		{
			targetName: "nil",
			blockHash: func(tmconsensus.ProposedHeader) string {
				return ""
			},
			wantNHR: tmmirror.NetworkHeightRound{
				VotingHeight: 1,
				VotingRound:  1,
				// Still committing 0 when the initial height advances to the next round.
			},
		},
	} {
		tc := tc
		t.Run("round votes for "+tc.targetName, func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			mfx := tmmirrortest.NewFixture(ctx, t, 4)
			mCh := mfx.UseMetrics(t, ctx)

			m := mfx.NewMirror()
			defer m.Wait()
			defer cancel()

			// Sign proposed header, because the mirror actually validates this.
			ph1 := mfx.Fx.NextProposedHeader([]byte("app_data_1"), 0)
			mfx.Fx.SignProposal(ctx, &ph1, 0)

			require.Equal(t, tmconsensus.HandleProposedHeaderAccepted, m.HandleProposedHeader(ctx, ph1))

			keyHash, _ := mfx.Fx.ValidatorHashes()

			// Skipping prevotes as they aren't strictly required here.

			// Half the votes arrive.
			targetHash := tc.blockHash(ph1)
			voteMapFirst := map[string][]int{
				targetHash: {0, 1},
			}
			sparsePrecommitProofMap := mfx.Fx.SparsePrecommitProofMap(ctx, 1, 0, voteMapFirst)
			firstPrecommitProof := tmconsensus.PrecommitSparseProof{
				Height:     1,
				Round:      0,
				PubKeyHash: keyHash,
				Proofs:     sparsePrecommitProofMap,
			}

			require.Equal(t, tmconsensus.HandleVoteProofsAccepted, m.HandlePrecommitProofs(ctx, firstPrecommitProof))

			// Drain the metrics before handling the precommit proofs.
			_ = gtest.ReceiveSoon(t, mCh)

			// Then the other half.
			voteMapSecond := map[string][]int{
				targetHash: {2, 3},
			}
			sparsePrecommitProofMap = mfx.Fx.SparsePrecommitProofMap(ctx, 1, 0, voteMapSecond)
			secondPrecommitProof := tmconsensus.PrecommitSparseProof{
				Height:     1,
				Round:      0,
				PubKeyHash: keyHash,
				Proofs:     sparsePrecommitProofMap,
			}
			require.Equal(t, tmconsensus.HandleVoteProofsAccepted, m.HandlePrecommitProofs(ctx, secondPrecommitProof))

			// Synchronize on the metrics update so the store read succeeds immediately.
			_ = gtest.ReceiveSoon(t, mCh)

			// Now that we have 100% of precommits for 1/0,
			// the NetworkHeightRound should be updated according to the test case.
			nhr, err := tmi.NetworkHeightRoundFromStore(mfx.Store().NetworkHeightRound(ctx))
			require.NoError(t, err)
			require.Equal(t, tc.wantNHR, nhr)
		})
	}
}

func TestMirror_pastInitialHeight(t *testing.T) {
	t.Run("initial inspection", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		mfx := tmmirrortest.NewFixture(ctx, t, 4)

		mfx.CommitInitialHeight(ctx, []byte("app_data_1"), 0, []int{0, 1, 2, 3})

		m := mfx.NewMirror()
		defer m.Wait()
		defer cancel()

		nhr, err := tmi.NetworkHeightRoundFromStore(mfx.Store().NetworkHeightRound(ctx))
		require.NoError(t, err)

		require.Equal(t, tmmirror.NetworkHeightRound{
			VotingHeight:     2,
			VotingRound:      0,
			CommittingHeight: 1,
			CommittingRound:  0,
		}, nhr)

		var vv tmconsensus.VersionedRoundView
		require.NoError(t, m.VotingView(ctx, &vv))
		// Versions start at 1 since we freshly loaded.
		require.Equal(t, uint32(1), vv.Version)
		require.Equal(t, uint64(2), vv.Height)
		require.Zero(t, vv.Round)

		// Haven't seen anything in the voting round yet,
		// so we should have no proposed headers,
		// and we should have empty proofs for nil votes.
		require.True(t, mfx.Fx.ValSet().Equal(vv.ValidatorSet))
		require.Empty(t, vv.ProposedHeaders)
		require.Empty(t, vv.PrevoteProofs)
		require.Empty(t, vv.PrecommitProofs)

		// Committing view has some information.
		require.NoError(t, m.CommittingView(ctx, &vv))
		require.Equal(t, uint32(1), vv.Version)
		require.Equal(t, uint64(1), vv.Height)
		require.Zero(t, vv.Round)

		require.Len(t, vv.ProposedHeaders, 1)
		ph := vv.ProposedHeaders[0]

		// We didn't store any prevote proofs.
		require.Empty(t, vv.PrevoteProofs)

		// And we should have all 4 signatures for the proposed header in precommit.
		require.Len(t, vv.PrecommitProofs, 1)
		np := vv.PrecommitProofs[string(ph.Header.Hash)]
		var bs bitset.BitSet
		np.SignatureBitSet(&bs)
		require.Equal(t, uint(4), bs.Count())
	})

	for _, vt := range voteTypes {
		vt := vt
		var expVoteCount uint
		if vt.Name == "prevote" {
			expVoteCount = 1 // Because we didn't store any prevotes into 1/0.
		} else if vt.Name == "precommit" {
			expVoteCount = 4
		} else {
			panic("unreachable")
		}

		t.Run("new "+vt.Name+" is accepted directly into committing round", func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			mfx := tmmirrortest.NewFixture(ctx, t, 4)

			mfx.CommitInitialHeight(ctx, []byte("app_data_1"), 0, []int{0, 1, 2}) // Last validator precommit missing.

			m := mfx.NewMirror()
			defer m.Wait()
			defer cancel()

			// Drain the initial committing view value.
			_ = gtest.ReceiveSoon(t, mfx.GossipStrategyOut)

			// Get the first block's hash through the round store.
			phs, _, _, err := mfx.Cfg.RoundStore.LoadRoundState(ctx, 1, 0)
			require.NoError(t, err)
			require.Len(t, phs, 1)
			ph1 := string(phs[0].Header.Hash)

			voter := vt.VoterFunc(mfx, m)
			res := voter.HandleProofs(ctx, 1, 0, map[string][]int{ph1: {3}})
			require.Equal(t, tmconsensus.HandleVoteProofsAccepted, res)

			// New Committing view sent on output channel.
			cv := gtest.ReceiveSoon(t, mfx.GossipStrategyOut).Committing
			var bs bitset.BitSet
			voter.ProofsFromView(cv.RoundView)[ph1].SignatureBitSet(&bs)
			require.Equal(t, expVoteCount, bs.Count())

			// Round store matches too.
			_, prevotes, precommits, err := mfx.Cfg.RoundStore.LoadRoundState(ctx, 1, 0)
			require.NoError(t, err)
			votes := voter.FullProofsFromRoundStateMaps(1, 0, mfx.Fx.ValSet(), prevotes, precommits)
			votes[ph1].SignatureBitSet(&bs)
			require.Equal(t, expVoteCount, bs.Count())
		})
	}

	t.Run("proposed header with new commit info is backfilled into committing view", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		mfx := tmmirrortest.NewFixture(ctx, t, 4)

		// Only has the first 3/4 validators in the commit info.
		mfx.CommitInitialHeight(ctx, []byte("app_data_1"), 0, []int{0, 1, 2})

		m := mfx.NewMirror()
		defer m.Wait()
		defer cancel()

		// Drain the initial value from the committing view channel.
		initCV := gtest.ReceiveSoon(t, mfx.GossipStrategyOut).Committing

		// Get the proposed header from the committing view.
		var vv tmconsensus.VersionedRoundView
		require.NoError(t, m.CommittingView(ctx, &vv))

		ph1Hash := vv.ProposedHeaders[0].Header.Hash

		// Make a new PH that contains the precommit from the last validator.
		ph2 := mfx.Fx.NextProposedHeader([]byte("app_data_2"), 0)
		precommitProofs := mfx.Fx.SparsePrecommitProofMap(ctx, 1, 0, map[string][]int{
			string(ph1Hash): {0, 1, 3}, // Missing val 2, has val 3.
		})

		ph2.Header.PrevCommitProof.Proofs = precommitProofs
		mfx.Fx.RecalculateHash(&ph2.Header)
		mfx.Fx.SignProposal(ctx, &ph2, 0)

		// Handling the proposed header should cause the new precommit
		// to backfill into the committing view.
		require.Equal(t, tmconsensus.HandleProposedHeaderAccepted, m.HandleProposedHeader(ctx, ph2))

		// Reload the committing view.
		require.NoError(t, m.CommittingView(ctx, &vv))

		// The precommits in the commit view, at height 1, now have all 4 signatures.
		commitProof := vv.PrecommitProofs[string(ph1Hash)]
		var bs bitset.BitSet
		commitProof.SignatureBitSet(&bs)
		require.Equal(t, uint(4), bs.Count())

		// And the update is available on the commit view channel.
		cv := gtest.ReceiveSoon(t, mfx.GossipStrategyOut).Committing
		require.Greater(t, cv.Version, initCV.Version)
		cv.PrecommitProofs[string(ph1Hash)].SignatureBitSet(&bs)
		require.Equal(t, uint(4), bs.Count())

		initCV.PrecommitProofs[string(ph1Hash)].SignatureBitSet(&bs)
		require.Equal(t, uint(3), bs.Count())
	})
}

func TestMirror_restart(t *testing.T) {
	t.Run("when committing height is more than 2 past initial height", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		mfx := tmmirrortest.NewFixture(ctx, t, 2)

		// Now we need to get the committing height up to 3,
		// which means voting height needs to be 4.
		//
		// To simplify this setup involving a few store writes,
		// we will just use the normal exported API.

		m := mfx.NewMirror()
		defer m.Wait()
		defer cancel()

		ph1 := mfx.Fx.NextProposedHeader([]byte("app_data_1"), 0)
		mfx.Fx.SignProposal(ctx, &ph1, 0)

		// Just send precommits, no point in doing prevotes.

		lastPH := ph1
		for i := uint64(1); i < 5; i++ {
			require.Equal(t, tmconsensus.HandleProposedHeaderAccepted, m.HandleProposedHeader(ctx, lastPH))

			voteMap := map[string][]int{
				string(lastPH.Header.Hash): {0, 1},
			}
			precommitProof := tmconsensus.PrecommitSparseProof{
				Height:     i,
				Round:      0,
				PubKeyHash: string(mfx.Fx.ValSet().PubKeyHash),
				Proofs:     mfx.Fx.SparsePrecommitProofMap(ctx, i, 0, voteMap),
			}
			require.Equal(t, tmconsensus.HandleVoteProofsAccepted.String(), m.HandlePrecommitProofs(ctx, precommitProof).String())

			dataID := []byte(fmt.Sprintf("app_state_%d", i))
			mfx.Fx.CommitBlock(lastPH.Header, dataID, 0, mfx.Fx.PrecommitProofMap(ctx, i, 0, voteMap))

			lastPH = mfx.Fx.NextProposedHeader([]byte(fmt.Sprintf("app_data_%d", i)), 0)
			mfx.Fx.SignProposal(ctx, &lastPH, 0)
		}

		// Now stop the running mirror.
		cancel()
		m.Wait()

		// And start a new one.
		// We have to use a new fixture for this with the old stores.
		// There is some state carried over that makes a restart almost always lock up,
		// if we try to make another mirror from the original fixture.
		ctx, cancel = context.WithCancel(context.Background())
		defer cancel()

		mfx2 := tmmirrortest.NewFixture(ctx, t, 2)
		mfx2.Cfg.Store = mfx.Cfg.Store
		mfx2.Cfg.CommittedHeaderStore = mfx.Cfg.CommittedHeaderStore
		mfx2.Cfg.RoundStore = mfx.Cfg.RoundStore
		mfx2.Cfg.ValidatorStore = mfx.Cfg.ValidatorStore

		m = mfx2.NewMirror()
		defer m.Wait()
		defer cancel()

		// We already have lastPH seeded as a new entry,
		// so just make sure it is handled correctly
		require.Equal(t, tmconsensus.HandleProposedHeaderAccepted, m.HandleProposedHeader(ctx, lastPH))
	})
}

func TestMirror_CommitToBlockStore(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mfx := tmmirrortest.NewFixture(ctx, t, 4)
	mCh := mfx.UseMetrics(t, ctx)

	m := mfx.NewMirror()
	defer m.Wait()
	defer cancel()

	ph1 := mfx.Fx.NextProposedHeader([]byte("app_data_1"), 0)
	mfx.Fx.SignProposal(ctx, &ph1, 0)

	require.Equal(t, tmconsensus.HandleProposedHeaderAccepted, m.HandleProposedHeader(ctx, ph1))

	// Now advance the fixture with a full set of precommits.
	voteMap1 := map[string][]int{
		string(ph1.Header.Hash): {0, 1, 2, 3},
	}
	precommitProofs1 := mfx.Fx.PrecommitProofMap(ctx, 1, 0, voteMap1)
	mfx.Fx.CommitBlock(ph1.Header, []byte("app_state_height_1"), 0, precommitProofs1)

	// And advance the mirror by submitting a full set of precommits.
	keyHash, _ := mfx.Fx.ValidatorHashes()
	require.Equal(t, tmconsensus.HandleVoteProofsAccepted, m.HandlePrecommitProofs(ctx, tmconsensus.PrecommitSparseProof{
		Height: 1,
		Round:  0,

		PubKeyHash: keyHash,

		Proofs: mfx.Fx.SparsePrecommitProofMap(ctx, 1, 0, voteMap1),
	}))

	// Read a gossip strategy value in order to synchronize here.
	_ = gtest.ReceiveSoon(t, mfx.GossipStrategyOut)

	// Now on height 2, we have a committing view at height 1.
	// So, the subjective commit for 1 should be in the header store.
	h, err := mfx.Cfg.CommittedHeaderStore.LoadCommittedHeader(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, tmconsensus.CommittedHeader{
		Header: ph1.Header,
		Proof: tmconsensus.CommitProof{
			Round:      0,
			PubKeyHash: keyHash,
			Proofs:     mfx.Fx.SparsePrecommitProofMap(ctx, 1, 0, voteMap1),
		},
	}, h)

	// Now propose and commit height 2.
	ph2 := mfx.Fx.NextProposedHeader([]byte("app_data_2"), 0)
	mfx.Fx.SignProposal(ctx, &ph2, 0)

	require.Equal(t, tmconsensus.HandleProposedHeaderAccepted, m.HandleProposedHeader(ctx, ph2))

	// Drain metrics.
	_ = gtest.ReceiveSoon(t, mCh)

	// Update the fixture for height 2.
	voteMap2 := map[string][]int{
		string(ph2.Header.Hash): {0, 1, 2, 3},
	}
	precommitProofs2 := mfx.Fx.PrecommitProofMap(ctx, 2, 0, voteMap2)
	mfx.Fx.CommitBlock(ph1.Header, []byte("app_state_height_2"), 0, precommitProofs2)

	require.Equal(t, tmconsensus.HandleVoteProofsAccepted, m.HandlePrecommitProofs(ctx, tmconsensus.PrecommitSparseProof{
		Height: 2,
		Round:  0,

		PubKeyHash: keyHash,

		Proofs: mfx.Fx.SparsePrecommitProofMap(ctx, 2, 0, voteMap2),
	}))

	// Synchronize on metrics before inspecting the mirror store.
	_ = gtest.ReceiveSoon(t, mCh)

	// Now we are voting on height 3, and committing height 2.
	wantNHR := tmmirror.NetworkHeightRound{
		VotingHeight: 3,
		VotingRound:  0,

		CommittingHeight: 2,
		CommittingRound:  0,
	}
	nhr, err := tmi.NetworkHeightRoundFromStore(mfx.Store().NetworkHeightRound(ctx))
	require.NoError(t, err)
	require.Equal(t, wantNHR, nhr)

	// This means height 1 must be in the header store.
	cb1, err := mfx.Cfg.CommittedHeaderStore.LoadCommittedHeader(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, ph1.Header, cb1.Header)
	require.Equal(t, ph2.Header.PrevCommitProof, cb1.Proof)
	require.Equal(t, tmconsensus.CommittedHeader{
		Header: ph1.Header,
		Proof:  ph2.Header.PrevCommitProof,
	}, cb1)
}

func TestMirror_nilPrecommitAdvancesRound(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mfx := tmmirrortest.NewFixture(ctx, t, 4)
	mCh := mfx.UseMetrics(t, ctx)

	m := mfx.NewMirror()
	defer m.Wait()
	defer cancel()

	// Drain the metrics before handling the precommit proofs.
	_ = gtest.ReceiveSoon(t, mCh)

	// See a full set of nil precommits at the first height and round.
	voteMap1 := map[string][]int{
		"": {0, 1, 2, 3},
	}
	keyHash, _ := mfx.Fx.ValidatorHashes()
	require.Equal(t, tmconsensus.HandleVoteProofsAccepted, m.HandlePrecommitProofs(ctx, tmconsensus.PrecommitSparseProof{
		Height: 1,
		Round:  0,

		PubKeyHash: keyHash,

		Proofs: mfx.Fx.SparsePrecommitProofMap(ctx, 1, 0, voteMap1),
	}))

	// Synchronize on the metrics update so the store read succeeds immediately.
	_ = gtest.ReceiveSoon(t, mCh)

	// Voting round advances, committing height remains at zero.
	wantNHR := tmmirror.NetworkHeightRound{
		VotingHeight: 1,
		VotingRound:  1,

		CommittingHeight: 0,
		CommittingRound:  0,
	}
	nhr, err := tmi.NetworkHeightRoundFromStore(mfx.Store().NetworkHeightRound(ctx))
	require.NoError(t, err)
	require.Equal(t, wantNHR, nhr)

	// And the voting view output reflects the new round.
	vv := gtest.ReceiveSoon(t, mfx.GossipStrategyOut).Voting
	require.Equal(t, uint32(2), vv.Version) // Would have been version 1 except it was shifted from NextRound.
	require.Equal(t, uint64(1), vv.Height)
	require.Equal(t, uint32(1), vv.Round)

	// The nil prevote and precommit are present but empty.
	require.Empty(t, vv.PrevoteProofs)
	require.Empty(t, vv.PrecommitProofs)
}

func TestMirror_advanceRoundOnMixedPrecommit(t *testing.T) {
	t.Run("when all validators have precommitted but no block has majority", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		mfx := tmmirrortest.NewFixture(ctx, t, 4)

		m := mfx.NewMirror()
		defer m.Wait()
		defer cancel()

		ph1 := mfx.Fx.NextProposedHeader([]byte("app_data_1"), 0)
		mfx.Fx.SignProposal(ctx, &ph1, 0)

		require.Equal(t, tmconsensus.HandleProposedHeaderAccepted, m.HandleProposedHeader(ctx, ph1))

		// Half of the precommits are for the block, the other half are for nil.
		voteMap1 := map[string][]int{
			string(ph1.Header.Hash): {0, 1},
			"":                      {2, 3},
		}
		keyHash, _ := mfx.Fx.ValidatorHashes()
		require.Equal(t, tmconsensus.HandleVoteProofsAccepted, m.HandlePrecommitProofs(ctx, tmconsensus.PrecommitSparseProof{
			Height: 1,
			Round:  0,

			PubKeyHash: keyHash,

			Proofs: mfx.Fx.SparsePrecommitProofMap(ctx, 1, 0, voteMap1),
		}))

		// The voting view advanced to the next round.
		gso := gtest.ReceiveSoon(t, mfx.GossipStrategyOut)
		vv := gso.Voting

		// It may seem like this should be Version 1 since the entire voting view is empty at this point:
		// it doesn't have any proposed headers or votes.
		// But it was a NextRound value which started at Version 1,
		// and we increment the version again when the otherwise-unchanged view
		// gets shifted from NextRound to Voting.
		require.Equal(t, uint32(2), vv.Version)
		require.Equal(t, uint64(1), vv.Height)
		require.Equal(t, uint32(1), vv.Round)
		require.Empty(t, vv.ProposedHeaders)

		// The nil prevote and precommit are present but empty.
		require.Empty(t, vv.PrevoteProofs)
		require.Empty(t, vv.PrecommitProofs)

		// No new committing update as a result of advancing the round.
		require.Nil(t, gso.Committing)
	})
}

// The proposed header is too old or too far in the future.
func TestMirror_proposedBlockOutOfBounds(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mfx := tmmirrortest.NewFixture(ctx, t, 2)

	m := mfx.NewMirror()
	defer m.Wait()
	defer cancel()

	// Hold on to a ph1 for the committing round.
	// (We will never send this although it will be otherwise valid.)
	ph10 := mfx.Fx.NextProposedHeader([]byte("missed"), 1)
	mfx.Fx.SignProposal(ctx, &ph10, 1)

	// Full nil precommit for 1/0.
	keyHash, _ := mfx.Fx.ValidatorHashes()
	voteMap10 := map[string][]int{
		"": {0, 1},
	}
	require.Equal(t, tmconsensus.HandleVoteProofsAccepted, m.HandlePrecommitProofs(ctx, tmconsensus.PrecommitSparseProof{
		Height: 1,
		Round:  0,

		PubKeyHash: keyHash,

		Proofs: mfx.Fx.SparsePrecommitProofMap(ctx, 1, 0, voteMap10),
	}))

	// Now we must be voting on 1/1.
	ph11 := mfx.Fx.NextProposedHeader([]byte("app_data_1"), 0)
	ph11.Round = 1
	mfx.Fx.SignProposal(ctx, &ph11, 0)

	require.Equal(t, tmconsensus.HandleProposedHeaderAccepted, m.HandleProposedHeader(ctx, ph11))

	// Now fully precommit 1/1, so we get to voting on height 2.
	voteMap11 := map[string][]int{
		string(ph11.Header.Hash): {0, 1},
	}
	require.Equal(t, tmconsensus.HandleVoteProofsAccepted, m.HandlePrecommitProofs(ctx, tmconsensus.PrecommitSparseProof{
		Height: 1,
		Round:  1,

		PubKeyHash: keyHash,

		Proofs: mfx.Fx.SparsePrecommitProofMap(ctx, 1, 1, voteMap11),
	}))

	// Now if the proposed header for 1/0 arrives, it's a "too old" result.
	// Arguably we could check or save it for detecting a double-propose,
	// but at this point we will just drop it.
	require.Equal(t, tmconsensus.HandleProposedHeaderRoundTooOld, m.HandleProposedHeader(ctx, ph10))

	// And now let's make a fake proposed header multiple heights in the future,
	// from a different fixture altogether so the voting hashes and validators differ.
	ffx := tmconsensustest.NewStandardFixture(5)
	for i := 0; i < 10; i++ {
		ph := ffx.NextProposedHeader([]byte("ignore"), 4)

		ffx.CommitBlock(ph.Header, []byte(fmt.Sprintf("height_%d", i+1)), 0, map[string]gcrypto.CommonMessageSignatureProof{
			string(ph.Header.Hash): ffx.PrecommitSignatureProof(
				ctx,
				tmconsensus.VoteTarget{
					Height:    uint64(i) + 1,
					Round:     0,
					BlockHash: string(ph.Header.Hash),
				}, nil, []int{0, 1, 2, 3, 4},
			),
		})
	}

	futurePH := ffx.NextProposedHeader([]byte("far_future"), 4)
	ffx.SignProposal(ctx, &futurePH, 4)

	require.Equal(t, tmconsensus.HandleProposedHeaderRoundTooFarInFuture, m.HandleProposedHeader(ctx, futurePH))
}

func TestMirror_votesBeforeVotingRound(t *testing.T) {
	for _, viewStatus := range []tmi.ViewLookupStatus{tmi.ViewBeforeCommitting, tmi.ViewOrphaned} {
		viewStatus := viewStatus
		for _, vt := range voteTypes {
			vt := vt
			t.Run(vt.Name+" into "+viewStatus.String(), func(t *testing.T) {
				t.Parallel()

				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()

				mfx := tmmirrortest.NewFixture(ctx, t, 2)
				mCh := mfx.UseMetrics(t, ctx)

				// Easy way to get voting height to 2.
				mfx.CommitInitialHeight(ctx, []byte("app_state_1"), 0, []int{0, 1})

				m := mfx.NewMirror()
				defer m.Wait()
				defer cancel()

				// Propose header at height 2.
				ph2 := mfx.Fx.NextProposedHeader([]byte("app_data_2"), 0)
				mfx.Fx.SignProposal(ctx, &ph2, 0)

				require.Equal(t, tmconsensus.HandleProposedHeaderAccepted, m.HandleProposedHeader(ctx, ph2))

				// Drain the metrics before handling the precommit proofs.
				_ = gtest.ReceiveSoon(t, mCh)

				// Full precommit for ph2.
				voteMap2 := map[string][]int{
					string(ph2.Header.Hash): {0, 1},
				}
				keyHash, _ := mfx.Fx.ValidatorHashes()
				require.Equal(t, tmconsensus.HandleVoteProofsAccepted, m.HandlePrecommitProofs(ctx, tmconsensus.PrecommitSparseProof{
					Height: 2,
					Round:  0,

					PubKeyHash: keyHash,

					Proofs: mfx.Fx.SparsePrecommitProofMap(ctx, 2, 0, voteMap2),
				}))

				wantVotingRound := uint32(0)
				if viewStatus == tmi.ViewOrphaned {
					// Drain the metrics again if necessary.
					_ = gtest.ReceiveSoon(t, mCh)

					// Need to advance the voting round by 1.
					// Drain the voting output channel first.
					voteMapNil3 := map[string][]int{
						"": {0, 1},
					}
					require.Equal(t, tmconsensus.HandleVoteProofsAccepted, m.HandlePrecommitProofs(ctx, tmconsensus.PrecommitSparseProof{
						Height: 3,
						Round:  0,

						PubKeyHash: keyHash,

						Proofs: mfx.Fx.SparsePrecommitProofMap(ctx, 3, 0, voteMapNil3),
					}))

					wantVotingRound = 1
				}

				// Synchronize on a metrics update before inspecting the store.
				_ = gtest.ReceiveSoon(t, mCh)

				// Now header 2 is in committing.
				nhr, err := tmi.NetworkHeightRoundFromStore(mfx.Store().NetworkHeightRound(ctx))
				require.NoError(t, err)
				require.Equal(t, tmmirror.NetworkHeightRound{
					VotingHeight:     3,
					VotingRound:      wantVotingRound,
					CommittingHeight: 2,
					CommittingRound:  0,
				}, nhr)

				var targetHeight uint64
				var targetBlockHash string
				switch viewStatus {
				case tmi.ViewOrphaned:
					// Nil vote at 3/0.
					targetHeight = 3
					targetBlockHash = ""
				case tmi.ViewBeforeCommitting:
					// Vote for the first proposed header,
					// which we don't have directly because it was scoped inside the mfx.CommitInitialHeight call.
					targetHeight = 1
					targetBlockHash = string(ph2.Header.PrevBlockHash)
				default:
					t.Fatalf("BUG: unhandled view status %s", viewStatus)
				}

				voter := vt.VoterFunc(mfx, m)
				res := voter.HandleProofs(ctx, targetHeight, 0, map[string][]int{targetBlockHash: {0}})
				require.Equal(t, tmconsensus.HandleVoteProofsRoundTooOld, res)
			})
		}
	}
}

func TestMirror_fetchProposedBlock(t *testing.T) {
	for _, vt := range voteTypes {
		vt := vt
		t.Run("fetch initiated when "+vt.Name+"s exceed minority voting power", func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			mfx := tmmirrortest.NewFixture(ctx, t, 4)

			phf := tmelinktest.NewPHFetcher(1, 1)
			mfx.Cfg.ProposedHeaderFetcher = phf.ProposedHeaderFetcher()

			m := mfx.NewMirror()
			defer m.Wait()
			defer cancel()

			voter := vt.VoterFunc(mfx, m)

			// Make the proposed header but don't give it to the mirror.
			ph1 := mfx.Fx.NextProposedHeader([]byte("app_data_1"), 0)
			mfx.Fx.SignProposal(ctx, &ph1, 0)

			// With one ote there is no fetch request.
			voteMap := map[string][]int{
				string(ph1.Header.Hash): {0},
			}

			res := voter.HandleProofs(ctx, 1, 0, voteMap)
			require.Equal(t, tmconsensus.HandleVoteProofsAccepted, res)

			gtest.NotSending(t, phf.ReqCh)

			// With a second vote, we have exceeded minority power, so there must be a fetch request.
			voteMap[string(ph1.Header.Hash)] = []int{1}
			res = voter.HandleProofs(ctx, 1, 0, voteMap)
			require.Equal(t, tmconsensus.HandleVoteProofsAccepted, res)

			req := gtest.ReceiveSoon(t, phf.ReqCh)
			require.Equal(t, uint64(1), req.Height)
			require.Equal(t, string(ph1.Header.Hash), req.BlockHash)
			require.NoError(t, req.Ctx.Err()) // Context is non-nil and has not been canceled.

			// And with a third vote, there is no new request.
			voteMap[string(ph1.Header.Hash)] = []int{2}
			res = voter.HandleProofs(ctx, 1, 0, voteMap)
			require.Equal(t, tmconsensus.HandleVoteProofsAccepted, res)

			gtest.NotSending(t, phf.ReqCh)

			// Drain the voting view before we send the proposed header via the fetch channel.
			vrv := gtest.ReceiveSoon(t, mfx.GossipStrategyOut).Voting
			require.Empty(t, vrv.ProposedHeaders) // Sanity check.

			gtest.SendSoon(t, phf.FetchedCh, ph1)

			if vt.Name == "prevote" {
				// The arrival of the fetched header should be reflected in the voting view.
				vrv = gtest.ReceiveSoon(t, mfx.GossipStrategyOut).Voting
				require.Equal(t, []tmconsensus.ProposedHeader{ph1}, vrv.ProposedHeaders)

				// And its arrival should have canceled the context, as a matter of cleanup.
				require.Error(t, req.Ctx.Err())
			} else if vt.Name == "precommit" {
				// The arrival of the fetched header should advance the voting view to the next height.
				vrv = gtest.ReceiveSoon(t, mfx.GossipStrategyOut).Voting
				require.Equal(t, uint64(2), vrv.Height)
				require.Zero(t, vrv.Round)
				require.Empty(t, vrv.ProposedHeaders)

				// And its arrival should have canceled the context, as a matter of cleanup.
				require.Error(t, req.Ctx.Err())
			} else {
				panic("unreachable")
			}
		})
	}

	t.Run("initiated fetch cancelled when proposed header arrives from network", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		mfx := tmmirrortest.NewFixture(ctx, t, 4)

		phf := tmelinktest.NewPHFetcher(1, 1)
		mfx.Cfg.ProposedHeaderFetcher = phf.ProposedHeaderFetcher()

		m := mfx.NewMirror()
		defer m.Wait()
		defer cancel()

		// Make the proposed header but don't give it to the mirror.
		ph1 := mfx.Fx.NextProposedHeader([]byte("app_data_1"), 0)
		mfx.Fx.SignProposal(ctx, &ph1, 0)

		// With two prevotes, there is a fetch request.
		voteMap := map[string][]int{
			string(ph1.Header.Hash): {0, 1},
		}

		keyHash, _ := mfx.Fx.ValidatorHashes()
		res := m.HandlePrevoteProofs(ctx, tmconsensus.PrevoteSparseProof{
			Height: 1,
			Round:  0,

			PubKeyHash: keyHash,

			Proofs: mfx.Fx.SparsePrevoteProofMap(ctx, 1, 0, voteMap),
		})
		require.Equal(t, tmconsensus.HandleVoteProofsAccepted, res)

		req := gtest.ReceiveSoon(t, phf.ReqCh)
		require.Equal(t, uint64(1), req.Height)
		require.Equal(t, string(ph1.Header.Hash), req.BlockHash)
		require.NoError(t, req.Ctx.Err()) // Context is non-nil and has not been canceled.

		// Sending the proposed header through the normal channel, is accepted as usual.
		require.Equal(t, tmconsensus.HandleProposedHeaderAccepted, m.HandleProposedHeader(ctx, ph1))

		// The proposed header is reflected in the voting view.
		vrv := gtest.ReceiveSoon(t, mfx.GossipStrategyOut).Voting
		require.Equal(t, []tmconsensus.ProposedHeader{ph1}, vrv.ProposedHeaders)

		// And its arrival should have canceled the context, as a matter of cleanup.
		require.Error(t, req.Ctx.Err())
	})
}

func TestMirror_nextRound(t *testing.T) {
	t.Run("proposed header", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		mfx := tmmirrortest.NewFixture(ctx, t, 4)

		// We are at 1/0, so a received proposed header at 1/1 must be saved to the NextRound view.
		ph11 := mfx.Fx.NextProposedHeader([]byte("app_data_1"), 0)
		ph11.Round = 1
		mfx.Fx.RecalculateHash(&ph11.Header)
		mfx.Fx.SignProposal(ctx, &ph11, 0)

		m := mfx.NewMirror()
		defer m.Wait()
		defer cancel()

		// Drain the voting view and next round outputs first.
		_ = gtest.ReceiveSoon(t, mfx.GossipStrategyOut)

		// Proposed header for 1/1 is accepted.
		require.Equal(t, tmconsensus.HandleProposedHeaderAccepted, m.HandleProposedHeader(ctx, ph11))

		// Just a proposed header is not sufficient to advance the round.
		// The voting view hasn't changed.
		gso := gtest.ReceiveSoon(t, mfx.GossipStrategyOut)
		require.Nil(t, gso.Voting)

		// But the next round view has the proposed header.
		nrrv := gso.NextRound
		require.Equal(t, uint64(1), nrrv.Height)
		require.Equal(t, uint32(1), nrrv.Round)
		require.Equal(t, []tmconsensus.ProposedHeader{ph11}, nrrv.ProposedHeaders)

		// Then if we get a full set of nil precommits to advance the round from 0 to 1...
		voteMapNil := map[string][]int{
			"": {0, 1, 2, 3},
		}
		keyHash, powHash := mfx.Fx.ValidatorHashes()
		require.Equal(t, tmconsensus.HandleVoteProofsAccepted, m.HandlePrecommitProofs(ctx, tmconsensus.PrecommitSparseProof{
			Height: 1,
			Round:  0,

			PubKeyHash: keyHash,

			Proofs: mfx.Fx.SparsePrecommitProofMap(ctx, 1, 0, voteMapNil),
		}))

		// The voting view shows the proposed header, and the next round doesn't.
		gso = gtest.ReceiveSoon(t, mfx.GossipStrategyOut)
		vrv := gso.Voting
		require.Equal(t, uint64(1), vrv.Height)
		require.Equal(t, uint32(1), vrv.Round)
		require.Equal(t, []tmconsensus.ProposedHeader{ph11}, vrv.ProposedHeaders)
		require.Equal(t, keyHash, string(vrv.ValidatorSet.PubKeyHash))
		require.Equal(t, powHash, string(vrv.ValidatorSet.VotePowerHash))

		nrrv = gso.NextRound
		require.Equal(t, uint64(1), nrrv.Height)
		require.Equal(t, uint32(2), nrrv.Round)
		require.Empty(t, nrrv.ProposedHeaders)
	})

	for _, vt := range voteTypes {
		vt := vt
		t.Run(vt.Name, func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			mfx := tmmirrortest.NewFixture(ctx, t, 4)

			// We will be fetching a missed PH later in the test.
			phf := tmelinktest.NewPHFetcher(1, 1)
			mfx.Cfg.ProposedHeaderFetcher = phf.ProposedHeaderFetcher()

			// We are at 1/0, and we are going to vote on a proposed header for 1/1.
			ph11 := mfx.Fx.NextProposedHeader([]byte("app_data_1"), 0)
			ph11.Round = 1
			mfx.Fx.RecalculateHash(&ph11.Header)
			mfx.Fx.SignProposal(ctx, &ph11, 0)

			m := mfx.NewMirror()
			defer m.Wait()
			defer cancel()

			// Drain the voting view and next round outputs first.
			_ = gtest.ReceiveSoon(t, mfx.GossipStrategyOut)

			// Vote for 1/1 is accepted.
			voter := vt.VoterFunc(mfx, m)
			res := voter.HandleProofs(ctx, 1, 1, map[string][]int{string(ph11.Header.Hash): {0}})
			require.Equal(t, tmconsensus.HandleVoteProofsAccepted, res)

			// The Next Round View has the votes.
			nrrv := gtest.ReceiveSoon(t, mfx.GossipStrategyOut).NextRound
			require.Equal(t, uint64(1), nrrv.Height)
			require.Equal(t, uint32(1), nrrv.Round)
			proofs := voter.ProofsFromView(nrrv.RoundView)
			var bs bitset.BitSet
			proofs[string(ph11.Header.Hash)].SignatureBitSet(&bs)
			require.Equal(t, uint(1), bs.Count())

			// And so does the round store.
			_, prevotes, precommits, err := mfx.Cfg.RoundStore.LoadRoundState(ctx, 1, 1)
			require.NoError(t, err)
			votes := voter.FullProofsFromRoundStateMaps(1, 1, mfx.Fx.ValSet(), prevotes, precommits)
			votes[string(ph11.Header.Hash)].SignatureBitSet(&bs)
			require.Equal(t, uint(1), bs.Count())

			// Now if we exceed the minority vote for that header on 1/1...
			res = voter.HandleProofs(ctx, 1, 1, map[string][]int{string(ph11.Header.Hash): {0, 1}}) // 2/4 validators.
			require.Equal(t, tmconsensus.HandleVoteProofsAccepted, res)

			// The voting round is now 1/1,
			// even if the vote was precommit, because we didn't get the proposed header yet.
			vrv := gtest.ReceiveSoon(t, mfx.GossipStrategyOut).Voting
			require.Equal(t, uint64(1), vrv.Height)
			require.Equal(t, uint32(1), vrv.Round)

			// And because we crossed the minority threshold,
			// there was a request for the header we missed.
			req := gtest.ReceiveSoon(t, phf.ReqCh)
			require.Equal(t, uint64(1), req.Height)
			require.Equal(t, string(ph11.Header.Hash), req.BlockHash)
			require.NoError(t, req.Ctx.Err()) // Context is non-nil and has not been canceled.
		})
	}
}

func TestMirror_stateMachineActions(t *testing.T) {
	t.Run("values for current voting round accepted", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		mfx := tmmirrortest.NewFixture(ctx, t, 4)

		m := mfx.NewMirror()
		defer m.Wait()
		defer cancel()

		// Drain the voting view.
		_ = gtest.ReceiveSoon(t, mfx.GossipStrategyOut)

		// Proposed header from state machine accepted.
		ph := mfx.Fx.NextProposedHeader([]byte("app_data_1"), 0)
		mfx.Fx.SignProposal(ctx, &ph, 0)

		actionCh := make(chan tmeil.StateMachineRoundAction, 3)
		re := tmeil.StateMachineRoundEntrance{
			H:        1,
			R:        0,
			PubKey:   mfx.Fx.Vals()[0].PubKey,
			Actions:  actionCh,
			Response: make(chan tmeil.RoundEntranceResponse, 1),
		}
		gtest.SendSoon(t, mfx.StateMachineRoundEntranceIn, re)

		// This is the initial height so the mirror only populates the round view.
		rer := gtest.ReceiveSoon(t, re.Response)
		require.Empty(t, rer.CH.Header.Hash)
		require.Empty(t, rer.CH.Proof.Proofs)

		require.Equal(t, uint64(1), rer.VRV.Height)
		require.Empty(t, rer.VRV.ProposedHeaders)
		require.Empty(t, rer.VRV.PrevoteProofs)
		require.Empty(t, rer.VRV.PrecommitProofs)

		t.Run("proposed header", func(t *testing.T) {
			// Buffered channel so we can just send without select.
			actionCh <- tmeil.StateMachineRoundAction{PH: ph}

			vrv := gtest.ReceiveSoon(t, mfx.GossipStrategyOut).Voting
			require.Equal(t, []tmconsensus.ProposedHeader{ph}, vrv.ProposedHeaders)
		})

		phHash := string(ph.Header.Hash)

		t.Run("prevote", func(t *testing.T) {
			vt := tmconsensus.VoteTarget{
				Height: 1, Round: 0,
				BlockHash: phHash,
			}
			signContent, err := tmconsensus.PrevoteSignBytes(vt, mfx.Fx.SignatureScheme)
			require.NoError(t, err)
			// Buffered channel so we can just send without select.
			actionCh <- tmeil.StateMachineRoundAction{
				Prevote: tmeil.ScopedSignature{
					TargetHash:  phHash,
					SignContent: signContent,
					Sig:         mfx.Fx.PrevoteSignature(ctx, vt, 0),
				},
			}

			vrv := gtest.ReceiveSoon(t, mfx.GossipStrategyOut).Voting
			// Nil block is always populated, just clear it for a simpler assertion here.
			delete(vrv.PrevoteProofs, "")
			require.Equal(t, map[string]gcrypto.CommonMessageSignatureProof{
				phHash: mfx.Fx.PrevoteSignatureProof(ctx, vt, nil, []int{0}),
			}, vrv.PrevoteProofs)
		})

		t.Run("precommit", func(t *testing.T) {
			vt := tmconsensus.VoteTarget{
				Height: 1, Round: 0,
				BlockHash: phHash,
			}
			signContent, err := tmconsensus.PrecommitSignBytes(vt, mfx.Fx.SignatureScheme)
			require.NoError(t, err)
			// Buffered channel so we can just send without select.
			actionCh <- tmeil.StateMachineRoundAction{
				Precommit: tmeil.ScopedSignature{
					TargetHash:  phHash,
					SignContent: signContent,
					Sig:         mfx.Fx.PrecommitSignature(ctx, vt, 0),
				},
			}

			vrv := gtest.ReceiveSoon(t, mfx.GossipStrategyOut).Voting
			// Nil block is always populated, just clear it for a simpler assertion here.
			delete(vrv.PrecommitProofs, "")
			require.Equal(t, map[string]gcrypto.CommonMessageSignatureProof{
				phHash: mfx.Fx.PrecommitSignatureProof(ctx, vt, nil, []int{0}),
			}, vrv.PrecommitProofs)
		})
	})

	t.Run("committing view sent as VRV", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		mfx := tmmirrortest.NewFixture(ctx, t, 4)

		// Easy way to get voting height to 2.
		mfx.CommitInitialHeight(ctx, []byte("app_state_1"), 0, []int{0, 1, 2, 3})

		m := mfx.NewMirror()
		defer m.Wait()
		defer cancel()

		// State machine sends round update.
		actionCh := make(chan tmeil.StateMachineRoundAction, 3)
		re := tmeil.StateMachineRoundEntrance{
			H:        1,
			R:        0,
			PubKey:   mfx.Fx.Vals()[0].PubKey,
			Actions:  actionCh,
			Response: make(chan tmeil.RoundEntranceResponse, 1),
		}
		gtest.SendSoon(t, mfx.StateMachineRoundEntranceIn, re)

		rer := gtest.ReceiveSoon(t, re.Response)
		// A committing header is not the same as a committed header.
		// We have sufficient info to commit,
		// but we don't know the canonical commits that will be persisted to chain.
		require.Empty(t, rer.CH.Header.Hash)
		require.Empty(t, rer.CH.Proof.Proofs)

		phs, _, _, err := mfx.Cfg.RoundStore.LoadRoundState(ctx, 1, 0)
		require.NoError(t, err)
		require.Len(t, phs, 1)

		require.Equal(t, uint64(1), rer.VRV.Height)
		require.Zero(t, rer.VRV.Round)
		require.Equal(t, phs, rer.VRV.ProposedHeaders)
		require.Empty(t, rer.VRV.PrevoteProofs)    // No prevotes were stored, that's fine.
		require.Len(t, rer.VRV.PrecommitProofs, 1) // Just proof for the block.
	})

	t.Run("historic header sent as CommittedHeader", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		mfx := tmmirrortest.NewFixture(ctx, t, 4)

		// Easy way to get voting height to 2.
		mfx.CommitInitialHeight(ctx, []byte("app_state_1"), 0, []int{0, 1, 2, 3})

		m := mfx.NewMirror()
		defer m.Wait()
		defer cancel()

		// Move through height 2 before state machine does anything.

		// Propose header at height 2.
		ph2 := mfx.Fx.NextProposedHeader([]byte("app_data_2"), 0)
		mfx.Fx.SignProposal(ctx, &ph2, 0)
		require.Len(t, ph2.Header.PrevCommitProof.Proofs, 1) // Height 2 has the commit proof for 1.
		require.NotNil(t, ph2.Header.PrevCommitProof.Proofs[string(ph2.Header.PrevBlockHash)])

		require.Equal(t, tmconsensus.HandleProposedHeaderAccepted, m.HandleProposedHeader(ctx, ph2))

		// Full precommit for ph2.
		voteMap2 := map[string][]int{
			string(ph2.Header.Hash): {0, 1, 2, 3},
		}
		keyHash, _ := mfx.Fx.ValidatorHashes()
		require.Equal(t, tmconsensus.HandleVoteProofsAccepted, m.HandlePrecommitProofs(ctx, tmconsensus.PrecommitSparseProof{
			Height: 2,
			Round:  0,

			PubKeyHash: keyHash,

			Proofs: mfx.Fx.SparsePrecommitProofMap(ctx, 2, 0, voteMap2),
		}))

		// Ensure the mirror is on height 3.
		gso := gtest.ReceiveSoon(t, mfx.GossipStrategyOut)
		vrv := gso.Voting
		require.Equal(t, uint64(3), vrv.Height)
		cv := gso.Committing
		require.NotNil(t, cv)
		require.Equal(t, uint64(2), cv.Height)
		require.Len(t, cv.PrevCommitProof.Proofs, 1)

		// Now height 1 is fully committed (from the fixture).
		ch1, err := mfx.Cfg.CommittedHeaderStore.LoadCommittedHeader(ctx, 1)
		require.NoError(t, err)

		// And height 2 is committed since we went through normal flow.
		ch2, err := mfx.Cfg.CommittedHeaderStore.LoadCommittedHeader(ctx, 2)
		require.NoError(t, err)
		require.Equal(t, tmconsensus.CommittedHeader{
			Header: ph2.Header,
			Proof: tmconsensus.CommitProof{
				PubKeyHash: gso.Voting.PrevCommitProof.PubKeyHash,
				Proofs:     gso.Voting.PrevCommitProof.Proofs,
			},
		}, ch2)

		// So, if the state machine starts up now at height 1, by first sending its round entrance...
		actionCh := make(chan tmeil.StateMachineRoundAction, 3)
		re := tmeil.StateMachineRoundEntrance{
			H:        1,
			R:        0,
			PubKey:   mfx.Fx.Vals()[0].PubKey,
			Actions:  actionCh,
			Response: make(chan tmeil.RoundEntranceResponse, 1),
		}
		gtest.SendSoon(t, mfx.StateMachineRoundEntranceIn, re)

		rer := gtest.ReceiveSoon(t, re.Response)

		// The whole VRV in the round entrance is blank, because we got a committed header replay.
		require.Zero(t, rer.VRV.Height)

		// But the committed header matches what was in the store.
		require.Equal(t, ch1, rer.CH)

		// Then if the state machine does all its work behind the scenes
		// and then enters height 2:
		re = tmeil.StateMachineRoundEntrance{
			H:        2,
			R:        0,
			PubKey:   mfx.Fx.Vals()[0].PubKey,
			Actions:  actionCh,
			Response: make(chan tmeil.RoundEntranceResponse, 1),
		}
		gtest.SendSoon(t, mfx.StateMachineRoundEntranceIn, re)

		rer = gtest.ReceiveSoon(t, re.Response)
		require.Empty(t, rer.CH.Header.Hash) // Nothing in the CommittedHeader.
		require.Equal(t, uint64(2), rer.VRV.Height)
	})

	t.Run("state machine precommit accepted when it arrives into committing view", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		mfx := tmmirrortest.NewFixture(ctx, t, 4)

		m := mfx.NewMirror()
		defer m.Wait()
		defer cancel()

		actionCh := make(chan tmeil.StateMachineRoundAction, 3)
		re := tmeil.StateMachineRoundEntrance{
			H:        1,
			R:        0,
			PubKey:   mfx.Fx.Vals()[0].PubKey,
			Actions:  actionCh,
			Response: make(chan tmeil.RoundEntranceResponse),
		}
		gtest.SendSoon(t, mfx.StateMachineRoundEntranceIn, re)

		_ = gtest.ReceiveSoon(t, re.Response)

		// Proposed header from different validator.
		ph := mfx.Fx.NextProposedHeader([]byte("app_data_1"), 1)
		mfx.Fx.SignProposal(ctx, &ph, 1)
		require.Equal(t, tmconsensus.HandleProposedHeaderAccepted, m.HandleProposedHeader(ctx, ph))

		smv := gtest.ReceiveSoon(t, mfx.StateMachineRoundViewOut).VRV
		require.Equal(t, []tmconsensus.ProposedHeader{ph}, smv.ProposedHeaders)

		// Then the state machine submits its valid prevote to the mirror.
		phHash := string(ph.Header.Hash)
		vt := tmconsensus.VoteTarget{
			Height: 1, Round: 0,
			BlockHash: phHash,
		}
		signContent, err := tmconsensus.PrevoteSignBytes(vt, mfx.Fx.SignatureScheme)
		require.NoError(t, err)
		// Buffered channel so we can just send without select.
		actionCh <- tmeil.StateMachineRoundAction{
			Prevote: tmeil.ScopedSignature{
				TargetHash:  phHash,
				SignContent: signContent,
				Sig:         mfx.Fx.PrevoteSignature(ctx, vt, 0),
			},
		}

		// The mirror repeats the vote back to the state machine.
		smv = gtest.ReceiveSoon(t, mfx.StateMachineRoundViewOut).VRV
		var bs bitset.BitSet
		smv.PrevoteProofs[phHash].SignatureBitSet(&bs)
		require.Equal(t, uint(1), bs.Count())

		// Now the mirror receives the remainder of the prevotes over the network.
		voteMap := map[string][]int{
			phHash: {1, 2, 3},
		}
		sparsePrevoteProofMap := mfx.Fx.SparsePrevoteProofMap(ctx, 1, 0, voteMap)
		keyHash, _ := mfx.Fx.ValidatorHashes()
		prevoteProof := tmconsensus.PrevoteSparseProof{
			Height:     1,
			Round:      0,
			PubKeyHash: keyHash,
			Proofs:     sparsePrevoteProofMap,
		}
		require.Equal(t, tmconsensus.HandleVoteProofsAccepted, m.HandlePrevoteProofs(ctx, prevoteProof))

		// The state machine sees the 100% prevote update.
		smv = gtest.ReceiveSoon(t, mfx.StateMachineRoundViewOut).VRV
		smv.PrevoteProofs[phHash].SignatureBitSet(&bs)
		require.Equal(t, uint(4), bs.Count())

		// Now the mirror receives everyone else's precommit over the network.
		sparsePrecommitProofMap := mfx.Fx.SparsePrecommitProofMap(ctx, 1, 0, voteMap)
		precommitProof := tmconsensus.PrecommitSparseProof{
			Height:     1,
			Round:      0,
			PubKeyHash: keyHash,
			Proofs:     sparsePrecommitProofMap,
		}
		require.Equal(t, tmconsensus.HandleVoteProofsAccepted, m.HandlePrecommitProofs(ctx, precommitProof))

		// The state machine sees the committing update.
		smv = gtest.ReceiveSoon(t, mfx.StateMachineRoundViewOut).VRV
		smv.PrecommitProofs[phHash].SignatureBitSet(&bs)
		require.Equal(t, uint(3), bs.Count())

		// And as a sanity check, the gossip strategy output indicates that 1/0 is now in committing.
		gso := gtest.ReceiveSoon(t, mfx.GossipStrategyOut)
		require.NotNil(t, gso.Committing)
		require.Equal(t, uint64(1), gso.Committing.Height)
		require.Zero(t, gso.Committing.Round)
		gso.Committing.PrecommitProofs[phHash].SignatureBitSet(&bs)
		require.Equal(t, uint(3), bs.Count())
		require.NotNil(t, gso.Voting)
		require.Equal(t, uint64(2), gso.Voting.Height)
		require.Zero(t, gso.Voting.Round)

		// Now the state machine submits its precommit, slightly lagging the mirror.
		signContent, err = tmconsensus.PrecommitSignBytes(vt, mfx.Fx.SignatureScheme)
		require.NoError(t, err)
		// Buffered channel so we can just send without select.
		actionCh <- tmeil.StateMachineRoundAction{
			Precommit: tmeil.ScopedSignature{
				TargetHash:  phHash,
				SignContent: signContent,
				Sig:         mfx.Fx.PrecommitSignature(ctx, vt, 0),
			},
		}

		// Finally, the gossip strategy output indicates that precommit was accepted.
		gso = gtest.ReceiveSoon(t, mfx.GossipStrategyOut)
		require.NotNil(t, gso.Committing)
		require.Equal(t, uint64(1), gso.Committing.Height)
		require.Zero(t, gso.Committing.Round)
		gso.Committing.PrecommitProofs[phHash].SignatureBitSet(&bs)
		require.Equal(t, uint(4), bs.Count())
	})

	t.Run("state machine precommit handled when it arrives into orphaned round view", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		mfx := tmmirrortest.NewFixture(ctx, t, 4)

		m := mfx.NewMirror()
		defer m.Wait()
		defer cancel()

		actionCh := make(chan tmeil.StateMachineRoundAction, 3)
		re := tmeil.StateMachineRoundEntrance{
			H:        1,
			R:        0,
			PubKey:   mfx.Fx.Vals()[0].PubKey,
			Actions:  actionCh,
			Response: make(chan tmeil.RoundEntranceResponse),
		}
		gtest.SendSoon(t, mfx.StateMachineRoundEntranceIn, re)

		_ = gtest.ReceiveSoon(t, re.Response)

		// No proposed header here.
		// The state machine has a simulated timeout and prevotes nil.
		vt := tmconsensus.VoteTarget{
			Height: 1, Round: 0,
		}
		signContent, err := tmconsensus.PrevoteSignBytes(vt, mfx.Fx.SignatureScheme)
		require.NoError(t, err)
		// Buffered channel so we can just send without select.
		actionCh <- tmeil.StateMachineRoundAction{
			Prevote: tmeil.ScopedSignature{
				TargetHash:  "",
				SignContent: signContent,
				Sig:         mfx.Fx.PrevoteSignature(ctx, vt, 0),
			},
		}

		// The mirror repeats the vote back to the state machine.
		smv := gtest.ReceiveSoon(t, mfx.StateMachineRoundViewOut).VRV
		var bs bitset.BitSet
		smv.PrevoteProofs[""].SignatureBitSet(&bs)
		require.Equal(t, uint(1), bs.Count())

		// Now the mirror receives the remainder of the prevotes over the network.
		voteMap := map[string][]int{
			"": {1, 2, 3},
		}
		sparsePrevoteProofMap := mfx.Fx.SparsePrevoteProofMap(ctx, 1, 0, voteMap)
		keyHash, _ := mfx.Fx.ValidatorHashes()
		prevoteProof := tmconsensus.PrevoteSparseProof{
			Height:     1,
			Round:      0,
			PubKeyHash: keyHash,
			Proofs:     sparsePrevoteProofMap,
		}
		require.Equal(t, tmconsensus.HandleVoteProofsAccepted, m.HandlePrevoteProofs(ctx, prevoteProof))

		// The state machine sees the 100% prevote update.
		smv = gtest.ReceiveSoon(t, mfx.StateMachineRoundViewOut).VRV
		smv.PrevoteProofs[""].SignatureBitSet(&bs)
		require.Equal(t, uint(4), bs.Count())

		// Now the mirror receives everyone else's precommit over the network.
		sparsePrecommitProofMap := mfx.Fx.SparsePrecommitProofMap(ctx, 1, 0, voteMap)
		precommitProof := tmconsensus.PrecommitSparseProof{
			Height:     1,
			Round:      0,
			PubKeyHash: keyHash,
			Proofs:     sparsePrecommitProofMap,
		}
		require.Equal(t, tmconsensus.HandleVoteProofsAccepted, m.HandlePrecommitProofs(ctx, precommitProof))

		// And as a sanity check, the gossip strategy output indicates that 1/1 is now in voting.
		gso := gtest.ReceiveSoon(t, mfx.GossipStrategyOut)
		require.NotNil(t, gso.Voting)
		require.Equal(t, uint64(1), gso.Voting.Height)
		require.Equal(t, uint32(1), gso.Voting.Round)
		require.Empty(t, gso.Voting.PrecommitProofs)

		// The state machine should be receiving the update to 1/0 to indicate that it was a nil commit.
		// But before it receives that update, the state machine sends its precommit,
		// which we now know does not match a maintained view in the mirror.

		signContent, err = tmconsensus.PrecommitSignBytes(vt, mfx.Fx.SignatureScheme)
		require.NoError(t, err)
		// Buffered channel so we can just send without select.
		actionCh <- tmeil.StateMachineRoundAction{
			Precommit: tmeil.ScopedSignature{
				TargetHash:  "",
				SignContent: signContent,
				Sig:         mfx.Fx.PrecommitSignature(ctx, vt, 0),
			},
		}

		// This is a little bit of a race condition,
		// in that we do not have a synchronization point on sending the precommit action.
		// But regardless of whether the mirror received the precommit action first,
		// the state machine view currently does not get an update to include
		// the state machine's own action, as that view was orphaned.
		// If that changes in the future, then we will need to either add a synchronizatoin point
		// or handle the first read being either a 3 or a 4.
		smv = gtest.ReceiveSoon(t, mfx.StateMachineRoundViewOut).VRV
		smv.PrecommitProofs[""].SignatureBitSet(&bs)
		require.Equal(t, uint(3), bs.Count())
	})
}

func TestMirror_StateMachineRoundViewOut(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mfx := tmmirrortest.NewFixture(ctx, t, 4)

	m := mfx.NewMirror()
	defer m.Wait()
	defer cancel()

	// Before the state machine starts, no value is sending.
	gtest.NotSending(t, mfx.StateMachineRoundViewOut)

	// Add a proposed header to the mirror before the state machine starts.
	ph11 := mfx.Fx.NextProposedHeader([]byte("app_data_1_1"), 1)
	mfx.Fx.SignProposal(ctx, &ph11, 1)
	require.Equal(t, tmconsensus.HandleProposedHeaderAccepted, m.HandleProposedHeader(ctx, ph11))

	// The state machine still hasn't started, so there is still nothing being sent.
	gtest.NotSending(t, mfx.StateMachineRoundViewOut)

	// Now the state machine starts.
	actionCh := make(chan tmeil.StateMachineRoundAction, 3)
	re := tmeil.StateMachineRoundEntrance{
		H: 1, R: 0,
		PubKey:   mfx.Fx.Vals()[0].PubKey,
		Actions:  actionCh,
		Response: make(chan tmeil.RoundEntranceResponse, 1),
	}
	gtest.SendSoon(t, mfx.StateMachineRoundEntranceIn, re)

	// The mirror responds with the initial state update.
	rer := gtest.ReceiveSoon(t, re.Response)
	require.Equal(t, []tmconsensus.ProposedHeader{ph11}, rer.VRV.ProposedHeaders)

	// And because the initial state update consumed the state machine view,
	// there is still nothing sent on the state machine view out channel.
	gtest.NotSending(t, mfx.StateMachineRoundViewOut)

	// But now if another proposed header arrives, it will reach the state machine view out.
	ph12 := mfx.Fx.NextProposedHeader([]byte("app_data_1_2"), 2)
	mfx.Fx.SignProposal(ctx, &ph12, 2)
	require.Equal(t, tmconsensus.HandleProposedHeaderAccepted, m.HandleProposedHeader(ctx, ph12))
	vrv := gtest.ReceiveSoon(t, mfx.StateMachineRoundViewOut).VRV
	_ = vrv
}

func TestMirror_stateMachineJumpAhead(t *testing.T) {
	t.Run("majority prevotes", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		mfx := tmmirrortest.NewFixture(ctx, t, 4)

		m := mfx.NewMirror()
		defer m.Wait()
		defer cancel()

		// The state machine starts right away.
		actionCh := make(chan tmeil.StateMachineRoundAction, 3)
		re := tmeil.StateMachineRoundEntrance{
			H: 1, R: 0,
			Actions:  actionCh,
			Response: make(chan tmeil.RoundEntranceResponse, 1),
		}
		gtest.SendSoon(t, mfx.StateMachineRoundEntranceIn, re)

		// Enqueue a proposed header for the state machine's round, but don't read it from the state machine yet.
		ph10 := mfx.Fx.NextProposedHeader([]byte("ignored"), 0)
		mfx.Fx.SignProposal(ctx, &ph10, 0)
		require.Equal(t, tmconsensus.HandleProposedHeaderAccepted, m.HandleProposedHeader(ctx, ph10))

		// Now the mirror sees a majority nil prevote at round 1.
		keyHash, _ := mfx.Fx.ValidatorHashes()
		ph11VoteMap := map[string][]int{
			"": {1, 2, 3},
		}
		sparsePrevoteProofMap := mfx.Fx.SparsePrevoteProofMap(ctx, 1, 1, ph11VoteMap)
		prevoteProof := tmconsensus.PrevoteSparseProof{
			Height:     1,
			Round:      1,
			PubKeyHash: keyHash,
			Proofs:     sparsePrevoteProofMap,
		}
		require.Equal(t, tmconsensus.HandleVoteProofsAccepted, m.HandlePrevoteProofs(ctx, prevoteProof))

		// Now the state machine reads its input from the mirror.
		smv := gtest.ReceiveSoon(t, mfx.StateMachineRoundViewOut)
		// It has the proposed header for 1/0 in the main VRV.
		require.Equal(t, uint64(1), smv.VRV.Height)
		require.Zero(t, smv.VRV.Round)
		require.Equal(t, []tmconsensus.ProposedHeader{ph10}, smv.VRV.ProposedHeaders)

		// And it has the jumpahead set.
		j := smv.JumpAheadRoundView
		require.NotNil(t, j)
		require.Equal(t, uint64(1), j.Height)
		require.Equal(t, uint32(1), j.Round)

		// Now the mirror expects the state machine to enter 1/1.
		// If we get more information before then, such as a precommit, nothing new is sent to the state machine.
		ph11VoteMap = map[string][]int{
			"": {1},
		}
		sparsePrecommitProofMap := mfx.Fx.SparsePrecommitProofMap(ctx, 1, 1, ph11VoteMap)
		precommitProof := tmconsensus.PrecommitSparseProof{
			Height:     1,
			Round:      1,
			PubKeyHash: keyHash,
			Proofs:     sparsePrecommitProofMap,
		}
		require.Equal(t, tmconsensus.HandleVoteProofsAccepted, m.HandlePrecommitProofs(ctx, precommitProof))
		gtest.NotSending(t, mfx.StateMachineRoundViewOut)

		// Then if the state machine enters the round...
		actionCh = make(chan tmeil.StateMachineRoundAction, 3)
		re = tmeil.StateMachineRoundEntrance{
			H: 1, R: 1,
			Actions:  actionCh,
			Response: make(chan tmeil.RoundEntranceResponse, 1),
		}
		gtest.SendSoon(t, mfx.StateMachineRoundEntranceIn, re)

		// The entrance response has the 3 prevotes and 1 precommit for 1/1.
		vrv := gtest.ReceiveSoon(t, re.Response).VRV
		require.Equal(t, uint64(1), vrv.Height)
		require.Equal(t, uint32(1), vrv.Round)
		var bs bitset.BitSet
		vrv.PrevoteProofs[""].SignatureBitSet(&bs)
		require.Equal(t, uint(3), bs.Count())
		vrv.PrecommitProofs[""].SignatureBitSet(&bs)
		require.Equal(t, uint(1), bs.Count())
	})

	t.Run("minority precommits", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		mfx := tmmirrortest.NewFixture(ctx, t, 4)

		m := mfx.NewMirror()
		defer m.Wait()
		defer cancel()

		// The state machine starts right away.
		actionCh := make(chan tmeil.StateMachineRoundAction, 3)
		re := tmeil.StateMachineRoundEntrance{
			H: 1, R: 0,
			Actions:  actionCh,
			Response: make(chan tmeil.RoundEntranceResponse, 1),
		}
		gtest.SendSoon(t, mfx.StateMachineRoundEntranceIn, re)

		// Enqueue a proposed header for the state machine's round, but don't read it from the state machine yet.
		ph10 := mfx.Fx.NextProposedHeader([]byte("ignored"), 0)
		mfx.Fx.SignProposal(ctx, &ph10, 0)
		require.Equal(t, tmconsensus.HandleProposedHeaderAccepted, m.HandleProposedHeader(ctx, ph10))

		// Now the mirror sees a minority nil precommit at round 1.
		keyHash, _ := mfx.Fx.ValidatorHashes()
		ph11VoteMap := map[string][]int{
			"": {2, 3},
		}
		sparsePrecommitProofMap := mfx.Fx.SparsePrecommitProofMap(ctx, 1, 1, ph11VoteMap)
		precommitProof := tmconsensus.PrecommitSparseProof{
			Height:     1,
			Round:      1,
			PubKeyHash: keyHash,
			Proofs:     sparsePrecommitProofMap,
		}
		require.Equal(t, tmconsensus.HandleVoteProofsAccepted, m.HandlePrecommitProofs(ctx, precommitProof))

		// Receiving the 1/0 update includes the jump ahead details.
		smv := gtest.ReceiveSoon(t, mfx.StateMachineRoundViewOut)
		require.Equal(t, []tmconsensus.ProposedHeader{ph10}, smv.VRV.ProposedHeaders)

		j := smv.JumpAheadRoundView
		require.NotNil(t, j)
		require.Equal(t, uint64(1), j.Height)
		require.Equal(t, uint32(1), j.Round)
		require.Empty(t, j.PrevoteProofs[""])
		var bs bitset.BitSet
		j.PrecommitProofs[""].SignatureBitSet(&bs)
		require.Equal(t, uint(2), bs.Count())
	})

	t.Run("when there is no current VRV to include", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		mfx := tmmirrortest.NewFixture(ctx, t, 4)

		m := mfx.NewMirror()
		defer m.Wait()
		defer cancel()

		// State machine enters round 1/0 immediately.
		actionCh := make(chan tmeil.StateMachineRoundAction, 3)
		re := tmeil.StateMachineRoundEntrance{
			H: 1, R: 0,
			Actions:  actionCh,
			Response: make(chan tmeil.RoundEntranceResponse, 1),
		}
		gtest.SendSoon(t, mfx.StateMachineRoundEntranceIn, re)

		// All the prevotes arrive and half the precommits arrive.
		voteMap := map[string][]int{
			"": {0, 1, 2, 3},
		}
		keyHash, _ := mfx.Fx.ValidatorHashes()
		prevoteProof := tmconsensus.PrevoteSparseProof{
			Height:     1,
			Round:      0,
			PubKeyHash: keyHash,
			Proofs:     mfx.Fx.SparsePrevoteProofMap(ctx, 1, 0, voteMap),
		}
		require.Equal(t, tmconsensus.HandleVoteProofsAccepted, m.HandlePrevoteProofs(ctx, prevoteProof))

		// Now, the other validators' precommits arrive on the network.
		voteMap[""] = []int{0, 1}
		precommitProof := tmconsensus.PrecommitSparseProof{
			Height:     1,
			Round:      0,
			PubKeyHash: keyHash,
			Proofs:     mfx.Fx.SparsePrecommitProofMap(ctx, 1, 0, voteMap),
		}
		require.Equal(t, tmconsensus.HandleVoteProofsAccepted, m.HandlePrecommitProofs(ctx, precommitProof))

		// The state machine receives the current state,
		// so that there is no VRV when we have a jump ahead signal.
		_ = gtest.ReceiveSoon(t, mfx.StateMachineRoundViewOut)

		// Now half nil precommits arrive for 1/1.
		precommitProof = tmconsensus.PrecommitSparseProof{
			Height:     1,
			Round:      1,
			PubKeyHash: keyHash,
			Proofs:     mfx.Fx.SparsePrecommitProofMap(ctx, 1, 1, voteMap),
		}
		require.Equal(t, tmconsensus.HandleVoteProofsAccepted, m.HandlePrecommitProofs(ctx, precommitProof))

		// The state machine receives another update.
		// This time the VRV is empty but the JumpAheadRoundView is set.
		smv := gtest.ReceiveSoon(t, mfx.StateMachineRoundViewOut)
		require.Equal(t, tmconsensus.VersionedRoundView{}, smv.VRV)
		require.NotNil(t, smv.JumpAheadRoundView)
	})
}

func TestMirror_VoteSummaryReset(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mfx := tmmirrortest.NewFixture(ctx, t, 4)

	m := mfx.NewMirror()
	defer m.Wait()
	defer cancel()

	// Proposed header first.
	ph10 := mfx.Fx.NextProposedHeader([]byte("app_data_1_0"), 0)
	mfx.Fx.SignProposal(ctx, &ph10, 0)
	require.Equal(t, tmconsensus.HandleProposedHeaderAccepted, m.HandleProposedHeader(ctx, ph10))

	keyHash, _ := mfx.Fx.ValidatorHashes()

	// Then 3/4 prevote for the block, 1/4 for nil.
	ph10Hash := string(ph10.Header.Hash)
	ph10VoteMap := map[string][]int{
		ph10Hash: {0, 1, 2},
		"":       {3},
	}
	sparsePrevoteProofMap := mfx.Fx.SparsePrevoteProofMap(ctx, 1, 0, ph10VoteMap)
	prevoteProof := tmconsensus.PrevoteSparseProof{
		Height:     1,
		Round:      0,
		PubKeyHash: keyHash,
		Proofs:     sparsePrevoteProofMap,
	}
	require.Equal(t, tmconsensus.HandleVoteProofsAccepted, m.HandlePrevoteProofs(ctx, prevoteProof))

	// Now the state machine comes online.
	actionCh := make(chan tmeil.StateMachineRoundAction, 3)
	as10 := tmeil.StateMachineRoundEntrance{
		H: 1, R: 0,
		PubKey:   nil,
		Actions:  actionCh,
		Response: make(chan tmeil.RoundEntranceResponse, 1),
	}
	gtest.SendSoon(t, mfx.StateMachineRoundEntranceIn, as10)

	rer := gtest.ReceiveSoon(t, as10.Response)
	require.Equal(t, []tmconsensus.ProposedHeader{ph10}, rer.VRV.ProposedHeaders)

	// Next, the precommit is split 50-50.
	ph10VoteMap = map[string][]int{
		ph10Hash: {0, 1},
		"":       {2, 3},
	}
	sparsePrecommitProofMap := mfx.Fx.SparsePrecommitProofMap(ctx, 1, 0, ph10VoteMap)
	precommitProof := tmconsensus.PrecommitSparseProof{
		Height:     1,
		Round:      0,
		PubKeyHash: keyHash,
		Proofs:     sparsePrecommitProofMap,
	}
	require.Equal(t, tmconsensus.HandleVoteProofsAccepted, m.HandlePrecommitProofs(ctx, precommitProof))

	// That update is sent to the state machine.
	vrv10 := gtest.ReceiveSoon(t, mfx.StateMachineRoundViewOut).VRV
	require.Len(t, vrv10.PrecommitProofs, 2)

	// And the state machine enters 1/1.
	as11 := tmeil.StateMachineRoundEntrance{
		H: 1, R: 1,
		PubKey:   nil,
		Actions:  actionCh,
		Response: make(chan tmeil.RoundEntranceResponse, 1),
	}
	gtest.SendSoon(t, mfx.StateMachineRoundEntranceIn, as11)

	rer = gtest.ReceiveSoon(t, as11.Response)
	require.Zero(t, rer.VRV.VoteSummary.TotalPrevotePower)
	require.Zero(t, rer.VRV.VoteSummary.TotalPrecommitPower)

	gsVRV := gtest.ReceiveSoon(t, mfx.GossipStrategyOut).Voting
	require.Equal(t, uint32(1), gsVRV.Round)
	require.Zero(t, gsVRV.VoteSummary.TotalPrevotePower)
	require.Zero(t, gsVRV.VoteSummary.TotalPrecommitPower)

	// Now there is a new proposed header at 1/1.
	ph11 := mfx.Fx.NextProposedHeader([]byte("app_data_1_1"), 0)
	ph11.Round = 1
	ph11Hash := string(ph11.Header.Hash)
	mfx.Fx.SignProposal(ctx, &ph11, 0)
	require.Equal(t, tmconsensus.HandleProposedHeaderAccepted, m.HandleProposedHeader(ctx, ph11))

	// Now the mirror just receives another split precommit.
	ph11VoteMap := map[string][]int{
		ph11Hash: {0, 1},
		"":       {2, 3},
	}
	sparsePrecommitProofMap = mfx.Fx.SparsePrecommitProofMap(ctx, 1, 1, ph11VoteMap)
	precommitProof = tmconsensus.PrecommitSparseProof{
		Height:     1,
		Round:      1,
		PubKeyHash: keyHash,
		Proofs:     sparsePrecommitProofMap,
	}
	require.Equal(t, tmconsensus.HandleVoteProofsAccepted, m.HandlePrecommitProofs(ctx, precommitProof))

	vrv11 := gtest.ReceiveSoon(t, mfx.StateMachineRoundViewOut).VRV
	require.Len(t, vrv11.PrecommitProofs, 2)

	// Now the state machine enters 1/2.
	// This is key, because the mirror should now be reusing the VRV it originally had at 1/0.
	as12 := tmeil.StateMachineRoundEntrance{
		H: 1, R: 2,
		PubKey:   nil,
		Actions:  actionCh,
		Response: make(chan tmeil.RoundEntranceResponse, 1),
	}
	gtest.SendSoon(t, mfx.StateMachineRoundEntranceIn, as12)

	rer = gtest.ReceiveSoon(t, as12.Response)
	require.Zero(t, rer.VRV.VoteSummary.TotalPrevotePower)
	require.Zero(t, rer.VRV.VoteSummary.TotalPrecommitPower)

	gsVRV = gtest.ReceiveSoon(t, mfx.GossipStrategyOut).Voting
	require.Equal(t, uint32(2), gsVRV.Round)
	require.Zero(t, gsVRV.VoteSummary.TotalPrevotePower)
	require.Zero(t, gsVRV.VoteSummary.TotalPrecommitPower)
}

func TestMirror_nilCommitSentToGossipStrategy(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mfx := tmmirrortest.NewFixture(ctx, t, 5)

	m := mfx.NewMirror()
	defer m.Wait()
	defer cancel()

	// Now the state machine starts.
	actionCh := make(chan tmeil.StateMachineRoundAction, 3)
	re := tmeil.StateMachineRoundEntrance{
		H: 1, R: 0,
		PubKey:   mfx.Fx.Vals()[0].PubKey,
		Actions:  actionCh,
		Response: make(chan tmeil.RoundEntranceResponse, 1),
	}
	gtest.SendSoon(t, mfx.StateMachineRoundEntranceIn, re)

	// The mirror responds with the initial state update;
	// nothing of interest yet.
	_ = gtest.ReceiveSoon(t, re.Response)

	// The state machine submits its own prevote for nil.
	vt := tmconsensus.VoteTarget{
		Height: 1, Round: 0,
	}
	signContent, err := tmconsensus.PrevoteSignBytes(vt, mfx.Fx.SignatureScheme)
	require.NoError(t, err)
	// Buffered channel so we can just send without select.
	actionCh <- tmeil.StateMachineRoundAction{
		Prevote: tmeil.ScopedSignature{
			TargetHash:  "",
			SignContent: signContent,
			Sig:         mfx.Fx.PrevoteSignature(ctx, vt, 0),
		},
	}

	// The mirror repeats the vote back to the state machine.
	smv := gtest.ReceiveSoon(t, mfx.StateMachineRoundViewOut).VRV
	var bs bitset.BitSet
	smv.PrevoteProofs[""].SignatureBitSet(&bs)
	require.Equal(t, uint(1), bs.Count())

	// Now the mirror receives the remainder of the prevotes over the network.
	voteMap := map[string][]int{
		"": {1, 2, 3}, // This is only 4/5 validators including the state machine, but that's enough.
	}
	keyHash, _ := mfx.Fx.ValidatorHashes()
	prevoteProof := tmconsensus.PrevoteSparseProof{
		Height:     1,
		Round:      0,
		PubKeyHash: keyHash,
		Proofs:     mfx.Fx.SparsePrevoteProofMap(ctx, 1, 0, voteMap),
	}
	require.Equal(t, tmconsensus.HandleVoteProofsAccepted, m.HandlePrevoteProofs(ctx, prevoteProof))

	// Now, the other validators' precommits arrive on the network.
	precommitProof := tmconsensus.PrecommitSparseProof{
		Height:     1,
		Round:      0,
		PubKeyHash: keyHash,
		Proofs:     mfx.Fx.SparsePrecommitProofMap(ctx, 1, 0, voteMap),
	}
	require.Equal(t, tmconsensus.HandleVoteProofsAccepted, m.HandlePrecommitProofs(ctx, precommitProof))

	// This is only 60% of the votes, so the mirror still reports voting on 1/0.
	gso := gtest.ReceiveSoon(t, mfx.GossipStrategyOut)
	require.NotNil(t, gso.Voting)
	gso.Voting.PrecommitProofs[""].SignatureBitSet(&bs)
	require.Equal(t, uint(3), bs.Count())

	// Same thing reported to the state machine.
	smv = gtest.ReceiveSoon(t, mfx.StateMachineRoundViewOut).VRV
	smv.PrecommitProofs[""].SignatureBitSet(&bs)
	require.Equal(t, uint(3), bs.Count())

	// Now, the state machine reports its nil precommit,
	// which is sufficient to push the round past the precommit threshold.
	signContent, err = tmconsensus.PrecommitSignBytes(vt, mfx.Fx.SignatureScheme)
	require.NoError(t, err)
	// Buffered channel so we can just send without select.
	actionCh <- tmeil.StateMachineRoundAction{
		Precommit: tmeil.ScopedSignature{
			TargetHash:  "",
			SignContent: signContent,
			Sig:         mfx.Fx.PrecommitSignature(ctx, vt, 0),
		},
	}

	// This causes another update to the gossip strategy.
	// The voting round is clean on round 1.
	gso = gtest.ReceiveSoon(t, mfx.GossipStrategyOut)
	require.NotNil(t, gso.Voting)
	require.Equal(t, uint64(1), gso.Voting.Height)
	require.Equal(t, uint32(1), gso.Voting.Round)

	// And the NilVotedRound field contains the 4/5 known precommits.
	require.NotNil(t, gso.NilVotedRound)
	gso.NilVotedRound.PrecommitProofs[""].SignatureBitSet(&bs)
	require.Equal(t, uint(4), bs.Count())

	// Finally, new information leading to another GossipStrategyOut update
	// will cause the NilVotedRound field to not be set.
	ph11 := mfx.Fx.NextProposedHeader([]byte("app_data_1"), 1)
	ph11.Round = 1
	mfx.Fx.SignProposal(ctx, &ph11, 1)
	require.Equal(t, tmconsensus.HandleProposedHeaderAccepted, m.HandleProposedHeader(ctx, ph11))

	gso = gtest.ReceiveSoon(t, mfx.GossipStrategyOut)
	require.NotNil(t, gso.Voting)
	require.Equal(t, uint64(1), gso.Voting.Height)
	require.Equal(t, uint32(1), gso.Voting.Round)
	require.Equal(t, []tmconsensus.ProposedHeader{ph11}, gso.Voting.ProposedHeaders)

	require.Nil(t, gso.NilVotedRound)
}

func TestMirror_replayedHeaders(t *testing.T) {
	t.Run("at initialization, without any p2p messages", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		mfx := tmmirrortest.NewFixture(ctx, t, 4)

		m := mfx.NewMirror()
		defer m.Wait()
		defer cancel()

		require.Equal(t, tmelink.LagStatusInitializing, gtest.ReceiveSoon(
			t, mfx.LagStateOut,
		).Status)

		ph1 := mfx.Fx.NextProposedHeader([]byte("app_data_1"), 0)

		// Everyone voted for the block.
		voteMap := map[string][]int{
			string(ph1.Header.Hash): {0, 1, 2, 3},
		}
		precommitProofs1 := mfx.Fx.PrecommitProofMap(ctx, 1, 0, voteMap)
		mfx.Fx.CommitBlock(ph1.Header, []byte("app_state_height_1"), 0, precommitProofs1)

		ph2 := mfx.Fx.NextProposedHeader([]byte("app_data_2"), 0)

		// Before we replay the block, make sure the header store is empty.
		_, err := mfx.Cfg.CommittedHeaderStore.LoadCommittedHeader(ctx, 1)
		require.ErrorIs(t, err, tmconsensus.HeightUnknownError{Want: 1})

		respCh := make(chan tmelink.ReplayedHeaderResponse, 1)
		req1 := tmelink.ReplayedHeaderRequest{
			Header: ph1.Header,
			Proof:  ph2.Header.PrevCommitProof,
			Resp:   respCh,
		}

		gtest.SendSoon(t, mfx.ReplayedHeadersIn, req1)
		_ = gtest.ReceiveSoon(t, respCh) // Response currently doesn't have anything meaningful.

		// Now we have a committing header.
		h, err := mfx.Cfg.CommittedHeaderStore.LoadCommittedHeader(ctx, 1)
		require.NoError(t, err)
		require.Equal(t, tmconsensus.CommittedHeader{
			Header: ph1.Header,
			Proof:  req1.Proof,
		}, h)

		// But the fresh data must be in the round store.
		rs := mfx.Cfg.RoundStore
		phs, prevotes, precommits, err := rs.LoadRoundState(ctx, 1, 0)
		require.NoError(t, err)
		require.Equal(t, []tmconsensus.ProposedHeader{
			{
				// We don't store a full header in the round store when replaying blocks,
				// because we don't expect to have proposer details.
				Header: ph1.Header,
			},
		}, phs)
		require.Empty(t, prevotes.BlockSignatures)
		fullPrecommits, err := precommits.ToFullPrecommitProofMap(
			1, 0,
			mfx.Fx.ValSet(),
			mfx.Fx.SignatureScheme, mfx.Fx.CommonMessageSignatureProofScheme,
		)
		require.NoError(t, err)
		require.Len(t, fullPrecommits, 1)
		var bs bitset.BitSet
		fullPrecommits[string(ph1.Header.Hash)].SignatureBitSet(&bs)
		require.Equal(t, uint(4), bs.Count())

		// So, let's replay the next header too.
		voteMap = map[string][]int{
			string(ph2.Header.Hash): {0, 1, 2, 3},
		}
		precommitProofs2 := mfx.Fx.PrecommitProofMap(ctx, 2, 0, voteMap)
		mfx.Fx.CommitBlock(ph2.Header, []byte("app_state_height_2"), 0, precommitProofs2)

		ph3 := mfx.Fx.NextProposedHeader([]byte("app_data_3"), 0)
		respCh = make(chan tmelink.ReplayedHeaderResponse, 1)
		req2 := tmelink.ReplayedHeaderRequest{
			Header: ph2.Header,
			Proof:  ph3.Header.PrevCommitProof,
			Resp:   respCh,
		}

		gtest.SendSoon(t, mfx.ReplayedHeadersIn, req2)
		require.NoError(t, gtest.ReceiveSoon(t, respCh).Err)
		gtest.NotSending(t, respCh) // Temporary: ensuring channel is not closed.

		// Now 2 is in committing and 3 is in voting,
		// so 2 must be in the header store.
		h, err = mfx.Cfg.CommittedHeaderStore.LoadCommittedHeader(ctx, 2)
		require.NoError(t, err)
		require.Equal(t, tmconsensus.CommittedHeader{
			Header: req2.Header,
			Proof:  req2.Proof,
		}, h)
	})

	t.Run("hashes for other blocks included", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		mfx := tmmirrortest.NewFixture(ctx, t, 8)

		m := mfx.NewMirror()
		defer m.Wait()
		defer cancel()

		require.Equal(t, tmelink.LagStatusInitializing, gtest.ReceiveSoon(
			t, mfx.LagStateOut,
		).Status)

		ph1Good := mfx.Fx.NextProposedHeader([]byte("app_data_1"), 0)
		ph1Bad := mfx.Fx.NextProposedHeader([]byte("app_data_1_bad"), 7)

		// Almost everyone votes for the good block.
		voteMap := map[string][]int{
			string(ph1Good.Header.Hash): {0, 1, 2, 3, 4, 5},
			"":                          {6},
			string(ph1Bad.Header.Hash):  {7},
		}
		precommitProofs1 := mfx.Fx.PrecommitProofMap(ctx, 1, 0, voteMap)
		mfx.Fx.CommitBlock(ph1Good.Header, []byte("app_state_height_1"), 0, precommitProofs1)

		ph2 := mfx.Fx.NextProposedHeader([]byte("app_data_2"), 0)

		// Before we replay the block, make sure the header store is empty.
		_, err := mfx.Cfg.CommittedHeaderStore.LoadCommittedHeader(ctx, 1)
		require.ErrorIs(t, err, tmconsensus.HeightUnknownError{Want: 1})

		respCh := make(chan tmelink.ReplayedHeaderResponse, 1)
		req1 := tmelink.ReplayedHeaderRequest{
			Header: ph1Good.Header,
			Proof:  ph2.Header.PrevCommitProof,
			Resp:   respCh,
		}

		gtest.SendSoon(t, mfx.ReplayedHeadersIn, req1)
		require.NoError(t, gtest.ReceiveSoon(t, respCh).Err)

		// The round store has precommits for the good, bad, and nil blocks.
		rs := mfx.Cfg.RoundStore
		phs, prevotes, precommits, err := rs.LoadRoundState(ctx, 1, 0)
		require.NoError(t, err)
		require.Equal(t, []tmconsensus.ProposedHeader{
			{
				// We don't store a full header in the round store when replaying blocks,
				// because we don't expect to have proposer details.
				Header: ph1Good.Header,
			},
			// And we don't store the header for the proposed-but-not-committed block,
			// so phs only contains the one element.
		}, phs)
		require.Empty(t, prevotes.BlockSignatures)
		fullPrecommits, err := precommits.ToFullPrecommitProofMap(
			1, 0,
			mfx.Fx.ValSet(),
			mfx.Fx.SignatureScheme, mfx.Fx.CommonMessageSignatureProofScheme,
		)
		require.NoError(t, err)
		require.Len(t, fullPrecommits, 3)
		var bs bitset.BitSet
		fullPrecommits[""].SignatureBitSet(&bs)
		require.Equal(t, uint(1), bs.Count())
		fullPrecommits[string(ph1Bad.Header.Hash)].SignatureBitSet(&bs)
		require.Equal(t, uint(1), bs.Count())
		fullPrecommits[string(ph1Good.Header.Hash)].SignatureBitSet(&bs)
		require.Equal(t, uint(6), bs.Count())
	})

	t.Run("indicative error when replayed block has wrong height", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		mfx := tmmirrortest.NewFixture(ctx, t, 4)

		m := mfx.NewMirror()
		defer m.Wait()
		defer cancel()

		require.Equal(t, tmelink.LagStatusInitializing, gtest.ReceiveSoon(
			t, mfx.LagStateOut,
		).Status)

		ph1 := mfx.Fx.NextProposedHeader([]byte("app_data_1"), 0)

		// Everyone voted for the block.
		voteMap := map[string][]int{
			string(ph1.Header.Hash): {0, 1, 2, 3},
		}
		precommitProofs1 := mfx.Fx.PrecommitProofMap(ctx, 1, 0, voteMap)
		mfx.Fx.CommitBlock(ph1.Header, []byte("app_state_height_1"), 0, precommitProofs1)

		ph2 := mfx.Fx.NextProposedHeader([]byte("app_data_2"), 0)
		voteMap = map[string][]int{
			string(ph2.Header.Hash): {0, 1, 2, 3},
		}
		precommitProofs2 := mfx.Fx.PrecommitProofMap(ctx, 2, 0, voteMap)
		mfx.Fx.CommitBlock(ph2.Header, []byte("app_state_height_2"), 0, precommitProofs2)

		ph3 := mfx.Fx.NextProposedHeader([]byte("app_data_3"), 0)
		respCh := make(chan tmelink.ReplayedHeaderResponse, 1)
		req2 := tmelink.ReplayedHeaderRequest{
			Header: ph2.Header,
			Proof:  ph3.Header.PrevCommitProof,
			Resp:   respCh,
		}

		gtest.SendSoon(t, mfx.ReplayedHeadersIn, req2)
		err := gtest.ReceiveSoon(t, respCh).Err

		require.Equal(t, tmelink.ReplayedHeaderOutOfSyncError{
			WantHeight: 1,
			GotHeight:  2,
		}, err)
	})

	t.Run("indicative error when replayed block has wrong hash", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		mfx := tmmirrortest.NewFixture(ctx, t, 4)

		m := mfx.NewMirror()
		defer m.Wait()
		defer cancel()

		require.Equal(t, tmelink.LagStatusInitializing, gtest.ReceiveSoon(
			t, mfx.LagStateOut,
		).Status)

		ph1 := mfx.Fx.NextProposedHeader([]byte("app_data_1"), 0)
		ph1.Header.Hash[0]++ // Change the first byte of its hash to make it invalid.

		// Everyone voted for the block with the bad hash.
		// Shouldn't be possible to happen, but the mirror will bail out
		// before checking the signatures anyway since the hash is wrong.
		voteMap := map[string][]int{
			string(ph1.Header.Hash): {0, 1, 2, 3},
		}
		precommitProofs1 := mfx.Fx.PrecommitProofMap(ctx, 1, 0, voteMap)
		mfx.Fx.CommitBlock(ph1.Header, []byte("app_state_height_1"), 0, precommitProofs1)

		ph2 := mfx.Fx.NextProposedHeader([]byte("app_data_2"), 0)

		// Before we replay the block, make sure the header store is empty.
		_, err := mfx.Cfg.CommittedHeaderStore.LoadCommittedHeader(ctx, 1)
		require.ErrorIs(t, err, tmconsensus.HeightUnknownError{Want: 1})

		respCh := make(chan tmelink.ReplayedHeaderResponse, 1)
		gtest.SendSoon(t, mfx.ReplayedHeadersIn, tmelink.ReplayedHeaderRequest{
			Header: ph1.Header,
			Proof:  ph2.Header.PrevCommitProof,
			Resp:   respCh,
		})

		err = gtest.ReceiveSoon(t, respCh).Err
		require.Error(t, err)
		var vErr tmelink.ReplayedHeaderValidationError
		require.ErrorAs(t, err, &vErr)
		require.Contains(t, vErr.Error(), fmt.Sprintf("block hash (%x) different from expected", ph1.Header.Hash))
	})

	t.Run("indicative error when any signature fails verification", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		mfx := tmmirrortest.NewFixture(ctx, t, 4)

		m := mfx.NewMirror()
		defer m.Wait()
		defer cancel()

		require.Equal(t, tmelink.LagStatusInitializing, gtest.ReceiveSoon(
			t, mfx.LagStateOut,
		).Status)

		ph1 := mfx.Fx.NextProposedHeader([]byte("app_data_1"), 0)
		voteMap := map[string][]int{
			string(ph1.Header.Hash): {0, 1, 2, 3},
		}
		precommitProofs1 := mfx.Fx.PrecommitProofMap(ctx, 1, 0, voteMap)
		mfx.Fx.CommitBlock(ph1.Header, []byte("app_state_height_1"), 0, precommitProofs1)

		ph2 := mfx.Fx.NextProposedHeader([]byte("app_data_2"), 0)

		// Before we replay the block, make sure the header store is empty.
		_, err := mfx.Cfg.CommittedHeaderStore.LoadCommittedHeader(ctx, 1)
		require.ErrorIs(t, err, tmconsensus.HeightUnknownError{Want: 1})

		// Corrupt the previous commit proof by just modifying one byte in one signature.
		// This assumes we are using the tmconsensustest simple signature scheme,
		// which seems unlikely to change in tests.
		for _, v := range ph2.Header.PrevCommitProof.Proofs {
			v[0].Sig[0]++
			break
		}

		respCh := make(chan tmelink.ReplayedHeaderResponse, 1)
		gtest.SendSoon(t, mfx.ReplayedHeadersIn, tmelink.ReplayedHeaderRequest{
			Header: ph1.Header,
			Proof:  ph2.Header.PrevCommitProof,
			Resp:   respCh,
		})

		err = gtest.ReceiveSoon(t, respCh).Err
		require.Error(t, err)
		var vErr tmelink.ReplayedHeaderValidationError
		require.ErrorAs(t, err, &vErr)
		require.Contains(t, vErr.Error(), "invalid signature received")
	})

	t.Run("indicative error when signatures fail to meet power threshold", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		mfx := tmmirrortest.NewFixture(ctx, t, 4)

		m := mfx.NewMirror()
		defer m.Wait()
		defer cancel()

		require.Equal(t, tmelink.LagStatusInitializing, gtest.ReceiveSoon(
			t, mfx.LagStateOut,
		).Status)

		ph1 := mfx.Fx.NextProposedHeader([]byte("app_data_1"), 0)
		voteMap := map[string][]int{
			string(ph1.Header.Hash): {0, 1}, // Only two votes this time instead of all four.
		}
		precommitProofs1 := mfx.Fx.PrecommitProofMap(ctx, 1, 0, voteMap)

		// The fixture currently allows us to commit this block,
		// although maybe it shouldn't.
		mfx.Fx.CommitBlock(ph1.Header, []byte("app_state_height_1"), 0, precommitProofs1)

		ph2 := mfx.Fx.NextProposedHeader([]byte("app_data_2"), 0)

		respCh := make(chan tmelink.ReplayedHeaderResponse, 1)
		gtest.SendSoon(t, mfx.ReplayedHeadersIn, tmelink.ReplayedHeaderRequest{
			Header: ph1.Header,
			Proof:  ph2.Header.PrevCommitProof,
			Resp:   respCh,
		})

		err := gtest.ReceiveSoon(t, respCh).Err
		require.Error(t, err)
		var vErr tmelink.ReplayedHeaderValidationError
		require.ErrorAs(t, err, &vErr)
		require.Contains(t, vErr.Error(), "needed at least")
		require.Contains(t, vErr.Error(), "vote power for block with hash")
	})
}

func TestMirror_metrics(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mfx := tmmirrortest.NewFixture(ctx, t, 4)

	// Set up metrics collection manually, like the engine would do internally.
	// This way we can set a garbage state machine value,
	// allowing the metrics output to be emitted.
	mCh := make(chan tmemetrics.Metrics)
	mc := tmemetrics.NewCollector(ctx, 4, mCh)
	defer mc.Wait()
	defer cancel()
	mc.UpdateStateMachine(tmemetrics.StateMachineMetrics{
		H: 1, R: 0,
	})
	mfx.Cfg.MetricsCollector = mc

	m := mfx.NewMirror()
	defer m.Wait()
	defer cancel()

	// Proposed header first.
	ph10 := mfx.Fx.NextProposedHeader([]byte("app_data_1_0"), 0)
	mfx.Fx.SignProposal(ctx, &ph10, 0)
	require.Equal(t, tmconsensus.HandleProposedHeaderAccepted, m.HandleProposedHeader(ctx, ph10))

	keyHash, _ := mfx.Fx.ValidatorHashes()

	// Then 3/4 prevote for the block, 1/4 for nil.
	ph10Hash := string(ph10.Header.Hash)
	ph10VoteMap := map[string][]int{
		ph10Hash: {0, 1, 2},
		"":       {3},
	}
	prevoteProof := tmconsensus.PrevoteSparseProof{
		Height:     1,
		Round:      0,
		PubKeyHash: keyHash,
		Proofs:     mfx.Fx.SparsePrevoteProofMap(ctx, 1, 0, ph10VoteMap),
	}
	require.Equal(t, tmconsensus.HandleVoteProofsAccepted, m.HandlePrevoteProofs(ctx, prevoteProof))

	// Metrics should be at 1/0 still.
	ms := gtest.ReceiveSoon(t, mCh)
	require.Equal(t, uint64(1), ms.MirrorVotingHeight)
	require.Zero(t, ms.MirrorVotingRound)
	require.Zero(t, ms.MirrorCommittingHeight)
	require.Zero(t, ms.MirrorCommittingRound)

	// Full precommit for 1/0 now.
	precommitProof := tmconsensus.PrecommitSparseProof{
		Height:     1,
		Round:      0,
		PubKeyHash: keyHash,
		Proofs:     mfx.Fx.SparsePrecommitProofMap(ctx, 1, 0, ph10VoteMap),
	}
	require.Equal(t, tmconsensus.HandleVoteProofsAccepted, m.HandlePrecommitProofs(ctx, precommitProof))

	// That pushes voting to 2/0 and committing to 1/0.
	ms = gtest.ReceiveSoon(t, mCh)
	require.Equal(t, uint64(2), ms.MirrorVotingHeight)
	require.Zero(t, ms.MirrorVotingRound)
	require.Equal(t, uint64(1), ms.MirrorCommittingHeight)
	require.Zero(t, ms.MirrorCommittingRound)

	// Now if we get a nil precommit at 2/0, metrics show 2/1.
	ph20VoteMap := map[string][]int{
		"": {0, 1, 2, 3},
	}
	precommitProof = tmconsensus.PrecommitSparseProof{
		Height:     2,
		Round:      0,
		PubKeyHash: keyHash,
		Proofs:     mfx.Fx.SparsePrecommitProofMap(ctx, 2, 0, ph20VoteMap),
	}
	require.Equal(t, tmconsensus.HandleVoteProofsAccepted, m.HandlePrecommitProofs(ctx, precommitProof))

	ms = gtest.ReceiveSoon(t, mCh)
	require.Equal(t, uint64(2), ms.MirrorVotingHeight)
	require.Equal(t, uint32(1), ms.MirrorVotingRound)
	require.Equal(t, uint64(1), ms.MirrorCommittingHeight)
	require.Zero(t, ms.MirrorCommittingRound)
}

func TestMirror_stateMachineCatchup_lateInitialization(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// We are validator 0 in this set of 4.
	// So, if our state machine takes a long time to initialize,
	// and the rest of the network moves ahead a few blocks,
	// then the first messages we send to the state machine should be replayed headers.
	mfx := tmmirrortest.NewFixture(ctx, t, 4)

	m := mfx.NewMirror()
	defer m.Wait()
	defer cancel()

	keyHash, _ := mfx.Fx.ValidatorHashes()

	// Have validator 1 propose, and vals 1-3 precommit for, several blocks in a row.
	const receivedBlocks = 6
	var phs []tmconsensus.ProposedHeader
	for i := uint64(1); i < receivedBlocks; i++ {
		ph := mfx.Fx.NextProposedHeader([]byte(fmt.Sprintf("app_data_%d", i)), 1)
		mfx.Fx.SignProposal(ctx, &ph, 1)
		require.Equal(t, tmconsensus.HandleProposedHeaderAccepted, m.HandleProposedHeader(ctx, ph))

		// Build the precommit proof.
		phHash := string(ph.Header.Hash)
		voteMap := map[string][]int{
			phHash: {1, 2, 3},
		}
		precommitProof := tmconsensus.PrecommitSparseProof{
			Height:     i,
			Round:      0,
			PubKeyHash: keyHash,
			Proofs:     mfx.Fx.SparsePrecommitProofMap(ctx, i, 0, voteMap),
		}
		require.Equal(t, tmconsensus.HandleVoteProofsAccepted, m.HandlePrecommitProofs(ctx, precommitProof))

		precommitProofsMap := mfx.Fx.PrecommitProofMap(ctx, i, 0, voteMap)
		mfx.Fx.CommitBlock(ph.Header, []byte(fmt.Sprintf("app_state_height_%d", i)), 0, precommitProofsMap)
		phs = append(phs, ph)
	}

	// Now the state machine finishes initializing.
	// It gets a sequence of committed headers first.
	for i := uint64(1); i <= receivedBlocks-2; i++ {
		actionCh := make(chan tmeil.StateMachineRoundAction, 3)
		re := tmeil.StateMachineRoundEntrance{
			H:        i,
			R:        0,
			PubKey:   mfx.Fx.Vals()[0].PubKey,
			Actions:  actionCh,
			Response: make(chan tmeil.RoundEntranceResponse, 1),
		}
		gtest.SendSoon(t, mfx.StateMachineRoundEntranceIn, re)

		rer := gtest.ReceiveSoon(t, re.Response)
		require.True(t, rer.IsCH()) // It's a committed header, not a round update.
	}

	// Now at the committing height, it gets a VRV,
	// because we could receive further commit information for the block.
	actionCh := make(chan tmeil.StateMachineRoundAction, 3)
	re := tmeil.StateMachineRoundEntrance{
		H:        receivedBlocks - 1,
		R:        0,
		PubKey:   mfx.Fx.Vals()[0].PubKey,
		Actions:  actionCh,
		Response: make(chan tmeil.RoundEntranceResponse, 1),
	}
	gtest.SendSoon(t, mfx.StateMachineRoundEntranceIn, re)

	rer := gtest.ReceiveSoon(t, re.Response)
	require.False(t, rer.IsCH())
	require.True(t, rer.IsVRV())

	committingHash := string(phs[receivedBlocks-2].Header.Hash) // -2 due to off by one on indexing.
	var bs bitset.BitSet
	rer.VRV.RoundView.PrecommitProofs[committingHash].SignatureBitSet(&bs)
	require.Equal(t, uint(3), bs.Count())

	// The state machine would probably submit its own vote at this point, but let's say it just goes ahead to the next round.
	actionCh = make(chan tmeil.StateMachineRoundAction, 3)
	re = tmeil.StateMachineRoundEntrance{
		H:        receivedBlocks,
		R:        0,
		PubKey:   mfx.Fx.Vals()[0].PubKey,
		Actions:  actionCh,
		Response: make(chan tmeil.RoundEntranceResponse, 1),
	}
	gtest.SendSoon(t, mfx.StateMachineRoundEntranceIn, re)

	// The round entrance is accepted, and the response is still a VRV, not a committed header.
	rer = gtest.ReceiveSoon(t, re.Response)
	require.False(t, rer.IsCH())
	require.True(t, rer.IsVRV())
}
