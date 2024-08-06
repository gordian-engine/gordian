package tmmirror_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/rollchains/gordian/gcrypto"
	"github.com/rollchains/gordian/internal/gtest"
	"github.com/rollchains/gordian/tm/tmconsensus"
	"github.com/rollchains/gordian/tm/tmconsensus/tmconsensustest"
	"github.com/rollchains/gordian/tm/tmengine/internal/tmeil"
	"github.com/rollchains/gordian/tm/tmengine/internal/tmemetrics"
	"github.com/rollchains/gordian/tm/tmengine/internal/tmmirror"
	"github.com/rollchains/gordian/tm/tmengine/internal/tmmirror/internal/tmi"
	"github.com/rollchains/gordian/tm/tmengine/internal/tmmirror/tmmirrortest"
	"github.com/rollchains/gordian/tm/tmengine/tmelink/tmelinktest"
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

		keyHash, err := mfx.Fx.HashScheme.PubKeys(
			tmconsensus.ValidatorsToPubKeys(mfx.Cfg.InitialValidators),
		)
		require.NoError(t, err)

		powHash, err := mfx.Fx.HashScheme.VotePowers(
			tmconsensus.ValidatorsToVotePowers(mfx.Cfg.InitialValidators),
		)
		require.NoError(t, err)

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
		require.True(t, tmconsensus.ValidatorSlicesEqual(mfx.Cfg.InitialValidators, gotVals))
	})

	t.Run("starts correctly if the validator hashes are already in the store", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		mfx := tmmirrortest.NewFixture(ctx, t, 2)

		vs := mfx.ValidatorStore()
		_, err := vs.SavePubKeys(
			ctx,
			tmconsensus.ValidatorsToPubKeys(mfx.Cfg.InitialValidators),
		)
		require.NoError(t, err)
		_, err = vs.SaveVotePowers(
			ctx,
			tmconsensus.ValidatorsToVotePowers(mfx.Cfg.InitialValidators),
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
			require.True(t, tmconsensus.ValidatorSlicesEqual(mfx.Cfg.InitialValidators, v.Validators))
			keyHash, powHash := mfx.Fx.ValidatorHashes()
			require.Equal(t, keyHash, v.ValidatorPubKeyHash)
			require.Equal(t, powHash, v.ValidatorVotePowerHash)

			// Proposed blocks are empty, and nil proofs are ready.
			require.Empty(t, v.ProposedBlocks)
			require.Empty(t, v.PrevoteProofs, 1)
			require.Empty(t, v.PrecommitProofs, 1)

			s := v.VoteSummary
			var expAvailPow uint64
			for _, val := range v.Validators {
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
			require.True(t, tmconsensus.ValidatorSlicesEqual(mfx.Cfg.InitialValidators, v.Validators))
			keyHash, powHash := mfx.Fx.ValidatorHashes()
			require.Equal(t, keyHash, v.ValidatorPubKeyHash)
			require.Equal(t, powHash, v.ValidatorVotePowerHash)

			// Proposed blocks are empty, and nil proofs are ready.
			require.Empty(t, v.ProposedBlocks)
			require.Empty(t, v.PrevoteProofs, 1)
			require.Empty(t, v.PrecommitProofs, 1)

			s := v.VoteSummary
			var expAvailPow uint64
			for _, val := range v.Validators {
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

		require.Equal(t, keyHash, gso.Voting.ValidatorPubKeyHash)
		require.Equal(t, powHash, gso.Voting.ValidatorVotePowerHash)

		require.Equal(t, keyHash, gso.NextRound.ValidatorPubKeyHash)
		require.Equal(t, powHash, gso.NextRound.ValidatorVotePowerHash)
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

		require.Equal(t, keyHash, gso.Committing.ValidatorPubKeyHash)
		require.Equal(t, powHash, gso.Committing.ValidatorVotePowerHash)

		require.Equal(t, keyHash, gso.Voting.ValidatorPubKeyHash)
		require.Equal(t, powHash, gso.Voting.ValidatorVotePowerHash)

		require.Equal(t, keyHash, gso.NextRound.ValidatorPubKeyHash)
		require.Equal(t, powHash, gso.NextRound.ValidatorVotePowerHash)
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

		pb1 := mfx.Fx.NextProposedBlock([]byte("app_data_1"), 0)
		mfx.Fx.SignProposal(ctx, &pb1, 0)
		require.Equal(t, tmconsensus.HandleProposedBlockAccepted, m.HandleProposedBlock(ctx, pb1))
		keyHash, powHash := mfx.Fx.ValidatorHashes()

		voteMap := map[string][]int{
			string(pb1.Block.Hash): {0, 1, 2, 3},
		}
		precommitProof := tmconsensus.PrecommitSparseProof{
			Height:     1,
			Round:      0,
			PubKeyHash: keyHash,
			Proofs:     mfx.Fx.SparsePrecommitProofMap(ctx, 1, 0, voteMap),
		}
		require.Equal(t, tmconsensus.HandleVoteProofsAccepted, m.HandlePrecommitProofs(ctx, precommitProof))

		gso := gtest.ReceiveSoon(t, mfx.GossipStrategyOut)

		require.Equal(t, keyHash, gso.Committing.ValidatorPubKeyHash)
		require.Equal(t, powHash, gso.Committing.ValidatorVotePowerHash)

		require.Equal(t, keyHash, gso.Voting.ValidatorPubKeyHash)
		require.Equal(t, powHash, gso.Voting.ValidatorVotePowerHash)

		require.Equal(t, keyHash, gso.NextRound.ValidatorPubKeyHash)
		require.Equal(t, powHash, gso.NextRound.ValidatorVotePowerHash)
	})
}

func TestMirror_HandleProposedBlock(t *testing.T) {
	t.Run("adds valid proposed block in voting round to gossip strategy output and round store", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		mfx := tmmirrortest.NewFixture(ctx, t, 2)

		m := mfx.NewMirror()
		defer m.Wait()
		defer cancel()

		// Drain the gossip strategy output.
		_ = gtest.ReceiveSoon(t, mfx.GossipStrategyOut)

		// Sign proposed block, because the mirror actually validates this.
		pb1 := mfx.Fx.NextProposedBlock([]byte("app_data_1"), 0)
		mfx.Fx.SignProposal(ctx, &pb1, 0)

		require.Equal(t, tmconsensus.HandleProposedBlockAccepted, m.HandleProposedBlock(ctx, pb1))

		keyHash, powHash := mfx.Fx.ValidatorHashes()

		// The proposed block is added in the background while HandleProposedBlock returns,
		// so synchronize on the voting view update before inspecting the store.
		gso := gtest.ReceiveSoon(t, mfx.GossipStrategyOut)
		// Version should be bumped to 2, because we can be sure the additional proposed block
		// was a single update operation.
		require.Equal(t, uint32(2), gso.Voting.Version)
		require.Equal(t, []tmconsensus.ProposedBlock{pb1}, gso.Voting.ProposedBlocks)

		// Present in the network view inspection.
		var vnv tmconsensus.VersionedRoundView
		require.NoError(t, m.VotingView(ctx, &vnv))

		// Slightly difficult to assert the nil-only vote proofs, so separately assert them
		// and then remove them from the large require.Equal call.
		require.Len(t, vnv.PrevoteProofs, 1)
		_, ok := vnv.PrevoteProofs[""]
		require.True(t, ok)
		vnv.PrevoteProofs = nil

		require.Len(t, vnv.PrecommitProofs, 1)
		_, ok = vnv.PrecommitProofs[""]
		require.True(t, ok)
		vnv.PrecommitProofs = nil

		vs := tmconsensus.NewVoteSummary()
		vs.SetAvailablePower(mfx.Fx.Vals())
		require.Equal(t, tmconsensus.VersionedRoundView{
			RoundView: tmconsensus.RoundView{
				Height:     1,
				Round:      0,
				Validators: mfx.Fx.Vals(),

				ValidatorPubKeyHash:    keyHash,
				ValidatorVotePowerHash: powHash,

				ProposedBlocks: []tmconsensus.ProposedBlock{pb1},

				VoteSummary: vs,
			},
			Version: 2, // Initially 1, then add a prevote (2).
		}, vnv)

		// And present in the round store.
		pbs, _, _, err := mfx.Cfg.RoundStore.LoadRoundState(ctx, 1, 0)
		require.NoError(t, err)
		require.Equal(t, []tmconsensus.ProposedBlock{pb1}, pbs)
	})

	t.Run("only latest proposed block update sent on Voting output channel", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		mfx := tmmirrortest.NewFixture(ctx, t, 2)

		m := mfx.NewMirror()
		defer m.Wait()
		defer cancel()

		initGSO := gtest.ReceiveSoon(t, mfx.GossipStrategyOut)
		require.Equal(t, uint32(1), initGSO.Voting.Version)
		require.Empty(t, initGSO.Voting.ProposedBlocks)

		pb1 := mfx.Fx.NextProposedBlock([]byte("app_data_1"), 0)
		mfx.Fx.SignProposal(ctx, &pb1, 0)
		require.Equal(t, tmconsensus.HandleProposedBlockAccepted, m.HandleProposedBlock(ctx, pb1))

		pb2 := mfx.Fx.NextProposedBlock([]byte("app_data_2"), 1)
		mfx.Fx.SignProposal(ctx, &pb2, 1)
		require.Equal(t, tmconsensus.HandleProposedBlockAccepted, m.HandleProposedBlock(ctx, pb2))

		gso := gtest.ReceiveSoon(t, mfx.GossipStrategyOut)
		// Not asserting the explicit version here -- 3 makes more sense than 2,
		// but we don't need to enforce that,
		// in case we condense those two updates into one in the future for some reason.
		require.Greater(t, gso.Voting.Version, initGSO.Voting.Version)
		require.Equal(t, []tmconsensus.ProposedBlock{pb1, pb2}, gso.Voting.ProposedBlocks)
	})

	t.Run("accepts proposed block to committing view", func(t *testing.T) {
		// If one validator is running slightly behind and proposes a block that reaches the committing view,
		// it should still be included in updates.
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		mfx := tmmirrortest.NewFixture(ctx, t, 4)

		pb03 := mfx.Fx.NextProposedBlock([]byte("app_data_0_3"), 3)
		mfx.Fx.SignProposal(ctx, &pb03, 3)

		mfx.CommitInitialHeight(ctx, []byte("app_data_1"), 0, []int{0, 1, 2, 3})

		m := mfx.NewMirror()
		defer m.Wait()
		defer cancel()

		gso := gtest.ReceiveSoon(t, mfx.GossipStrategyOut)
		require.NotNil(t, gso.Committing)
		require.NotNil(t, gso.Voting)
		require.NotNil(t, gso.NextRound)

		before := gso.Committing.Clone()

		require.Equal(t, tmconsensus.HandleProposedBlockAccepted, m.HandleProposedBlock(ctx, pb03))

		gso = gtest.ReceiveSoon(t, mfx.GossipStrategyOut)
		require.NotNil(t, gso.Committing)
		require.Nil(t, gso.Voting)
		require.Nil(t, gso.NextRound)

		after := gso.Committing.Clone()

		require.Greater(t, after.Version, before.Version)
		require.Subset(t, after.ProposedBlocks, before.ProposedBlocks)
		require.Contains(t, after.ProposedBlocks, pb03)
	})

	t.Run("proposed block for next height backfills commit into voting round", func(t *testing.T) {
		t.Run("when the voting view already has a proposed block matching", func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			mfx := tmmirrortest.NewFixture(ctx, t, 4)

			pb1 := mfx.Fx.NextProposedBlock([]byte("app_data_1"), 1)
			mfx.Fx.SignProposal(ctx, &pb1, 1)

			m := mfx.NewMirror()
			defer m.Wait()
			defer cancel()

			require.Equal(t, tmconsensus.HandleProposedBlockAccepted, m.HandleProposedBlock(ctx, pb1))

			keyHash, _ := mfx.Fx.ValidatorHashes()
			voteMap := map[string][]int{
				string(pb1.Block.Hash): {0, 1, 2, 3},
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
			mfx.Fx.CommitBlock(pb1.Block, []byte("app_state_height_1"), 0, precommitProofs1)

			// Now the fixture can produce the proposed block for height 2.
			pb2 := mfx.Fx.NextProposedBlock([]byte("app_data_2"), 1)
			mfx.Fx.SignProposal(ctx, &pb2, 1)

			// Drain the gossip strategy output before applying the proposed block.
			before := gtest.ReceiveSoon(t, mfx.GossipStrategyOut)
			require.Nil(t, before.Committing) // No committing view yet.
			require.NotNil(t, before.Voting)

			require.Equal(t, tmconsensus.HandleProposedBlockAccepted, m.HandleProposedBlock(ctx, pb2))

			// Height 1 should be in the committing view, and the voting view should have the new proposed block.
			after := gtest.ReceiveSoon(t, mfx.GossipStrategyOut)
			require.NotNil(t, after.Committing)
			require.Equal(t, uint64(1), after.Committing.Height)
			require.Zero(t, after.Committing.Round)

			require.NotNil(t, after.Voting)
			require.Equal(t, uint64(2), after.Voting.Height)
			require.Zero(t, after.Voting.Round)
			require.Equal(t, []tmconsensus.ProposedBlock{pb2}, after.Voting.ProposedBlocks)
		})
	})

	t.Run("ignored when proposed block round is too old", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		mfx := tmmirrortest.NewFixture(ctx, t, 4)

		m := mfx.NewMirror()
		defer m.Wait()
		defer cancel()

		// Proposed block and full precommits at height 1.
		pb1 := mfx.Fx.NextProposedBlock([]byte("app_data_1"), 0)
		mfx.Fx.SignProposal(ctx, &pb1, 0)
		require.Equal(t, tmconsensus.HandleProposedBlockAccepted, m.HandleProposedBlock(ctx, pb1))
		keyHash, _ := mfx.Fx.ValidatorHashes()
		voteMap := map[string][]int{
			string(pb1.Block.Hash): {0, 1, 2, 3},
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
			pb1.Block, []byte("app_state_height_1"), 0,
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

		// Now we are on round 1, so a new proposed block at round 0 should be too old.
		t.Run("older round within voting height", func(t *testing.T) {
			pb2 := mfx.Fx.NextProposedBlock([]byte("app_data_2"), 0)
			mfx.Fx.SignProposal(ctx, &pb2, 0)
			require.Equal(t, uint64(2), pb2.Block.Height)
			require.Zero(t, pb2.Round)
			require.Equal(t, tmconsensus.HandleProposedBlockRoundTooOld, m.HandleProposedBlock(ctx, pb2))
		})

		pb2 := mfx.Fx.NextProposedBlock([]byte("app_data_2"), 0)
		pb2.Round = 1
		mfx.Fx.SignProposal(ctx, &pb2, 0)

		require.Equal(t, tmconsensus.HandleProposedBlockAccepted, m.HandleProposedBlock(ctx, pb2))
		voteMap = map[string][]int{
			string(pb2.Block.Hash): {0, 1, 2, 3},
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
			pb2.Block, []byte("app_state_height_1"), 0,
			mfx.Fx.PrecommitProofMap(ctx, 2, 0, voteMap),
		)
		pb3 := mfx.Fx.NextProposedBlock([]byte("app_data_3"), 0)
		mfx.Fx.SignProposal(ctx, &pb3, 0)

		t.Run("older round within committing height", func(t *testing.T) {
			// Just reuse the existing pb2, overwriting its round to zero.
			pb2.Round = 0
			mfx.Fx.SignProposal(ctx, &pb2, 0)
			require.Equal(t, tmconsensus.HandleProposedBlockRoundTooOld, m.HandleProposedBlock(ctx, pb2))
		})

		require.Equal(t, tmconsensus.HandleProposedBlockAccepted, m.HandleProposedBlock(ctx, pb3))
		voteMap = map[string][]int{
			string(pb3.Block.Hash): {0, 1, 2, 3},
		}
		precommitProof = tmconsensus.PrecommitSparseProof{
			Height:     3,
			Round:      0,
			PubKeyHash: keyHash,
			Proofs:     mfx.Fx.SparsePrecommitProofMap(ctx, 3, 0, voteMap),
		}
		require.Equal(t, tmconsensus.HandleVoteProofsAccepted, m.HandlePrecommitProofs(ctx, precommitProof))

		t.Run("height that is no longer in view", func(t *testing.T) {
			require.Equal(t, tmconsensus.HandleProposedBlockRoundTooOld, m.HandleProposedBlock(ctx, pb1))
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

		// Sign proposed block, because the mirror actually validates this.
		pb1 := mfx.Fx.NextProposedBlock([]byte("app_data_1"), 0)
		mfx.Fx.SignProposal(ctx, &pb1, 0)

		require.Equal(t, tmconsensus.HandleProposedBlockAccepted, m.HandleProposedBlock(ctx, pb1))

		keyHash, powHash := mfx.Fx.ValidatorHashes()

		// One active and one nil prevote.
		voteMap := map[string][]int{
			string(pb1.Block.Hash): {0},
			"":                     {1},
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
				Height:     1,
				Round:      0,
				Validators: mfx.Fx.Vals(),

				ValidatorPubKeyHash:    keyHash,
				ValidatorVotePowerHash: powHash,

				ProposedBlocks: []tmconsensus.ProposedBlock{pb1},

				PrevoteProofs: fullPrevoteProofMap,
				PrecommitProofs: mfx.Fx.PrecommitProofMap(ctx, 1, 0, map[string][]int{
					"": {},
				}),
				VoteSummary: vs,
			},
			Version: 3, // Started at 1, added a proposed block (2), then bulk added prevotes (3).
			PrevoteBlockVersions: map[string]uint32{
				"":                     1,
				string(pb1.Block.Hash): 1,
			},
		}, vnv)

		_, prevotes, _, err := mfx.Cfg.RoundStore.LoadRoundState(ctx, 1, 0)
		require.NoError(t, err)
		require.Equal(t, fullPrevoteProofMap, prevotes)
	})

	t.Run("concurrent independent updates accepted", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		mfx := tmmirrortest.NewFixture(ctx, t, 10)

		m := mfx.NewMirror()
		defer m.Wait()
		defer cancel()

		// Sign proposed block, because the mirror actually validates this.
		pb1 := mfx.Fx.NextProposedBlock([]byte("app_data_1"), 0)
		mfx.Fx.SignProposal(ctx, &pb1, 0)

		require.Equal(t, tmconsensus.HandleProposedBlockAccepted, m.HandleProposedBlock(ctx, pb1))

		keyHash, powHash := mfx.Fx.ValidatorHashes()

		// Start an independent goroutine for each prevote to submit.
		start := make(chan struct{})
		feedbackCh := make(chan tmconsensus.HandleVoteProofsResult, 10)
		for i := 0; i < 10; i++ {
			go func(i int) {
				voteMap := map[string][]int{
					string(pb1.Block.Hash): {i},
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
			string(pb1.Block.Hash): {0, 1, 2, 3, 4, 5, 6, 7, 8, 9},
			"":                     {}, // Empty proof always present.
		}
		fullPrevoteProofMap := mfx.Fx.PrevoteProofMap(ctx, 1, 0, fullVoteMap)

		// Now assert all 10 goroutines have had their message accepted.
		close(start)
		for i := 0; i < 10; i++ {
			require.Equal(t, tmconsensus.HandleVoteProofsAccepted, gtest.ReceiveSoon(t, feedbackCh))
		}

		// Only asserting against the RoundView content, ignoring the versioned details.
		expNV := tmconsensus.RoundView{
			Height:     1,
			Round:      0,
			Validators: mfx.Fx.Vals(),

			ValidatorPubKeyHash:    keyHash,
			ValidatorVotePowerHash: powHash,

			ProposedBlocks: []tmconsensus.ProposedBlock{pb1},

			PrevoteProofs: fullPrevoteProofMap,
			PrecommitProofs: mfx.Fx.PrecommitProofMap(ctx, 1, 0, map[string][]int{
				"": {},
			}),
		}

		vs := tmconsensus.NewVoteSummary()
		vs.SetAvailablePower(expNV.Validators)
		vs.SetPrevotePowers(expNV.Validators, expNV.PrevoteProofs)
		expNV.VoteSummary = vs

		var vnv tmconsensus.VersionedRoundView
		require.NoError(t, m.VotingView(ctx, &vnv))
		require.Equal(t, expNV, vnv.RoundView)

		_, prevotes, _, err := mfx.Cfg.RoundStore.LoadRoundState(ctx, 1, 0)
		require.NoError(t, err)
		require.Equal(t, fullPrevoteProofMap, prevotes)

		gso := gtest.ReceiveSoon(t, mfx.GossipStrategyOut)

		// The GossipStrategyOut values have empty nil votes stripped.
		// Maybe we will do that for the VotingView method too, if we even keep that method.
		// Delete the invalid expected value for now.
		delete(expNV.PrevoteProofs, "")
		delete(expNV.PrecommitProofs, "")
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

		// Sign proposed block, because the mirror actually validates this.
		pb1 := mfx.Fx.NextProposedBlock([]byte("app_data_1"), 0)
		mfx.Fx.SignProposal(ctx, &pb1, 0)

		require.Equal(t, tmconsensus.HandleProposedBlockAccepted, m.HandleProposedBlock(ctx, pb1))

		keyHash, powHash := mfx.Fx.ValidatorHashes()

		// One active and one nil precommit.
		// Still below 2/3 voting power present.
		voteMap := map[string][]int{
			string(pb1.Block.Hash): {0},
			"":                     {1},
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
				Height:     1,
				Round:      0,
				Validators: mfx.Fx.Vals(),

				ValidatorPubKeyHash:    keyHash,
				ValidatorVotePowerHash: powHash,

				ProposedBlocks: []tmconsensus.ProposedBlock{pb1},

				PrevoteProofs: mfx.Fx.PrevoteProofMap(ctx, 1, 0, map[string][]int{
					"": {},
				}),
				PrecommitProofs: fullPrecommitProofMap,
				VoteSummary:     vs,
			},
			Version: 3, // Started at 1, added a proposed block (2), then bulk added precommits (3).
			PrecommitBlockVersions: map[string]uint32{
				"":                     1,
				string(pb1.Block.Hash): 1,
			},
		}, vnv)

		_, _, precommits, err := mfx.Cfg.RoundStore.LoadRoundState(ctx, 1, 0)
		require.NoError(t, err)
		require.Equal(t, fullPrecommitProofMap, precommits)
	})
}

func TestMirror_FullRound(t *testing.T) {
	for _, tc := range []struct {
		targetName string
		blockHash  func(tmconsensus.ProposedBlock) string
		wantNHR    tmmirror.NetworkHeightRound
	}{
		{
			targetName: "block",
			blockHash: func(pb tmconsensus.ProposedBlock) string {
				return string(pb.Block.Hash)
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
			blockHash: func(tmconsensus.ProposedBlock) string {
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

			// Sign proposed block, because the mirror actually validates this.
			pb1 := mfx.Fx.NextProposedBlock([]byte("app_data_1"), 0)
			mfx.Fx.SignProposal(ctx, &pb1, 0)

			require.Equal(t, tmconsensus.HandleProposedBlockAccepted, m.HandleProposedBlock(ctx, pb1))

			keyHash, _ := mfx.Fx.ValidatorHashes()

			// Skipping prevotes as they aren't strictly required here.

			// Half the votes arrive.
			targetHash := tc.blockHash(pb1)
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
		// so we should have no proposed blocks,
		// and we should have empty proofs for nil votes.
		require.True(t, tmconsensus.ValidatorSlicesEqual(mfx.Cfg.InitialValidators, vv.Validators))
		require.Empty(t, vv.ProposedBlocks)
		require.Len(t, vv.PrevoteProofs, 1)
		np := vv.PrevoteProofs[""]
		require.Zero(t, np.SignatureBitSet().Count())
		require.Len(t, vv.PrecommitProofs, 1)
		np = vv.PrecommitProofs[""]
		require.Zero(t, np.SignatureBitSet().Count())

		// Committing view has some information.
		require.NoError(t, m.CommittingView(ctx, &vv))
		require.Equal(t, uint32(1), vv.Version)
		require.Equal(t, uint64(1), vv.Height)
		require.Zero(t, vv.Round)

		require.Len(t, vv.ProposedBlocks, 1)
		pb := vv.ProposedBlocks[0]

		// We didn't store any prevote proofs, but the nil proof should still be present.
		require.Len(t, vv.PrevoteProofs, 1)
		np = vv.PrevoteProofs[""]
		require.Zero(t, np.SignatureBitSet().Count())

		// And we should have all 4 signatures for the proposed block in precommit.
		require.Len(t, vv.PrecommitProofs, 2)
		np = vv.PrevoteProofs[""]
		require.Zero(t, np.SignatureBitSet().Count())
		np = vv.PrecommitProofs[string(pb.Block.Hash)]
		require.Equal(t, uint(4), np.SignatureBitSet().Count())
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
			pbs, _, _, err := mfx.Cfg.RoundStore.LoadRoundState(ctx, 1, 0)
			require.NoError(t, err)
			require.Len(t, pbs, 1)
			pb1Hash := string(pbs[0].Block.Hash)

			voter := vt.VoterFunc(mfx, m)
			res := voter.HandleProofs(ctx, 1, 0, map[string][]int{pb1Hash: {3}})
			require.Equal(t, tmconsensus.HandleVoteProofsAccepted, res)

			// New Committing view sent on output channel.
			cv := gtest.ReceiveSoon(t, mfx.GossipStrategyOut).Committing
			require.Equal(t, expVoteCount, voter.ProofsFromView(cv.RoundView)[pb1Hash].SignatureBitSet().Count())

			// Round store matches too.
			_, prevotes, precommits, err := mfx.Cfg.RoundStore.LoadRoundState(ctx, 1, 0)
			require.NoError(t, err)
			votes := voter.ProofsFromRoundStateMaps(prevotes, precommits)
			require.Equal(t, expVoteCount, votes[pb1Hash].SignatureBitSet().Count())
		})
	}

	t.Run("proposed block with new commit info is backfilled into committing view", func(t *testing.T) {
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

		// Get the proposed block from the committing view.
		var vv tmconsensus.VersionedRoundView
		require.NoError(t, m.CommittingView(ctx, &vv))

		pb1Hash := vv.ProposedBlocks[0].Block.Hash

		// Make a new PB that contains the precommit from the last validator.
		pb2 := mfx.Fx.NextProposedBlock([]byte("app_data_2"), 0)
		precommitProofs := mfx.Fx.SparsePrecommitProofMap(ctx, 1, 0, map[string][]int{
			string(pb1Hash): {0, 1, 3}, // Missing val 2, has val 3.
		})

		pb2.Block.PrevCommitProof.Proofs = precommitProofs
		mfx.Fx.RecalculateHash(&pb2.Block)
		mfx.Fx.SignProposal(ctx, &pb2, 0)

		// Handling the proposed block should cause the new precommit
		// to backfill into the committing view.
		require.Equal(t, tmconsensus.HandleProposedBlockAccepted, m.HandleProposedBlock(ctx, pb2))

		// Reload the committing view.
		require.NoError(t, m.CommittingView(ctx, &vv))

		// The precommits in the commit view, at height 1, now have all 4 signatures.
		commitProof := vv.PrecommitProofs[string(pb1Hash)]
		require.Equal(t, uint(4), commitProof.SignatureBitSet().Count())

		// And the update is available on the commit view channel.
		cv := gtest.ReceiveSoon(t, mfx.GossipStrategyOut).Committing
		require.Greater(t, cv.Version, initCV.Version)
		require.Equal(t, uint(4), cv.PrecommitProofs[string(pb1Hash)].SignatureBitSet().Count())
		require.Equal(t, uint(3), initCV.PrecommitProofs[string(pb1Hash)].SignatureBitSet().Count())
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

	pb1 := mfx.Fx.NextProposedBlock([]byte("app_data_1"), 0)
	mfx.Fx.SignProposal(ctx, &pb1, 0)

	require.Equal(t, tmconsensus.HandleProposedBlockAccepted, m.HandleProposedBlock(ctx, pb1))

	// Now advance the fixture with a full set of precommits.
	voteMap1 := map[string][]int{
		string(pb1.Block.Hash): {0, 1, 2, 3},
	}
	precommitProofs1 := mfx.Fx.PrecommitProofMap(ctx, 1, 0, voteMap1)
	mfx.Fx.CommitBlock(pb1.Block, []byte("app_state_height_1"), 0, precommitProofs1)

	// And advance the mirror by submitting a full set of precommits.
	keyHash, _ := mfx.Fx.ValidatorHashes()
	require.Equal(t, tmconsensus.HandleVoteProofsAccepted, m.HandlePrecommitProofs(ctx, tmconsensus.PrecommitSparseProof{
		Height: 1,
		Round:  0,

		PubKeyHash: keyHash,

		Proofs: mfx.Fx.SparsePrecommitProofMap(ctx, 1, 0, voteMap1),
	}))

	// Now on height 2, we have a committing view at height 1.
	// Nothing should be in the block store yet.
	_, err := mfx.Cfg.BlockStore.LoadBlock(ctx, 1)
	require.ErrorIs(t, err, tmconsensus.HeightUnknownError{Want: 1})

	// Now propose and commit height 2.
	pb2 := mfx.Fx.NextProposedBlock([]byte("app_data_2"), 0)
	mfx.Fx.SignProposal(ctx, &pb2, 0)

	require.Equal(t, tmconsensus.HandleProposedBlockAccepted, m.HandleProposedBlock(ctx, pb2))

	// Drain metrics.
	_ = gtest.ReceiveSoon(t, mCh)

	// Update the fixture for height 2.
	voteMap2 := map[string][]int{
		string(pb2.Block.Hash): {0, 1, 2, 3},
	}
	precommitProofs2 := mfx.Fx.PrecommitProofMap(ctx, 2, 0, voteMap2)
	mfx.Fx.CommitBlock(pb1.Block, []byte("app_state_height_2"), 0, precommitProofs2)

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

	// This means height 1 must be in the block store.
	cb1, err := mfx.Cfg.BlockStore.LoadBlock(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, pb1.Block, cb1.Block)
	require.Equal(t, pb2.Block.PrevCommitProof, cb1.Proof)
	require.Equal(t, tmconsensus.CommittedBlock{
		Block: pb1.Block,
		Proof: pb2.Block.PrevCommitProof,
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

		pb1 := mfx.Fx.NextProposedBlock([]byte("app_data_1"), 0)
		mfx.Fx.SignProposal(ctx, &pb1, 0)

		require.Equal(t, tmconsensus.HandleProposedBlockAccepted, m.HandleProposedBlock(ctx, pb1))

		// Half of the precommits are for the block, the other half are for nil.
		voteMap1 := map[string][]int{
			string(pb1.Block.Hash): {0, 1},
			"":                     {2, 3},
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
		// it doesn't have any proposed blocks or votes.
		// But it was a NextRound value which started at Version 1,
		// and we increment the version again when the otherwise-unchanged view
		// gets shifted from NextRound to Voting.
		require.Equal(t, uint32(2), vv.Version)
		require.Equal(t, uint64(1), vv.Height)
		require.Equal(t, uint32(1), vv.Round)
		require.Empty(t, vv.ProposedBlocks)

		// The nil prevote and precommit are present but empty.
		require.Empty(t, vv.PrevoteProofs)
		require.Empty(t, vv.PrecommitProofs)

		// No new committing update as a result of advancing the round.
		require.Nil(t, gso.Committing)
	})
}

// The proposed block is too old or too far in the future.
func TestMirror_proposedBlockOutOfBounds(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mfx := tmmirrortest.NewFixture(ctx, t, 2)

	m := mfx.NewMirror()
	defer m.Wait()
	defer cancel()

	// Hold on to a pb1 for the committing round.
	// (We will never send this although it will be otherwise valid.)
	pb10 := mfx.Fx.NextProposedBlock([]byte("missed"), 1)
	mfx.Fx.SignProposal(ctx, &pb10, 1)

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
	pb11 := mfx.Fx.NextProposedBlock([]byte("app_data_1"), 0)
	pb11.Round = 1
	mfx.Fx.SignProposal(ctx, &pb11, 0)

	require.Equal(t, tmconsensus.HandleProposedBlockAccepted, m.HandleProposedBlock(ctx, pb11))

	// Now fully precommit 1/1, so we get to voting on height 2.
	voteMap11 := map[string][]int{
		string(pb11.Block.Hash): {0, 1},
	}
	require.Equal(t, tmconsensus.HandleVoteProofsAccepted, m.HandlePrecommitProofs(ctx, tmconsensus.PrecommitSparseProof{
		Height: 1,
		Round:  1,

		PubKeyHash: keyHash,

		Proofs: mfx.Fx.SparsePrecommitProofMap(ctx, 1, 1, voteMap11),
	}))

	// Now if the proposed block for 1/0 arrives, it's a "too old" result.
	// Arguably we could check or save it for detecting a double-propose,
	// but at this point we will just drop it.
	require.Equal(t, tmconsensus.HandleProposedBlockRoundTooOld, m.HandleProposedBlock(ctx, pb10))

	// And now let's make a fake proposed block multiple heights in the future,
	// from a different fixture altogether so the voting hashes and validators differ.
	ffx := tmconsensustest.NewStandardFixture(5)
	for i := 0; i < 10; i++ {
		pb := ffx.NextProposedBlock([]byte("ignore"), 4)

		ffx.CommitBlock(pb.Block, []byte(fmt.Sprintf("height_%d", i+1)), 0, map[string]gcrypto.CommonMessageSignatureProof{
			string(pb.Block.Hash): ffx.PrecommitSignatureProof(
				ctx,
				tmconsensus.VoteTarget{
					Height:    uint64(i) + 1,
					Round:     0,
					BlockHash: string(pb.Block.Hash),
				}, nil, []int{0, 1, 2, 3, 4},
			),
		})
	}

	futurePB := ffx.NextProposedBlock([]byte("far_future"), 4)
	ffx.SignProposal(ctx, &futurePB, 4)

	require.Equal(t, tmconsensus.HandleProposedBlockRoundTooFarInFuture, m.HandleProposedBlock(ctx, futurePB))
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

				// Propose block at height 2.
				pb2 := mfx.Fx.NextProposedBlock([]byte("app_data_2"), 0)
				mfx.Fx.SignProposal(ctx, &pb2, 0)

				require.Equal(t, tmconsensus.HandleProposedBlockAccepted, m.HandleProposedBlock(ctx, pb2))

				// Drain the metrics before handling the precommit proofs.
				_ = gtest.ReceiveSoon(t, mCh)

				// Full precommit for pb2.
				voteMap2 := map[string][]int{
					string(pb2.Block.Hash): {0, 1},
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

				// Now block 2 is in committing.
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
					// Vote for the first proposed block,
					// which we don't have directly because it was scoped inside the mfx.CommitInitialHeight call.
					targetHeight = 1
					targetBlockHash = string(pb2.Block.PrevBlockHash)
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

			pbf := tmelinktest.NewPBFetcher(1, 1)
			mfx.Cfg.ProposedBlockFetcher = pbf.ProposedBlockFetcher()

			m := mfx.NewMirror()
			defer m.Wait()
			defer cancel()

			voter := vt.VoterFunc(mfx, m)

			// Make the proposed block but don't give it to the mirror.
			pb1 := mfx.Fx.NextProposedBlock([]byte("app_data_1"), 0)
			mfx.Fx.SignProposal(ctx, &pb1, 0)

			// With one ote there is no fetch request.
			voteMap := map[string][]int{
				string(pb1.Block.Hash): {0},
			}

			res := voter.HandleProofs(ctx, 1, 0, voteMap)
			require.Equal(t, tmconsensus.HandleVoteProofsAccepted, res)

			gtest.NotSending(t, pbf.ReqCh)

			// With a second vote, we have exceeded minority power, so there must be a fetch request.
			voteMap[string(pb1.Block.Hash)] = []int{1}
			res = voter.HandleProofs(ctx, 1, 0, voteMap)
			require.Equal(t, tmconsensus.HandleVoteProofsAccepted, res)

			req := gtest.ReceiveSoon(t, pbf.ReqCh)
			require.Equal(t, uint64(1), req.Height)
			require.Equal(t, string(pb1.Block.Hash), req.BlockHash)
			require.NoError(t, req.Ctx.Err()) // Context is non-nil and has not been canceled.

			// And with a third vote, there is no new request.
			voteMap[string(pb1.Block.Hash)] = []int{2}
			res = voter.HandleProofs(ctx, 1, 0, voteMap)
			require.Equal(t, tmconsensus.HandleVoteProofsAccepted, res)

			gtest.NotSending(t, pbf.ReqCh)

			// Drain the voting view before we send the proposed block via the fetch channel.
			vrv := gtest.ReceiveSoon(t, mfx.GossipStrategyOut).Voting
			require.Empty(t, vrv.ProposedBlocks) // Sanity check.

			gtest.SendSoon(t, pbf.FetchedCh, pb1)

			if vt.Name == "prevote" {
				// The arrival of the fetched block should be reflected in the voting view.
				vrv = gtest.ReceiveSoon(t, mfx.GossipStrategyOut).Voting
				require.Equal(t, []tmconsensus.ProposedBlock{pb1}, vrv.ProposedBlocks)

				// And its arrival should have canceled the context, as a matter of cleanup.
				require.Error(t, req.Ctx.Err())
			} else if vt.Name == "precommit" {
				// The arrival of the fetched block should advance the voting view to the next height.
				vrv = gtest.ReceiveSoon(t, mfx.GossipStrategyOut).Voting
				require.Equal(t, uint64(2), vrv.Height)
				require.Zero(t, vrv.Round)
				require.Empty(t, vrv.ProposedBlocks)

				// And its arrival should have canceled the context, as a matter of cleanup.
				require.Error(t, req.Ctx.Err())
			} else {
				panic("unreachable")
			}
		})
	}

	t.Run("initiated fetch cancelled when proposed block arrives from network", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		mfx := tmmirrortest.NewFixture(ctx, t, 4)

		pbf := tmelinktest.NewPBFetcher(1, 1)
		mfx.Cfg.ProposedBlockFetcher = pbf.ProposedBlockFetcher()

		m := mfx.NewMirror()
		defer m.Wait()
		defer cancel()

		// Make the proposed block but don't give it to the mirror.
		pb1 := mfx.Fx.NextProposedBlock([]byte("app_data_1"), 0)
		mfx.Fx.SignProposal(ctx, &pb1, 0)

		// With two prevotes, there is a fetch request.
		voteMap := map[string][]int{
			string(pb1.Block.Hash): {0, 1},
		}

		keyHash, _ := mfx.Fx.ValidatorHashes()
		res := m.HandlePrevoteProofs(ctx, tmconsensus.PrevoteSparseProof{
			Height: 1,
			Round:  0,

			PubKeyHash: keyHash,

			Proofs: mfx.Fx.SparsePrevoteProofMap(ctx, 1, 0, voteMap),
		})
		require.Equal(t, tmconsensus.HandleVoteProofsAccepted, res)

		req := gtest.ReceiveSoon(t, pbf.ReqCh)
		require.Equal(t, uint64(1), req.Height)
		require.Equal(t, string(pb1.Block.Hash), req.BlockHash)
		require.NoError(t, req.Ctx.Err()) // Context is non-nil and has not been canceled.

		// Sending the proposed block through the normal channel, is accepted as usual.
		require.Equal(t, tmconsensus.HandleProposedBlockAccepted, m.HandleProposedBlock(ctx, pb1))

		// The proposed block is reflected in the voting view.
		vrv := gtest.ReceiveSoon(t, mfx.GossipStrategyOut).Voting
		require.Equal(t, []tmconsensus.ProposedBlock{pb1}, vrv.ProposedBlocks)

		// And its arrival should have canceled the context, as a matter of cleanup.
		require.Error(t, req.Ctx.Err())
	})
}

func TestMirror_nextRound(t *testing.T) {
	t.Run("proposed block", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		mfx := tmmirrortest.NewFixture(ctx, t, 4)

		// We are at 1/0, so a received proposed block at 1/1 must be saved to the NextRound view.
		pb11 := mfx.Fx.NextProposedBlock([]byte("app_data_1"), 0)
		pb11.Round = 1
		mfx.Fx.RecalculateHash(&pb11.Block)
		mfx.Fx.SignProposal(ctx, &pb11, 0)

		m := mfx.NewMirror()
		defer m.Wait()
		defer cancel()

		// Drain the voting view and next round outputs first.
		_ = gtest.ReceiveSoon(t, mfx.GossipStrategyOut)

		// Proposed block for 1/1 is accepted.
		require.Equal(t, tmconsensus.HandleProposedBlockAccepted, m.HandleProposedBlock(ctx, pb11))

		// Just a proposed block is not sufficient to advance the round.
		// The voting view hasn't changed.
		gso := gtest.ReceiveSoon(t, mfx.GossipStrategyOut)
		require.Nil(t, gso.Voting)

		// But the next round view has the proposed block.
		nrrv := gso.NextRound
		require.Equal(t, uint64(1), nrrv.Height)
		require.Equal(t, uint32(1), nrrv.Round)
		require.Equal(t, []tmconsensus.ProposedBlock{pb11}, nrrv.ProposedBlocks)

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

		// The voting view shows the proposed block, and the next round doesn't.
		gso = gtest.ReceiveSoon(t, mfx.GossipStrategyOut)
		vrv := gso.Voting
		require.Equal(t, uint64(1), vrv.Height)
		require.Equal(t, uint32(1), vrv.Round)
		require.Equal(t, []tmconsensus.ProposedBlock{pb11}, vrv.ProposedBlocks)
		require.Equal(t, keyHash, vrv.ValidatorPubKeyHash)
		require.Equal(t, powHash, vrv.ValidatorVotePowerHash)

		nrrv = gso.NextRound
		require.Equal(t, uint64(1), nrrv.Height)
		require.Equal(t, uint32(2), nrrv.Round)
		require.Empty(t, nrrv.ProposedBlocks)
	})

	for _, vt := range voteTypes {
		vt := vt
		t.Run(vt.Name, func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			mfx := tmmirrortest.NewFixture(ctx, t, 4)

			// We will be fetching a missed PB later in the test.
			pbf := tmelinktest.NewPBFetcher(1, 1)
			mfx.Cfg.ProposedBlockFetcher = pbf.ProposedBlockFetcher()

			// We are at 1/0, and we are going to vote on a proposed block for 1/1.
			pb11 := mfx.Fx.NextProposedBlock([]byte("app_data_1"), 0)
			pb11.Round = 1
			mfx.Fx.RecalculateHash(&pb11.Block)
			mfx.Fx.SignProposal(ctx, &pb11, 0)

			m := mfx.NewMirror()
			defer m.Wait()
			defer cancel()

			// Drain the voting view and next round outputs first.
			_ = gtest.ReceiveSoon(t, mfx.GossipStrategyOut)

			// Vote for 1/1 is accepted.
			voter := vt.VoterFunc(mfx, m)
			res := voter.HandleProofs(ctx, 1, 1, map[string][]int{string(pb11.Block.Hash): {0}})
			require.Equal(t, tmconsensus.HandleVoteProofsAccepted, res)

			// The Next Round View has the votes.
			nrrv := gtest.ReceiveSoon(t, mfx.GossipStrategyOut).NextRound
			require.Equal(t, uint64(1), nrrv.Height)
			require.Equal(t, uint32(1), nrrv.Round)
			proofs := voter.ProofsFromView(nrrv.RoundView)
			require.Equal(t, uint(1), proofs[string(pb11.Block.Hash)].SignatureBitSet().Count())

			// And so does the round store.
			_, prevotes, precommits, err := mfx.Cfg.RoundStore.LoadRoundState(ctx, 1, 1)
			require.NoError(t, err)
			votes := voter.ProofsFromRoundStateMaps(prevotes, precommits)
			require.Equal(t, uint(1), votes[string(pb11.Block.Hash)].SignatureBitSet().Count())

			// Now if we exceed the minority vote for that block on 1/1...
			res = voter.HandleProofs(ctx, 1, 1, map[string][]int{string(pb11.Block.Hash): {0, 1}}) // 2/4 validators.
			require.Equal(t, tmconsensus.HandleVoteProofsAccepted, res)

			// The voting round is now 1/1,
			// even if the vote was precommit, because we didn't get the proposed block yet.
			vrv := gtest.ReceiveSoon(t, mfx.GossipStrategyOut).Voting
			require.Equal(t, uint64(1), vrv.Height)
			require.Equal(t, uint32(1), vrv.Round)

			// And because we crossed the minority threshold,
			// there was a request for the block we missed.
			req := gtest.ReceiveSoon(t, pbf.ReqCh)
			require.Equal(t, uint64(1), req.Height)
			require.Equal(t, string(pb11.Block.Hash), req.BlockHash)
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

		// Proposed block from state machine accepted.
		pb := mfx.Fx.NextProposedBlock([]byte("app_data_1"), 0)
		mfx.Fx.SignProposal(ctx, &pb, 0)

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
		require.Empty(t, rer.CB.Block.Hash)
		require.Empty(t, rer.CB.Proof.Proofs)

		require.Equal(t, uint64(1), rer.VRV.Height)
		require.Empty(t, rer.VRV.ProposedBlocks)
		require.Len(t, rer.VRV.PrevoteProofs, 1)   // Empty nil proof.
		require.Len(t, rer.VRV.PrecommitProofs, 1) // Empty nil proof.

		t.Run("proposed block", func(t *testing.T) {
			// Buffered channel so we can just send without select.
			actionCh <- tmeil.StateMachineRoundAction{PB: pb}

			vrv := gtest.ReceiveSoon(t, mfx.GossipStrategyOut).Voting
			require.Equal(t, []tmconsensus.ProposedBlock{pb}, vrv.ProposedBlocks)
		})

		pbHash := string(pb.Block.Hash)

		t.Run("prevote", func(t *testing.T) {
			vt := tmconsensus.VoteTarget{
				Height: 1, Round: 0,
				BlockHash: pbHash,
			}
			signContent, err := tmconsensus.PrevoteSignBytes(vt, mfx.Fx.SignatureScheme)
			require.NoError(t, err)
			// Buffered channel so we can just send without select.
			actionCh <- tmeil.StateMachineRoundAction{
				Prevote: tmeil.ScopedSignature{
					TargetHash:  pbHash,
					SignContent: signContent,
					Sig:         mfx.Fx.PrevoteSignature(ctx, vt, 0),
				},
			}

			vrv := gtest.ReceiveSoon(t, mfx.GossipStrategyOut).Voting
			// Nil block is always populated, just clear it for a simpler assertion here.
			delete(vrv.PrevoteProofs, "")
			require.Equal(t, map[string]gcrypto.CommonMessageSignatureProof{
				pbHash: mfx.Fx.PrevoteSignatureProof(ctx, vt, nil, []int{0}),
			}, vrv.PrevoteProofs)
		})

		t.Run("precommit", func(t *testing.T) {
			vt := tmconsensus.VoteTarget{
				Height: 1, Round: 0,
				BlockHash: pbHash,
			}
			signContent, err := tmconsensus.PrecommitSignBytes(vt, mfx.Fx.SignatureScheme)
			require.NoError(t, err)
			// Buffered channel so we can just send without select.
			actionCh <- tmeil.StateMachineRoundAction{
				Precommit: tmeil.ScopedSignature{
					TargetHash:  pbHash,
					SignContent: signContent,
					Sig:         mfx.Fx.PrecommitSignature(ctx, vt, 0),
				},
			}

			vrv := gtest.ReceiveSoon(t, mfx.GossipStrategyOut).Voting
			// Nil block is always populated, just clear it for a simpler assertion here.
			delete(vrv.PrecommitProofs, "")
			require.Equal(t, map[string]gcrypto.CommonMessageSignatureProof{
				pbHash: mfx.Fx.PrecommitSignatureProof(ctx, vt, nil, []int{0}),
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
		// A committing block is not the same as a committed block.
		// We have sufficient info to commit,
		// but we don't know the canonical commits that will be persisted to chain.
		require.Empty(t, rer.CB.Block.Hash)
		require.Empty(t, rer.CB.Proof.Proofs)

		pbs, _, _, err := mfx.Cfg.RoundStore.LoadRoundState(ctx, 1, 0)
		require.NoError(t, err)
		require.Len(t, pbs, 1)

		require.Equal(t, uint64(1), rer.VRV.Height)
		require.Zero(t, rer.VRV.Round)
		require.Equal(t, pbs, rer.VRV.ProposedBlocks)
		require.Len(t, rer.VRV.PrevoteProofs, 1)   // No prevotes were stored, that's fine.
		require.Len(t, rer.VRV.PrecommitProofs, 2) // Empty nil proof and proof for the block.
	})

	t.Run("historic block sent as CommittedBlock", func(t *testing.T) {
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

		// Propose block at height 2.
		pb2 := mfx.Fx.NextProposedBlock([]byte("app_data_2"), 0)
		mfx.Fx.SignProposal(ctx, &pb2, 0)

		require.Equal(t, tmconsensus.HandleProposedBlockAccepted, m.HandleProposedBlock(ctx, pb2))

		// Full precommit for pb2.
		voteMap2 := map[string][]int{
			string(pb2.Block.Hash): {0, 1, 2, 3},
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

		// Now height 1 is fully committed.
		cb, err := mfx.Cfg.BlockStore.LoadBlock(ctx, 1)
		require.NoError(t, err)

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

		// The whole VRV is blank.
		require.Zero(t, rer.VRV.Height)

		// But the committed block matches what was in the store.
		require.Equal(t, cb, rer.CB)

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
		require.Empty(t, rer.CB.Block.Hash) // Nothing in the CommittedBlock.
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

		// Proposed block from different validator.
		pb := mfx.Fx.NextProposedBlock([]byte("app_data_1"), 1)
		mfx.Fx.SignProposal(ctx, &pb, 1)
		require.Equal(t, tmconsensus.HandleProposedBlockAccepted, m.HandleProposedBlock(ctx, pb))

		smv := gtest.ReceiveSoon(t, mfx.StateMachineRoundViewOut).VRV
		require.Equal(t, []tmconsensus.ProposedBlock{pb}, smv.ProposedBlocks)

		// Then the state machine submits its valid prevote to the mirror.
		pbHash := string(pb.Block.Hash)
		vt := tmconsensus.VoteTarget{
			Height: 1, Round: 0,
			BlockHash: pbHash,
		}
		signContent, err := tmconsensus.PrevoteSignBytes(vt, mfx.Fx.SignatureScheme)
		require.NoError(t, err)
		// Buffered channel so we can just send without select.
		actionCh <- tmeil.StateMachineRoundAction{
			Prevote: tmeil.ScopedSignature{
				TargetHash:  pbHash,
				SignContent: signContent,
				Sig:         mfx.Fx.PrevoteSignature(ctx, vt, 0),
			},
		}

		// The mirror repeats the vote back to the state machine.
		smv = gtest.ReceiveSoon(t, mfx.StateMachineRoundViewOut).VRV
		require.Equal(t, uint(1), smv.PrevoteProofs[pbHash].SignatureBitSet().Count())

		// Now the mirror receives the remainder of the prevotes over the network.
		voteMap := map[string][]int{
			pbHash: {1, 2, 3},
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
		require.Equal(t, uint(4), smv.PrevoteProofs[pbHash].SignatureBitSet().Count())

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
		require.Equal(t, uint(3), smv.PrecommitProofs[pbHash].SignatureBitSet().Count())

		// And as a sanity check, the gossip strategy output indicates that 1/0 is now in committing.
		gso := gtest.ReceiveSoon(t, mfx.GossipStrategyOut)
		require.NotNil(t, gso.Committing)
		require.Equal(t, uint64(1), gso.Committing.Height)
		require.Zero(t, gso.Committing.Round)
		require.Equal(t, uint(3), gso.Committing.PrecommitProofs[pbHash].SignatureBitSet().Count())
		require.NotNil(t, gso.Voting)
		require.Equal(t, uint64(2), gso.Voting.Height)
		require.Zero(t, gso.Voting.Round)

		// Now the state machine submits its precommit, slightly lagging the mirror.
		signContent, err = tmconsensus.PrecommitSignBytes(vt, mfx.Fx.SignatureScheme)
		require.NoError(t, err)
		// Buffered channel so we can just send without select.
		actionCh <- tmeil.StateMachineRoundAction{
			Precommit: tmeil.ScopedSignature{
				TargetHash:  pbHash,
				SignContent: signContent,
				Sig:         mfx.Fx.PrecommitSignature(ctx, vt, 0),
			},
		}

		// Finally, the gossip strategy output indicates that precommit was accepted.
		gso = gtest.ReceiveSoon(t, mfx.GossipStrategyOut)
		require.NotNil(t, gso.Committing)
		require.Equal(t, uint64(1), gso.Committing.Height)
		require.Zero(t, gso.Committing.Round)
		require.Equal(t, uint(4), gso.Committing.PrecommitProofs[pbHash].SignatureBitSet().Count())
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

		// No proposed block here.
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
		require.Equal(t, uint(1), smv.PrevoteProofs[""].SignatureBitSet().Count())

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
		require.Equal(t, uint(4), smv.PrevoteProofs[""].SignatureBitSet().Count())

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
		require.Equal(t, uint(3), smv.PrecommitProofs[""].SignatureBitSet().Count())
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

	// Add a proposed block to the mirror before the state machine starts.
	pb11 := mfx.Fx.NextProposedBlock([]byte("app_data_1_1"), 1)
	mfx.Fx.SignProposal(ctx, &pb11, 1)
	require.Equal(t, tmconsensus.HandleProposedBlockAccepted, m.HandleProposedBlock(ctx, pb11))

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
	require.Equal(t, []tmconsensus.ProposedBlock{pb11}, rer.VRV.ProposedBlocks)

	// And because the initial state update consumed the state machine view,
	// there is still nothing sent on the state machine view out channel.
	gtest.NotSending(t, mfx.StateMachineRoundViewOut)

	// But now if another proposed block arrives, it will reach the state machine view out.
	pb12 := mfx.Fx.NextProposedBlock([]byte("app_data_1_2"), 2)
	mfx.Fx.SignProposal(ctx, &pb12, 2)
	require.Equal(t, tmconsensus.HandleProposedBlockAccepted, m.HandleProposedBlock(ctx, pb12))
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

		// Enqueue a proposed block for the state machine's round, but don't read it from the state machine yet.
		pb10 := mfx.Fx.NextProposedBlock([]byte("ignored"), 0)
		mfx.Fx.SignProposal(ctx, &pb10, 0)
		require.Equal(t, tmconsensus.HandleProposedBlockAccepted, m.HandleProposedBlock(ctx, pb10))

		// Now the mirror sees a majority nil prevote at round 1.
		keyHash, _ := mfx.Fx.ValidatorHashes()
		pb11VoteMap := map[string][]int{
			"": {1, 2, 3},
		}
		sparsePrevoteProofMap := mfx.Fx.SparsePrevoteProofMap(ctx, 1, 1, pb11VoteMap)
		prevoteProof := tmconsensus.PrevoteSparseProof{
			Height:     1,
			Round:      1,
			PubKeyHash: keyHash,
			Proofs:     sparsePrevoteProofMap,
		}
		require.Equal(t, tmconsensus.HandleVoteProofsAccepted, m.HandlePrevoteProofs(ctx, prevoteProof))

		// Now the state machine reads its input from the mirror.
		smv := gtest.ReceiveSoon(t, mfx.StateMachineRoundViewOut)
		// It has the proposed block for 1/0 in the main VRV.
		require.Equal(t, uint64(1), smv.VRV.Height)
		require.Zero(t, smv.VRV.Round)
		require.Equal(t, []tmconsensus.ProposedBlock{pb10}, smv.VRV.ProposedBlocks)

		// And it has the jumpahead set.
		j := smv.JumpAheadRoundView
		require.NotNil(t, j)
		require.Equal(t, uint64(1), j.Height)
		require.Equal(t, uint32(1), j.Round)

		// Now the mirror expects the state machine to enter 1/1.
		// If we get more information before then, such as a precommit, nothing new is sent to the state machine.
		pb11VoteMap = map[string][]int{
			"": {1},
		}
		sparsePrecommitProofMap := mfx.Fx.SparsePrecommitProofMap(ctx, 1, 1, pb11VoteMap)
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
		require.Equal(t, uint(3), vrv.PrevoteProofs[""].SignatureBitSet().Count())
		require.Equal(t, uint(1), vrv.PrecommitProofs[""].SignatureBitSet().Count())
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

		// Enqueue a proposed block for the state machine's round, but don't read it from the state machine yet.
		pb10 := mfx.Fx.NextProposedBlock([]byte("ignored"), 0)
		mfx.Fx.SignProposal(ctx, &pb10, 0)
		require.Equal(t, tmconsensus.HandleProposedBlockAccepted, m.HandleProposedBlock(ctx, pb10))

		// Now the mirror sees a minority nil precommit at round 1.
		keyHash, _ := mfx.Fx.ValidatorHashes()
		pb11VoteMap := map[string][]int{
			"": {2, 3},
		}
		sparsePrecommitProofMap := mfx.Fx.SparsePrecommitProofMap(ctx, 1, 1, pb11VoteMap)
		precommitProof := tmconsensus.PrecommitSparseProof{
			Height:     1,
			Round:      1,
			PubKeyHash: keyHash,
			Proofs:     sparsePrecommitProofMap,
		}
		require.Equal(t, tmconsensus.HandleVoteProofsAccepted, m.HandlePrecommitProofs(ctx, precommitProof))

		// Receiving the 1/0 update includes the jump ahead details.
		smv := gtest.ReceiveSoon(t, mfx.StateMachineRoundViewOut)
		require.Equal(t, []tmconsensus.ProposedBlock{pb10}, smv.VRV.ProposedBlocks)

		j := smv.JumpAheadRoundView
		require.NotNil(t, j)
		require.Equal(t, uint64(1), j.Height)
		require.Equal(t, uint32(1), j.Round)
		require.True(t, j.PrevoteProofs[""].SignatureBitSet().None())
		require.Equal(t, uint(2), j.PrecommitProofs[""].SignatureBitSet().Count())
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

	// Proposed block first.
	pb10 := mfx.Fx.NextProposedBlock([]byte("app_data_1_0"), 0)
	mfx.Fx.SignProposal(ctx, &pb10, 0)
	require.Equal(t, tmconsensus.HandleProposedBlockAccepted, m.HandleProposedBlock(ctx, pb10))

	keyHash, _ := mfx.Fx.ValidatorHashes()

	// Then 3/4 prevote for the block, 1/4 for nil.
	pb10Hash := string(pb10.Block.Hash)
	pb10VoteMap := map[string][]int{
		pb10Hash: {0, 1, 2},
		"":       {3},
	}
	sparsePrevoteProofMap := mfx.Fx.SparsePrevoteProofMap(ctx, 1, 0, pb10VoteMap)
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
	require.Equal(t, []tmconsensus.ProposedBlock{pb10}, rer.VRV.ProposedBlocks)

	// Next, the precommit is split 50-50.
	pb10VoteMap = map[string][]int{
		pb10Hash: {0, 1},
		"":       {2, 3},
	}
	sparsePrecommitProofMap := mfx.Fx.SparsePrecommitProofMap(ctx, 1, 0, pb10VoteMap)
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

	// Now there is a new proposed block at 1/1.
	pb11 := mfx.Fx.NextProposedBlock([]byte("app_data_1_1"), 0)
	pb11.Round = 1
	pb11Hash := string(pb11.Block.Hash)
	mfx.Fx.SignProposal(ctx, &pb11, 0)
	require.Equal(t, tmconsensus.HandleProposedBlockAccepted, m.HandleProposedBlock(ctx, pb11))

	// Now the mirror just receives another split precommit.
	pb11VoteMap := map[string][]int{
		pb11Hash: {0, 1},
		"":       {2, 3},
	}
	sparsePrecommitProofMap = mfx.Fx.SparsePrecommitProofMap(ctx, 1, 1, pb11VoteMap)
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
	require.Equal(t, uint(1), smv.PrevoteProofs[""].SignatureBitSet().Count())

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
	require.Equal(t, uint(3), gso.Voting.PrecommitProofs[""].SignatureBitSet().Count())

	// Same thing reported to the state machine.
	smv = gtest.ReceiveSoon(t, mfx.StateMachineRoundViewOut).VRV
	require.Equal(t, uint(3), smv.PrecommitProofs[""].SignatureBitSet().Count())

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
	require.Equal(t, uint(4), gso.NilVotedRound.PrecommitProofs[""].SignatureBitSet().Count())

	// Finally, new information leading to another GossipStrategyOut update
	// will cause the NilVotedRound field to not be set.
	pb11 := mfx.Fx.NextProposedBlock([]byte("app_data_1"), 1)
	pb11.Round = 1
	mfx.Fx.SignProposal(ctx, &pb11, 1)
	require.Equal(t, tmconsensus.HandleProposedBlockAccepted, m.HandleProposedBlock(ctx, pb11))

	gso = gtest.ReceiveSoon(t, mfx.GossipStrategyOut)
	require.NotNil(t, gso.Voting)
	require.Equal(t, uint64(1), gso.Voting.Height)
	require.Equal(t, uint32(1), gso.Voting.Round)
	require.Equal(t, []tmconsensus.ProposedBlock{pb11}, gso.Voting.ProposedBlocks)

	require.Nil(t, gso.NilVotedRound)
}

func TestMirror_gossipStrategyOutStripsEmptyNilVotes(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mfx := tmmirrortest.NewFixture(ctx, t, 4)

	m := mfx.NewMirror()
	defer m.Wait()
	defer cancel()

	// Initial GSO is present but has no prevotes or precommits.
	gso := gtest.ReceiveSoon(t, mfx.GossipStrategyOut)
	require.NotNil(t, gso.Voting)
	require.Equal(t, uint64(1), gso.Voting.Height)
	require.Zero(t, gso.Voting.Round)
	require.Nil(t, gso.Voting.PrevoteProofs[""])
	require.Nil(t, gso.Voting.PrecommitProofs[""])

	prevVotingVersion := gso.Voting.Version

	// Proposed block first.
	pb1 := mfx.Fx.NextProposedBlock([]byte("app_data_1_0"), 0)
	mfx.Fx.SignProposal(ctx, &pb1, 0)
	require.Equal(t, tmconsensus.HandleProposedBlockAccepted, m.HandleProposedBlock(ctx, pb1))

	gso = gtest.ReceiveSoon(t, mfx.GossipStrategyOut)
	require.Greater(t, gso.Voting.Version, prevVotingVersion)
	require.NotEmpty(t, gso.Voting.ProposedBlocks)
	require.Nil(t, gso.Voting.PrevoteProofs[""])
	require.Nil(t, gso.Voting.PrecommitProofs[""])

	// Prevotes arrive from the network.
	voteMap := map[string][]int{
		string(pb1.Block.Hash): {0, 1, 2, 3},
	}
	keyHash, _ := mfx.Fx.ValidatorHashes()
	prevoteProof := tmconsensus.PrevoteSparseProof{
		Height:     1,
		Round:      0,
		PubKeyHash: keyHash,
		Proofs:     mfx.Fx.SparsePrevoteProofMap(ctx, 1, 0, voteMap),
	}
	require.Equal(t, tmconsensus.HandleVoteProofsAccepted, m.HandlePrevoteProofs(ctx, prevoteProof))

	// Version increments and we still don't have any nil votes.
	prevVotingVersion = gso.Voting.Version
	gso = gtest.ReceiveSoon(t, mfx.GossipStrategyOut)
	require.Greater(t, gso.Voting.Version, prevVotingVersion)
	require.NotEmpty(t, gso.Voting.ProposedBlocks)
	require.Nil(t, gso.Voting.PrevoteProofs[""])
	require.Nil(t, gso.Voting.PrecommitProofs[""])

	// 3/4 precommits arrive, and the voting view still doesn't have nil votes present.
	voteMap[string(pb1.Block.Hash)] = []int{0, 1, 2}
	precommitProof := tmconsensus.PrecommitSparseProof{
		Height:     1,
		Round:      0,
		PubKeyHash: keyHash,
		Proofs:     mfx.Fx.SparsePrecommitProofMap(ctx, 1, 0, voteMap),
	}
	require.Equal(t, tmconsensus.HandleVoteProofsAccepted, m.HandlePrecommitProofs(ctx, precommitProof))

	prevVotingVersion = gso.Voting.Version
	gso = gtest.ReceiveSoon(t, mfx.GossipStrategyOut)
	require.Empty(t, gso.Voting.ProposedBlocks)
	require.Nil(t, gso.Voting.PrevoteProofs[""])
	require.Nil(t, gso.Voting.PrecommitProofs[""])

	// Also there is now a committing view, which also doesn't have empty nil proofs.
	require.Greater(t, gso.Committing.Version, prevVotingVersion)
	require.Nil(t, gso.Committing.PrevoteProofs[""])
	require.Nil(t, gso.Committing.PrecommitProofs[""])
	require.Equal(t, uint(3), gso.Committing.PrecommitProofs[string(pb1.Block.Hash)].SignatureBitSet().Count())

	// And if the last precommit arrives, we still don't have any nil proofs on the committing view.
	voteMap[string(pb1.Block.Hash)] = []int{3}
	precommitProof = tmconsensus.PrecommitSparseProof{
		Height:     1,
		Round:      0,
		PubKeyHash: keyHash,
		Proofs:     mfx.Fx.SparsePrecommitProofMap(ctx, 1, 0, voteMap),
	}
	require.Equal(t, tmconsensus.HandleVoteProofsAccepted, m.HandlePrecommitProofs(ctx, precommitProof))

	prevCommittingVersion := gso.Committing.Version
	gso = gtest.ReceiveSoon(t, mfx.GossipStrategyOut)
	require.Greater(t, gso.Committing.Version, prevCommittingVersion)
	require.Nil(t, gso.Committing.PrevoteProofs[""])
	require.Nil(t, gso.Committing.PrecommitProofs[""])
	require.Equal(t, uint(4), gso.Committing.PrecommitProofs[string(pb1.Block.Hash)].SignatureBitSet().Count())
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

	// Proposed block first.
	pb10 := mfx.Fx.NextProposedBlock([]byte("app_data_1_0"), 0)
	mfx.Fx.SignProposal(ctx, &pb10, 0)
	require.Equal(t, tmconsensus.HandleProposedBlockAccepted, m.HandleProposedBlock(ctx, pb10))

	keyHash, _ := mfx.Fx.ValidatorHashes()

	// Then 3/4 prevote for the block, 1/4 for nil.
	pb10Hash := string(pb10.Block.Hash)
	pb10VoteMap := map[string][]int{
		pb10Hash: {0, 1, 2},
		"":       {3},
	}
	prevoteProof := tmconsensus.PrevoteSparseProof{
		Height:     1,
		Round:      0,
		PubKeyHash: keyHash,
		Proofs:     mfx.Fx.SparsePrevoteProofMap(ctx, 1, 0, pb10VoteMap),
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
		Proofs:     mfx.Fx.SparsePrecommitProofMap(ctx, 1, 0, pb10VoteMap),
	}
	require.Equal(t, tmconsensus.HandleVoteProofsAccepted, m.HandlePrecommitProofs(ctx, precommitProof))

	// That pushes voting to 2/0 and committing to 1/0.
	ms = gtest.ReceiveSoon(t, mCh)
	require.Equal(t, uint64(2), ms.MirrorVotingHeight)
	require.Zero(t, ms.MirrorVotingRound)
	require.Equal(t, uint64(1), ms.MirrorCommittingHeight)
	require.Zero(t, ms.MirrorCommittingRound)

	// Now if we get a nil precommit at 2/0, metrics show 2/1.
	pb20VoteMap := map[string][]int{
		"": {0, 1, 2, 3},
	}
	precommitProof = tmconsensus.PrecommitSparseProof{
		Height:     2,
		Round:      0,
		PubKeyHash: keyHash,
		Proofs:     mfx.Fx.SparsePrecommitProofMap(ctx, 2, 0, pb20VoteMap),
	}
	require.Equal(t, tmconsensus.HandleVoteProofsAccepted, m.HandlePrecommitProofs(ctx, precommitProof))

	ms = gtest.ReceiveSoon(t, mCh)
	require.Equal(t, uint64(2), ms.MirrorVotingHeight)
	require.Equal(t, uint32(1), ms.MirrorVotingRound)
	require.Equal(t, uint64(1), ms.MirrorCommittingHeight)
	require.Zero(t, ms.MirrorCommittingRound)
}
