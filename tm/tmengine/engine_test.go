package tmengine_test

import (
	"bytes"
	"context"
	"sort"
	"testing"

	"github.com/rollchains/gordian/gwatchdog"
	"github.com/rollchains/gordian/internal/gtest"
	"github.com/rollchains/gordian/tm/tmconsensus"
	"github.com/rollchains/gordian/tm/tmconsensus/tmconsensustest"
	"github.com/rollchains/gordian/tm/tmdriver"
	"github.com/rollchains/gordian/tm/tmengine"
	"github.com/rollchains/gordian/tm/tmengine/internal/tmstate/tmstatetest"
	"github.com/rollchains/gordian/tm/tmengine/tmenginetest"
	"github.com/rollchains/gordian/tm/tmgossip/tmgossiptest"
	"github.com/rollchains/gordian/tm/tmstore/tmmemstore"
	"github.com/stretchr/testify/require"
)

func TestEngine_plumbing_ConsensusStrategy(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	efx := tmenginetest.NewFixture(ctx, t, 4)

	var engine *tmengine.Engine
	eReady := make(chan struct{})
	go func() {
		defer close(eReady)
		engine = efx.MustNewEngine(efx.SigningOptionMap().ToSlice()...)
	}()

	defer func() {
		cancel()
		<-eReady
		engine.Wait()
	}()

	// Set up the EnterRound expectation before any other actions,
	// to avoid it happening early and causing a panic.
	cs := efx.ConsensusStrategy
	ercCh := cs.ExpectEnterRound(1, 0, nil)

	// It makes an init chain request.
	icReq := gtest.ReceiveSoon(t, efx.InitChainCh)

	const initAppStateHash = "app_state_0"

	gtest.SendSoon(t, icReq.Resp, tmdriver.InitChainResponse{
		AppStateHash: []byte(initAppStateHash),
	})

	// After we send the response, the engine is ready.
	_ = gtest.ReceiveSoon(t, eReady)

	// Grouping assertions into subtests for a little more clarity in this relatively long test.

	erc := gtest.ReceiveSoon(t, ercCh)

	t.Run("EnterRound", func(t *testing.T) {
		// Now, the mirror and state machine should both believe we are on 1/0.
		// This should result in an EnterRound call to the consensus strategy.
		rv0 := erc.RV
		require.Equal(t, uint64(1), rv0.Height)
		require.Zero(t, rv0.Round)

		require.True(t, tmconsensus.ValidatorSlicesEqual(rv0.Validators, efx.Fx.Vals()))

		require.NotEmpty(t, rv0.ValidatorPubKeyHash)
		require.NotEmpty(t, rv0.ValidatorVotePowerHash)

		require.Empty(t, rv0.ProposedBlocks)

		// The proofs always have an empty nil proof, for some reason internal to the mirror.
		require.Len(t, rv0.PrevoteProofs, 1)
		require.NotNil(t, rv0.PrevoteProofs[""])
		require.Len(t, rv0.PrecommitProofs, 1)
		require.NotNil(t, rv0.PrecommitProofs[""])

		var expAvailPow uint64
		for _, v := range efx.Fx.Vals() {
			expAvailPow += v.Power
		}
		require.Equal(t, expAvailPow, rv0.VoteSummary.AvailablePower)
		require.Zero(t, rv0.VoteSummary.TotalPrevotePower)
		require.Zero(t, rv0.VoteSummary.TotalPrecommitPower)
	})

	// Shared across subsequent subtests.
	var pb tmconsensus.ProposedBlock

	t.Run("state machine proposes a block", func(t *testing.T) {
		require.Equal(t, 1, cap(erc.ProposalOut))

		// Drain the voting view before proposing a block.
		_ = gtest.ReceiveSoon(t, efx.GossipStrategy.Updates)

		// Application proposes a data hash.
		erc.ProposalOut <- tmconsensus.Proposal{
			DataID: "app_data_1",
		}

		// This causes a voting view update to be sent to the gossip strategy.
		vrv := gtest.ReceiveSoon(t, efx.GossipStrategy.Updates).Voting

		require.Len(t, vrv.ProposedBlocks, 1)
		pb = vrv.ProposedBlocks[0]
		require.Equal(t, "app_data_1", string(pb.Block.DataID))

		// The proposed block is not identical to the fixture's next proposed block.
		// Arguably that is a bug in the fixture,
		// but the actual engine sets the PrevBlockHash to match the genesis pseudo-block
		// and it has a PrevAppStateHash.
		expPB := efx.Fx.NextProposedBlock([]byte("app_data_1"), 0)
		expPB.Block.PrevAppStateHash = []byte(initAppStateHash)

		g := tmconsensus.Genesis{
			ChainID:             "my-chain", // NOTE: this is hard-coded to the fixture's default chain ID.
			InitialHeight:       1,
			CurrentAppStateHash: []byte(initAppStateHash),
			Validators:          efx.Fx.Vals(),
		}

		gBlock, err := g.Block(efx.Fx.HashScheme)
		require.NoError(t, err)
		expPB.Block.PrevBlockHash = gBlock.Hash

		efx.Fx.RecalculateHash(&expPB.Block)

		efx.Fx.SignProposal(ctx, &expPB, 0)
		require.Equal(t, expPB, pb)
	})

	// Used by subsequent subtests.
	blockHash := string(pb.Block.Hash)
	var vrv tmconsensus.VersionedRoundView

	t.Run("mirror presents proposed block back to state machine", func(t *testing.T) {
		// Once the state machine has sent the proposed block to the mirror,
		// the mirror enqueues a view update presented to the state machine.
		// That update will include the new proposed block.
		// When the state machine receives that new proposed block,
		// it calls the consensus strategy's ConsiderProposedBlocks method.
		cReq := gtest.ReceiveSoon(t, efx.ConsensusStrategy.ConsiderProposedBlocksRequests)
		require.Equal(t, []tmconsensus.ProposedBlock{pb}, cReq.PBs)
		require.Equal(t, []string{string(pb.Block.Hash)}, cReq.Reason.NewProposedBlocks)

		// The state machine votes for the block it proposed.
		cReq.ChoiceHash <- blockHash

		// The mirror sends an updated voting view to the gossip strategy.
		vrv = *(gtest.ReceiveSoon(t, efx.GossipStrategy.Updates).Voting)
		require.Len(t, vrv.PrevoteProofs, 1) // Nil prevote automatically set, plus prevoted block.
		proof := vrv.PrevoteProofs[blockHash]
		require.Equal(t, uint(1), proof.SignatureBitSet().Count())
		require.True(t, proof.SignatureBitSet().Test(0))
	})

	t.Run("state machine decides precommit when rest of prevotes arrive", func(t *testing.T) {
		fullPrevotes := efx.Fx.SparsePrevoteProofMap(ctx, 1, 0, map[string][]int{
			blockHash: {0, 1, 2, 3},
		})
		prevoteSparseProof := tmconsensus.PrevoteSparseProof{
			Height: 1, Round: 0,
			PubKeyHash: vrv.ValidatorPubKeyHash,
			Proofs:     fullPrevotes,
		}
		require.Equal(t, tmconsensus.HandleVoteProofsAccepted, engine.HandlePrevoteProofs(ctx, prevoteSparseProof))

		// Drain the voting view sent to the gossip strategy, because we have another assertion later in this test.
		_ = gtest.ReceiveSoon(t, efx.GossipStrategy.Updates)

		precommitReq := gtest.ReceiveSoon(t, efx.ConsensusStrategy.DecidePrecommitRequests)
		vs := precommitReq.Input
		require.NotZero(t, vs.AvailablePower)
		require.Equal(t, vs.AvailablePower, vs.TotalPrevotePower)
		require.Equal(t, vs.TotalPrevotePower, vs.PrevoteBlockPower[blockHash])

		// The consensus strategy, via the state machine, submits a precommit in favor of the block.
		precommitReq.ChoiceHash <- blockHash

		// The mirror sends an updated voting view to the gossip strategy.
		vrv = *(gtest.ReceiveSoon(t, efx.GossipStrategy.Updates).Voting)

		// It still has the proposed block and prevotes.
		require.Len(t, vrv.ProposedBlocks, 1)
		require.Len(t, vrv.PrevoteProofs, 1)
		require.Equal(t, uint(4), vrv.PrevoteProofs[blockHash].SignatureBitSet().Count())

		// And it has the new single precommit.
		require.Len(t, vrv.PrecommitProofs, 1)
		bs := vrv.PrecommitProofs[blockHash].SignatureBitSet()
		require.Equal(t, uint(1), bs.Count())
		require.True(t, bs.Test(0))
	})

	t.Run("state machine finalizes block when rest of precommits arrive", func(t *testing.T) {
		fullPrecommits := efx.Fx.SparsePrecommitProofMap(ctx, 1, 0, map[string][]int{
			blockHash: {0, 1, 2, 3},
		})
		precommitSparseProof := tmconsensus.PrecommitSparseProof{
			Height: 1, Round: 0,
			PubKeyHash: vrv.ValidatorPubKeyHash,
			Proofs:     fullPrecommits,
		}
		require.Equal(t, tmconsensus.HandleVoteProofsAccepted, engine.HandlePrecommitProofs(ctx, precommitSparseProof))

		prevVersion := vrv.Version

		// Since that was a full precommit, now the voting view is the next height.
		gsu := gtest.ReceiveSoon(t, efx.GossipStrategy.Updates)
		newVotingVRV := gsu.Voting
		require.Equal(t, uint64(2), newVotingVRV.Height)

		// The committing view retains and increases the previous version.
		committingVRV := gsu.Committing
		require.Equal(t, uint64(1), committingVRV.Height)
		require.Zero(t, committingVRV.Round)
		require.Greater(t, committingVRV.Version, prevVersion)

		// And the state machine received the committing view, causing it to make a finalize block request.
		finReq := gtest.ReceiveSoon(t, efx.FinalizeBlockRequests)
		require.Equal(t, pb.Block, finReq.Block)
		require.Zero(t, finReq.Round)

		// By the time the state machine received a finalize block request,
		// the round timer should be in commit wait.
		efx.RoundTimer.RequireActiveCommitWaitTimer(t, 1, 0)

		// Under normal circumstances, the finalize request will complete before the timeout.
		gtest.SendSoon(t, finReq.Resp, tmdriver.FinalizeBlockResponse{
			Height: 1, Round: 0,
			BlockHash: pb.Block.Hash,

			// Validators unchanged.
			Validators: efx.Fx.Vals(),

			AppStateHash: []byte("app_state_1"),
		})

		// We are going to propose a block at height 2 later,
		// so we need to commit the block at height 1 within the fixture.
		efx.Fx.CommitBlock(
			committingVRV.ProposedBlocks[0].Block,
			[]byte("app_state_1"),
			0,
			committingVRV.PrecommitProofs,
		)

		// After the commit wait timeout elapses, the state machine should enter Height 2,
		// so we need to set up an EnterRound expectation first.
		ercCh := cs.ExpectEnterRound(2, 0, nil)

		// And let's synchronize on the proposal timer being active, for the next subtest.
		startedCh := efx.RoundTimer.ProposalStartNotification(2, 0)

		require.NoError(t, efx.RoundTimer.ElapseCommitWaitTimer(1, 0))

		enterRoundReq := gtest.ReceiveSoon(t, ercCh)
		require.Equal(t, uint64(2), enterRoundReq.RV.Height)
		require.Zero(t, enterRoundReq.RV.Round)

		// Synchronize on the proposal timer started so the next subtest works.
		gtest.ReceiveSoon(t, startedCh)
	})

	t.Run("proposal timeout on the new height", func(t *testing.T) {
		// After that timer elapses, the consensus strategy must choose a proposed block
		// (of which there are none).
		require.NoError(t, efx.RoundTimer.ElapseProposalTimer(2, 0))

		cReq := gtest.ReceiveSoon(t, cs.ChooseProposedBlockRequests)
		require.Empty(t, cReq.Input)

		// Precommit nil.
		gtest.SendSoon(t, cReq.ChoiceHash, "")

		// The mirror sends an updated voting view to the gossip strategy.
		vrv = *(gtest.ReceiveSoon(t, efx.GossipStrategy.Updates).Voting)

		fullPrevotes := efx.Fx.SparsePrevoteProofMap(ctx, 2, 0, map[string][]int{
			"": {0, 1, 2, 3},
		})
		prevoteSparseProof := tmconsensus.PrevoteSparseProof{
			Height: 2, Round: 0,
			PubKeyHash: vrv.ValidatorPubKeyHash,
			Proofs:     fullPrevotes,
		}
		require.Equal(t, tmconsensus.HandleVoteProofsAccepted, engine.HandlePrevoteProofs(ctx, prevoteSparseProof))

		// With full prevotes having arrived, the state machine requests a precommit decision.
		precommitReq := gtest.ReceiveSoon(t, efx.ConsensusStrategy.DecidePrecommitRequests)
		vs := precommitReq.Input
		require.NotZero(t, vs.AvailablePower)
		require.Equal(t, vs.AvailablePower, vs.TotalPrevotePower)
		require.Equal(t, vs.TotalPrevotePower, vs.PrevoteBlockPower[""])

		// Our consensus strategy precommits nil, which updates the voting view again.
		precommitReq.ChoiceHash <- ""
		vrv = *(gtest.ReceiveSoon(t, efx.GossipStrategy.Updates).Voting)

		// Then the rest of the validators precommit nil.
		// This will cause a transition to the next round, so set that expectation first.
		ercCh := cs.ExpectEnterRound(2, 1, nil)
		fullPrecommits := efx.Fx.SparsePrecommitProofMap(ctx, 2, 0, map[string][]int{
			"": {0, 1, 2, 3},
		})
		precommitSparseProof := tmconsensus.PrecommitSparseProof{
			Height: 2, Round: 0,
			PubKeyHash: vrv.ValidatorPubKeyHash,
			Proofs:     fullPrecommits,
		}
		require.Equal(t, tmconsensus.HandleVoteProofsAccepted, engine.HandlePrecommitProofs(ctx, precommitSparseProof))

		// The enter round call has the correct height and round.
		erReq := gtest.ReceiveSoon(t, ercCh)
		require.Equal(t, uint64(2), erReq.RV.Height)
		require.Equal(t, uint32(1), erReq.RV.Round)
	})

	t.Run("chain advances properly from round 1 on a height", func(t *testing.T) {
		pb21 := efx.Fx.NextProposedBlock([]byte("app_data_2_1"), 1)
		pb21.Round = 1
		efx.Fx.SignProposal(ctx, &pb21, 1)
		require.Equal(t, tmconsensus.HandleProposedBlockAccepted, engine.HandleProposedBlock(ctx, pb21))

		cpbReq := gtest.ReceiveSoon(t, cs.ConsiderProposedBlocksRequests)
		require.Equal(t, []tmconsensus.ProposedBlock{pb21}, cpbReq.PBs)
		require.Equal(t, []string{string(pb21.Block.Hash)}, cpbReq.Reason.NewProposedBlocks)

		// There is technically a data race on asserting this timer active
		// immediately after making the enter round call.
		// But it certainly must be active by the time a call to ConsiderProposedBlocks happens.
		efx.RoundTimer.RequireActiveProposalTimer(t, 2, 1)

		// Sending the precommit choice updates the voting view to the gossip strategy.
		_ = gtest.ReceiveSoon(t, efx.GossipStrategy.Updates)

		blockHash := string(pb21.Block.Hash)
		gtest.SendSoon(t, cpbReq.ChoiceHash, blockHash)
		_ = gtest.ReceiveSoon(t, efx.GossipStrategy.Updates)

		// The rest of the prevotes arrive.
		fullPrevotes := efx.Fx.SparsePrevoteProofMap(ctx, 2, 1, map[string][]int{
			blockHash: {0, 1, 2, 3},
		})
		prevoteSparseProof := tmconsensus.PrevoteSparseProof{
			Height: 2, Round: 1,
			PubKeyHash: vrv.ValidatorPubKeyHash,
			Proofs:     fullPrevotes,
		}
		require.Equal(t, tmconsensus.HandleVoteProofsAccepted, engine.HandlePrevoteProofs(ctx, prevoteSparseProof))
		_ = gtest.ReceiveSoon(t, efx.GossipStrategy.Updates)

		// This causes a precommit decision.
		precommitReq := gtest.ReceiveSoon(t, cs.DecidePrecommitRequests)
		gtest.SendSoon(t, precommitReq.ChoiceHash, blockHash)
		_ = gtest.ReceiveSoon(t, efx.GossipStrategy.Updates)

		// Then if all the precommits arrive in favor of the block, we finalize 2/1.

		fullPrecommits := efx.Fx.SparsePrecommitProofMap(ctx, 2, 1, map[string][]int{
			blockHash: {0, 1, 2, 3},
		})
		precommitSparseProof := tmconsensus.PrecommitSparseProof{
			Height: 2, Round: 1,
			PubKeyHash: vrv.ValidatorPubKeyHash,
			Proofs:     fullPrecommits,
		}
		require.Equal(t, tmconsensus.HandleVoteProofsAccepted, engine.HandlePrecommitProofs(ctx, precommitSparseProof))

		finReq := gtest.ReceiveSoon(t, efx.FinalizeBlockRequests)
		require.Equal(t, pb21.Block, finReq.Block)
		require.Equal(t, uint32(1), finReq.Round)

		// The commit wait timer elapses before finalization.
		require.NoError(t, efx.RoundTimer.ElapseCommitWaitTimer(2, 1))

		// Expect enter round before we send the finalization response.
		ercCh := cs.ExpectEnterRound(3, 0, nil)

		finReq.Resp <- tmdriver.FinalizeBlockResponse{
			Height: 2, Round: 1,
			BlockHash: pb21.Block.Hash,

			Validators: efx.Fx.Vals(),

			AppStateHash: []byte("app_state_2_1"),
		}

		erc := gtest.ReceiveSoon(t, ercCh)
		require.Equal(t, uint64(3), erc.RV.Height)
		require.Zero(t, erc.RV.Round)
	})
}

func TestEngine_plumbing_GossipStrategy(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	efx := tmenginetest.NewFixture(ctx, t, 4)

	var engine *tmengine.Engine
	eReady := make(chan struct{})
	go func() {
		defer close(eReady)
		engine = efx.MustNewEngine(efx.SigningOptionMap().ToSlice()...)
	}()

	defer func() {
		cancel()
		<-eReady
		engine.Wait()
	}()

	cs := efx.ConsensusStrategy
	ercCh := cs.ExpectEnterRound(1, 0, nil)

	// It makes an init chain request.
	icReq := gtest.ReceiveSoon(t, efx.InitChainCh)

	const initAppStateHash = "app_state_0"

	gtest.SendSoon(t, icReq.Resp, tmdriver.InitChainResponse{
		AppStateHash: []byte(initAppStateHash),
	})

	// After we send the response, the engine is ready.
	_ = gtest.ReceiveSoon(t, eReady)

	erc := gtest.ReceiveSoon(t, ercCh)

	gs := efx.GossipStrategy
	_ = gtest.ReceiveSoon(t, gs.Ready)

	t.Run("initial output state", func(t *testing.T) {
		u := gtest.ReceiveSoon(t, gs.Updates)

		require.Nil(t, u.Committing)

		require.Equal(t, uint64(1), u.Voting.Height)
		require.Zero(t, u.Voting.Round)
		require.Empty(t, u.Voting.ProposedBlocks)

		require.Equal(t, uint64(1), u.NextRound.Height)
		require.Equal(t, uint32(1), u.NextRound.Round)
		require.Empty(t, u.NextRound.ProposedBlocks)
	})

	pb103 := efx.Fx.NextProposedBlock([]byte("app_data_1_0_3"), 3)
	pb103.Block.PrevAppStateHash = []byte(initAppStateHash)
	efx.Fx.RecalculateHash(&pb103.Block)
	efx.Fx.SignProposal(ctx, &pb103, 3)
	t.Run("proposed block from network", func(t *testing.T) {
		require.Equal(t, tmconsensus.HandleProposedBlockAccepted, engine.HandleProposedBlock(ctx, pb103))

		u := gtest.ReceiveSoon(t, gs.Updates)

		require.Equal(t, uint64(1), u.Voting.Height)
		require.Zero(t, u.Voting.Round)
		require.Equal(t, []tmconsensus.ProposedBlock{pb103}, u.Voting.ProposedBlocks)

		require.Nil(t, u.Committing)
		require.Nil(t, u.NextRound)

		cReq := gtest.ReceiveSoon(t, cs.ConsiderProposedBlocksRequests)
		require.Equal(t, []string{string(pb103.Block.Hash)}, cReq.Reason.NewProposedBlocks)
		gtest.SendSoon(t, cReq.ChoiceError, tmconsensus.ErrProposedBlockChoiceNotReady)
	})

	var pb100 tmconsensus.ProposedBlock
	var blockHash100 string
	t.Run("proposed block from state machine", func(t *testing.T) {
		gtest.SendSoon(t, erc.ProposalOut, tmconsensus.Proposal{
			DataID: "app_data_1_0_0",
		})

		// The proposed blocks that arrive from the state machine
		// are constructed slightly differently from the fixture --
		// the fixture omits the previous block hash and previous app state hash,
		// at genesis at least.
		// So load the proposed block from the state machine's action store
		// to confirm it matches with what the gossip strategy receives.

		u := gtest.ReceiveSoon(t, gs.Updates)

		ra, err := efx.ActionStore.Load(ctx, 1, 0)
		require.NoError(t, err)
		pb100 = ra.ProposedBlock
		blockHash100 = string(pb100.Block.Hash)
		require.Equal(t, uint64(1), pb100.Block.Height)
		require.Zero(t, pb100.Round)
		require.Equal(t, "app_data_1_0_0", string(pb100.Block.DataID))

		require.Equal(t, uint64(1), u.Voting.Height)
		require.Zero(t, u.Voting.Round)
		require.Equal(t, []tmconsensus.ProposedBlock{pb103, pb100}, u.Voting.ProposedBlocks)

		require.Nil(t, u.Committing)
		require.Nil(t, u.NextRound)
	})

	// From here the network is going to settle on the 1_0_3 block, not the 1_0_0.
	blockHash103 := string(pb103.Block.Hash)
	pubKeyHash, _ := efx.Fx.ValidatorHashes()
	t.Run("prevote from network", func(t *testing.T) {
		only3Prevote := efx.Fx.SparsePrevoteProofMap(ctx, 1, 0, map[string][]int{
			blockHash103: {3},
		})
		prevoteSparseProof := tmconsensus.PrevoteSparseProof{
			Height: 1, Round: 0,
			PubKeyHash: pubKeyHash,
			Proofs:     only3Prevote,
		}
		require.Equal(t, tmconsensus.HandleVoteProofsAccepted, engine.HandlePrevoteProofs(ctx, prevoteSparseProof))

		u := gtest.ReceiveSoon(t, gs.Updates)

		require.Equal(t, uint64(1), u.Voting.Height)
		require.Zero(t, u.Voting.Round)
		require.Equal(t, []tmconsensus.ProposedBlock{pb103, pb100}, u.Voting.ProposedBlocks)

		proof103 := u.Voting.PrevoteProofs[blockHash103]
		require.Equal(t, uint(1), proof103.SignatureBitSet().Count())
		require.True(t, proof103.SignatureBitSet().Test(3))
		require.Equal(t, efx.Fx.Vals()[3].Power, u.Voting.VoteSummary.TotalPrevotePower)

		require.Nil(t, u.Committing)
		require.Nil(t, u.NextRound)
	})

	t.Run("prevote from state machine", func(t *testing.T) {
		cReq := gtest.ReceiveSoon(t, cs.ConsiderProposedBlocksRequests)
		require.Equal(t, []string{blockHash100}, cReq.Reason.NewProposedBlocks)
		gtest.SendSoon(t, cReq.ChoiceHash, blockHash100)

		u := gtest.ReceiveSoon(t, gs.Updates)

		require.Equal(t, uint64(1), u.Voting.Height)
		require.Zero(t, u.Voting.Round)

		proof103 := u.Voting.PrevoteProofs[blockHash103]
		require.Equal(t, uint(1), proof103.SignatureBitSet().Count())

		proof100 := u.Voting.PrevoteProofs[blockHash100]
		require.True(t, proof100.SignatureBitSet().Test(0))
		require.Equal(t, efx.Fx.Vals()[3].Power+efx.Fx.Vals()[0].Power, u.Voting.VoteSummary.TotalPrevotePower)

		require.Nil(t, u.Committing)
		require.Nil(t, u.NextRound)
	})

	t.Run("rest of prevotes arrive and state machine submits precommit", func(t *testing.T) {
		remainingPrevotes := efx.Fx.SparsePrevoteProofMap(ctx, 1, 0, map[string][]int{
			blockHash103: {1, 2},
		})
		prevoteSparseProof := tmconsensus.PrevoteSparseProof{
			Height: 1, Round: 0,
			PubKeyHash: pubKeyHash,
			Proofs:     remainingPrevotes,
		}
		require.Equal(t, tmconsensus.HandleVoteProofsAccepted, engine.HandlePrevoteProofs(ctx, prevoteSparseProof))

		// Everyone else voted for 103, so the state machine precommits it here too.
		_ = gtest.ReceiveSoon(t, gs.Updates) // Drain before state machine precommit.
		req := gtest.ReceiveSoon(t, cs.DecidePrecommitRequests)
		gtest.SendSoon(t, req.ChoiceHash, blockHash103)

		// And one more precommit arrives, so that we don't have to synchonize on the state machine precommit.
		_ = gtest.ReceiveSoon(t, gs.Updates) // Drain the updates first so we can be sure the next read is up to date.
		precommit3 := efx.Fx.SparsePrecommitProofMap(ctx, 1, 0, map[string][]int{
			blockHash103: {3},
		})
		precommitSparseProof := tmconsensus.PrecommitSparseProof{
			Height: 1, Round: 0,
			PubKeyHash: pubKeyHash,
			Proofs:     precommit3,
		}
		require.Equal(t, tmconsensus.HandleVoteProofsAccepted, engine.HandlePrecommitProofs(ctx, precommitSparseProof))

		u := gtest.ReceiveSoon(t, gs.Updates)

		require.Equal(t, uint64(1), u.Voting.Height)
		require.Zero(t, u.Voting.Round)

		prevote103 := u.Voting.PrevoteProofs[blockHash103]
		require.Equal(t, uint(3), prevote103.SignatureBitSet().Count())

		precommit103 := u.Voting.PrecommitProofs[blockHash103]
		require.Equal(t, uint(2), precommit103.SignatureBitSet().Count())
		require.True(t, precommit103.SignatureBitSet().Test(0))
		require.True(t, precommit103.SignatureBitSet().Test(3))
		require.Equal(t, efx.Fx.Vals()[3].Power+efx.Fx.Vals()[0].Power, u.Voting.VoteSummary.TotalPrecommitPower)
	})

	t.Run("committing view is updated once remaining precommits arrive", func(t *testing.T) {
		precommit3 := efx.Fx.SparsePrecommitProofMap(ctx, 1, 0, map[string][]int{
			blockHash103: {1, 2},
		})
		precommitSparseProof := tmconsensus.PrecommitSparseProof{
			Height: 1, Round: 0,
			PubKeyHash: pubKeyHash,
			Proofs:     precommit3,
		}
		require.Equal(t, tmconsensus.HandleVoteProofsAccepted, engine.HandlePrecommitProofs(ctx, precommitSparseProof))

		u := gtest.ReceiveSoon(t, gs.Updates)

		require.Equal(t, uint64(2), u.Voting.Height)
		require.Zero(t, u.Voting.Round)

		require.NotNil(t, u.Committing)
		require.Equal(t, uint64(1), u.Committing.Height)
		require.Zero(t, u.Committing.Round)

		require.NotNil(t, u.NextRound)
		require.Equal(t, uint64(2), u.NextRound.Height)
		require.Equal(t, uint32(1), u.NextRound.Round)
	})
}

func TestEngine_initChain(t *testing.T) {
	t.Run("default startup flow requiring InitChain call, no validator override", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		efx := tmenginetest.NewFixture(ctx, t, 2)

		// NewEngine blocks in the main goroutine on the init chain call,
		// so we have to create it in the background,
		// and we use a channel to synchronize access,
		// particularly for the Wait call at the end of the test.
		var engine *tmengine.Engine
		eReady := make(chan struct{})
		go func() {
			defer close(eReady)
			engine = efx.MustNewEngine(efx.SigningOptionMap().ToSlice()...)
		}()

		defer func() {
			cancel()
			<-eReady
			engine.Wait()
		}()

		// We may or may not reach EnterRound as this test finishes,
		// so we need to set an expectation on the mock consensus strategy.
		_ = efx.ConsensusStrategy.ExpectEnterRound(1, 0, nil)

		// It makes an init chain request.
		icReq := gtest.ReceiveSoon(t, efx.InitChainCh)

		// The NewEngine call still hasn't returned before we respond.
		gtest.NotSending(t, eReady)

		gtest.SendSoon(t, icReq.Resp, tmdriver.InitChainResponse{
			AppStateHash: []byte("app_state_0"),
		})

		// After we send the response, the engine is ready.
		_ = gtest.ReceiveSoon(t, eReady)

		// And this means the finalization store is populated.
		round, _, vals, appStateHash, err := efx.FinalizationStore.LoadFinalizationByHeight(ctx, 0)
		require.NoError(t, err)
		require.Zero(t, round)
		require.True(t, tmconsensus.ValidatorSlicesEqual(vals, efx.Fx.Vals()))
		require.Equal(t, "app_state_0", appStateHash)
	})

	t.Run("default startup flow requiring InitChain call, with validator override", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		efx := tmenginetest.NewFixture(ctx, t, 2)

		// NewEngine blocks in the main goroutine on the init chain call,
		// so we have to create it in the background,
		// and we use a channel to synchronize access,
		// particularly for the Wait call at the end of the test.
		var engine *tmengine.Engine
		eReady := make(chan struct{})
		go func() {
			defer close(eReady)
			engine = efx.MustNewEngine(efx.SigningOptionMap().ToSlice()...)
		}()

		defer func() {
			cancel()
			<-eReady
			engine.Wait()
		}()

		// We may or may not reach EnterRound as this test finishes,
		// so we need to set an expectation on the mock consensus strategy.
		_ = efx.ConsensusStrategy.ExpectEnterRound(1, 0, nil)

		// It makes an init chain request.
		icReq := gtest.ReceiveSoon(t, efx.InitChainCh)

		// The NewEngine call still hasn't returned before we respond.
		gtest.NotSending(t, eReady)

		newVals := tmconsensustest.DeterministicValidatorsEd25519(3).Vals()

		gtest.SendSoon(t, icReq.Resp, tmdriver.InitChainResponse{
			AppStateHash: []byte("app_state_0"),
			Validators:   newVals,
		})

		// After we send the response, the engine is ready.
		_ = gtest.ReceiveSoon(t, eReady)

		// And this means the finalization store is populated.
		round, _, vals, appStateHash, err := efx.FinalizationStore.LoadFinalizationByHeight(ctx, 0)
		require.NoError(t, err)
		require.Zero(t, round)
		require.True(t, tmconsensus.ValidatorSlicesEqual(vals, newVals))
		require.Equal(t, "app_state_0", appStateHash)
	})

	t.Run("default startup flow requiring InitChain call, with no initial validators but with a validator override", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		efx := tmenginetest.NewFixture(ctx, t, 2)

		var engine *tmengine.Engine
		eReady := make(chan struct{})
		go func() {
			defer close(eReady)
			// Overwrite the WithGenesis option so that it has no GenesisValidators specified.
			optMap := efx.SigningOptionMap()
			optMap["WithGenesis"] = tmengine.WithGenesis(&tmconsensus.ExternalGenesis{
				ChainID:           "my-chain",
				InitialHeight:     1,
				InitialAppState:   new(bytes.Buffer),
				GenesisValidators: nil, // Explicitly nil initial validators.
			})
			engine = efx.MustNewEngine(optMap.ToSlice()...)
		}()

		defer func() {
			cancel()
			<-eReady
			engine.Wait()
		}()

		// We may or may not reach EnterRound as this test finishes,
		// so we need to set an expectation on the mock consensus strategy.
		_ = efx.ConsensusStrategy.ExpectEnterRound(1, 0, nil)

		// It makes an init chain request.
		icReq := gtest.ReceiveSoon(t, efx.InitChainCh)

		// The NewEngine call still hasn't returned before we respond.
		gtest.NotSending(t, eReady)

		newVals := tmconsensustest.DeterministicValidatorsEd25519(3).Vals()

		gtest.SendSoon(t, icReq.Resp, tmdriver.InitChainResponse{
			AppStateHash: []byte("app_state_0"),
			Validators:   newVals,
		})

		// After we send the response, the engine is ready.
		_ = gtest.ReceiveSoon(t, eReady)

		// And this means the finalization store is populated.
		round, _, vals, appStateHash, err := efx.FinalizationStore.LoadFinalizationByHeight(ctx, 0)
		require.NoError(t, err)
		require.Zero(t, round)
		require.True(t, tmconsensus.ValidatorSlicesEqual(vals, newVals))
		require.Equal(t, "app_state_0", appStateHash)
	})

	t.Run("no init chain call when finalization already exists", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		efx := tmenginetest.NewFixture(ctx, t, 2)

		// Before starting the engine, save a finalization.
		require.NoError(t, efx.FinalizationStore.SaveFinalization(
			ctx,
			0, 0, // 0-height because it must be 1 less than initial height.
			"a_block_hash",
			efx.Fx.Vals(),
			"app_state_hash",
		))

		// Still making the engine on a background goroutine,
		// to avoid halting the test in the event of a failure.
		var engine *tmengine.Engine
		eReady := make(chan struct{})
		go func() {
			defer close(eReady)
			engine = efx.MustNewEngine(efx.SigningOptionMap().ToSlice()...)
		}()

		defer func() {
			cancel()
			<-eReady
			engine.Wait()
		}()

		// We may or may not reach EnterRound as this test finishes,
		// so we need to set an expectation on the mock consensus strategy.
		_ = efx.ConsensusStrategy.ExpectEnterRound(1, 0, nil)

		// The engine is ready more or less immediately.
		_ = gtest.ReceiveSoon(t, eReady)

		// And there is no init chain request.
		gtest.NotSending(t, efx.InitChainCh)

		require.NotNil(t, engine)
	})

	t.Run("no init chain call when network height-round is set", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		efx := tmenginetest.NewFixture(ctx, t, 2)

		// Before starting the engine, set the network height-round.
		// This will cause the engine to not consult the finalization store.
		require.NoError(t, efx.MirrorStore.SetNetworkHeightRound(ctx, 1, 0, 0, 0))

		// Still making the engine on a background goroutine,
		// to avoid halting the test in the event of a failure.
		var engine *tmengine.Engine
		eReady := make(chan struct{})
		go func() {
			defer close(eReady)
			engine = efx.MustNewEngine(efx.SigningOptionMap().ToSlice()...)
		}()

		defer func() {
			cancel()
			<-eReady
			engine.Wait()
		}()

		// We may or may not reach EnterRound as this test finishes,
		// so we need to set an expectation on the mock consensus strategy.
		_ = efx.ConsensusStrategy.ExpectEnterRound(1, 0, nil)

		// The engine is ready more or less immediately.
		_ = gtest.ReceiveSoon(t, eReady)

		// And there is no init chain request.
		gtest.NotSending(t, efx.InitChainCh)

		require.NotNil(t, engine)
	})
}

func TestEngine_configuration(t *testing.T) {
	t.Parallel()

	fx := tmconsensustest.NewStandardFixture(1)
	as := fx.NewMemActionStore()
	fs := tmmemstore.NewFinalizationStore()
	ms := tmmemstore.NewMirrorStore()
	rs := tmmemstore.NewRoundStore()
	vs := fx.NewMemValidatorStore()

	// Set a network height-round to trick the engine into thinking
	// the chain has already been initialized.
	require.NoError(t, ms.SetNetworkHeightRound(context.Background(), 1, 0, 0, 0))

	cStrat := tmconsensustest.NewMockConsensusStrategy()

	eg := &tmconsensus.ExternalGenesis{
		ChainID:           "my-chain",
		InitialHeight:     1,
		InitialAppState:   new(bytes.Buffer),
		GenesisValidators: fx.Vals(),
	}

	// NOTE: Uncancellable root context means this leaks a goroutine.
	// Making this cancellable would require some rework on the parallel subtests.
	wd, _ := gwatchdog.NewNopWatchdog(context.Background(), gtest.NewLogger(t))

	fullOptions := map[string]tmengine.Opt{
		"WithGenesis": tmengine.WithGenesis(eg),

		"WithFinalizationStore": tmengine.WithFinalizationStore(fs),
		"WithMirrorStore":       tmengine.WithMirrorStore(ms),
		"WithRoundStore":        tmengine.WithRoundStore(rs),
		"WithValidatorStore":    tmengine.WithValidatorStore(vs),

		"WithHashScheme":                        tmengine.WithHashScheme(fx.HashScheme),
		"WithSignatureScheme":                   tmengine.WithSignatureScheme(fx.SignatureScheme),
		"WithCommonMessageSignatureProofScheme": tmengine.WithCommonMessageSignatureProofScheme(fx.CommonMessageSignatureProofScheme),

		"WithGossipStrategy":    tmengine.WithGossipStrategy(tmgossiptest.NopStrategy{}),
		"WithConsensusStrategy": tmengine.WithConsensusStrategy(cStrat),

		// One special case: we have the WithInternalRoundTimer option
		// so that you can set a round timer directly,
		// but external callers are expected to call WithTimeoutStrategy
		// which uses real timers.
		//
		// If there is no value set on the RoundTimer field,
		// the engine returns an error indicating that the WithTimeoutStrategy option was missed.
		"WithTimeoutStrategy": tmengine.WithInternalRoundTimer(new(tmstatetest.MockRoundTimer)),

		"WithBlockFinalizationChannel": tmengine.WithBlockFinalizationChannel(make(chan tmdriver.FinalizeBlockRequest)),

		"WithWatchdog": tmengine.WithWatchdog(wd),
	}

	requiredWithSignerOptions := map[string]tmengine.Opt{
		"WithActionStore": tmengine.WithActionStore(as),
	}

	t.Run("fully configured", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		opts := make([]tmengine.Opt, 0, len(fullOptions)+len(requiredWithSignerOptions)+1)
		for _, opt := range fullOptions {
			opts = append(opts, opt)
		}
		for _, opt := range requiredWithSignerOptions {
			opts = append(opts, opt)
		}
		opts = append(opts, tmengine.WithSigner(tmconsensustest.DeterministicValidatorsEd25519(1)[0].Signer))

		e, err := tmengine.New(ctx, gtest.NewLogger(t), opts...)
		require.NoError(t, err)
		defer e.Wait()
		defer cancel()
	})

	t.Run("no options at all", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		e, err := tmengine.New(ctx, gtest.NewLogger(t))
		require.Error(t, err)
		require.Nil(t, e)
	})

	optKeys := make([]string, 0, len(fullOptions))
	for k := range fullOptions {
		optKeys = append(optKeys, k)
	}

	// Sort the keys so that the tests run in a consistent order.
	sort.Strings(optKeys)

	for _, k := range optKeys {
		k := k
		t.Run("when missing required option "+k, func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			opts := make([]tmengine.Opt, 0, len(fullOptions)-1)
			for name, opt := range fullOptions {
				if name == k {
					continue
				}
				opts = append(opts, opt)
			}

			e, err := tmengine.New(ctx, gtest.NewLogger(t), opts...)

			require.Error(t, err)
			require.ErrorContains(t, err, "tmengine."+k)
			require.Nil(t, e)
		})
	}

	signerOptKeys := make([]string, 0, len(requiredWithSignerOptions))
	for k := range requiredWithSignerOptions {
		signerOptKeys = append(signerOptKeys, k)
	}
	sort.Strings(signerOptKeys)

	for _, k := range signerOptKeys {
		k := k
		t.Run("when missing signer-required option "+k, func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			opts := make([]tmengine.Opt, 0, len(fullOptions)+len(requiredWithSignerOptions))
			for _, opt := range fullOptions {
				opts = append(opts, opt)
			}
			for name, opt := range requiredWithSignerOptions {
				if name == k {
					continue
				}
				opts = append(opts, opt)
			}
			opts = append(opts, tmengine.WithSigner(tmconsensustest.DeterministicValidatorsEd25519(1)[0].Signer))

			e, err := tmengine.New(ctx, gtest.NewLogger(t), opts...)

			require.Error(t, err)
			require.ErrorContains(t, err, "tmengine."+k)
			require.Nil(t, e)
		})
	}
}

func TestEngine_mirrorSkipsAhead(t *testing.T) {
	t.Run("skip to next round due to minority prevote", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		efx := tmenginetest.NewFixture(ctx, t, 4)

		var engine *tmengine.Engine
		eReady := make(chan struct{})
		go func() {
			defer close(eReady)
			opts := efx.SigningOptionMap()
			delete(opts, "WithSigner")
			engine = efx.MustNewEngine(opts.ToSlice()...)
		}()

		defer func() {
			cancel()
			<-eReady
			engine.Wait()
		}()

		icReq := gtest.ReceiveSoon(t, efx.InitChainCh)

		const initAppStateHash = "app_state_0"

		gtest.SendSoon(t, icReq.Resp, tmdriver.InitChainResponse{
			AppStateHash: []byte(initAppStateHash),
		})

		// After we send the response, the engine is ready.
		_ = gtest.ReceiveSoon(t, eReady)

		_ = efx.ConsensusStrategy.ExpectEnterRound(1, 0, nil)

		// At the very beginning, the entire network submits a nil prevote at 1/0.
		keyHash, _ := efx.Fx.ValidatorHashes()
		fullPrevotes := efx.Fx.SparsePrevoteProofMap(ctx, 1, 0, map[string][]int{
			"": {0, 1, 2, 3},
		})
		prevoteSparseProof := tmconsensus.PrevoteSparseProof{
			Height: 1, Round: 0,
			PubKeyHash: keyHash,
			Proofs:     fullPrevotes,
		}
		require.Equal(t, tmconsensus.HandleVoteProofsAccepted, engine.HandlePrevoteProofs(ctx, prevoteSparseProof))

		// Now we get half precommits, but not enough to proceed.
		partialPrecommits := efx.Fx.SparsePrecommitProofMap(ctx, 1, 0, map[string][]int{
			"": {0, 1},
		})
		precommitSparseProof := tmconsensus.PrecommitSparseProof{
			Height: 1, Round: 0,
			PubKeyHash: keyHash,
			Proofs:     partialPrecommits,
		}
		require.Equal(t, tmconsensus.HandleVoteProofsAccepted, engine.HandlePrecommitProofs(ctx, precommitSparseProof))

		// Now, the state machine is able to handle events, but the mock consensus strategy
		// is currently blocked on a ChooseProposedBlock call.
		// It would be better if that had was associated with the round
		// and had a context that automatically canceled,
		// but for now we will just respond to unblock the mock consensus strategy.
		cbReq := gtest.ReceiveSoon(t, efx.ConsensusStrategy.ChooseProposedBlockRequests)
		gtest.SendSoon(t, cbReq.ChoiceHash, "")

		// Now before completing the precommits at 1/0,
		// we see half the prevotes for nil at 1/1,
		// which should cause our state machine to jump ahead.
		ercCh := efx.ConsensusStrategy.ExpectEnterRound(1, 1, nil)

		partialPrevotes := efx.Fx.SparsePrevoteProofMap(ctx, 1, 1, map[string][]int{
			"": {2, 3},
		})
		prevoteSparseProof = tmconsensus.PrevoteSparseProof{
			Height: 1, Round: 1,
			PubKeyHash: keyHash,
			Proofs:     partialPrevotes,
		}
		require.Equal(t, tmconsensus.HandleVoteProofsAccepted, engine.HandlePrevoteProofs(ctx, prevoteSparseProof))

		// Wait for state machine to enter 1/1.
		_ = gtest.ReceiveSoon(t, ercCh)
	})
}

func TestEngine_metrics(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	efx := tmenginetest.NewFixture(ctx, t, 4)

	metricsCh := make(chan tmengine.Metrics)
	var engine *tmengine.Engine
	eReady := make(chan struct{})
	go func() {
		defer close(eReady)
		opts := efx.SigningOptionMap().ToSlice()
		opts = append(opts, tmengine.WithMetricsChannel(metricsCh))
		engine = efx.MustNewEngine(opts...)
	}()

	defer func() {
		cancel()
		<-eReady
		engine.Wait()
	}()

	// Set up the EnterRound expectation before any other actions,
	// to avoid it happening early and causing a panic.
	cs := efx.ConsensusStrategy
	ercCh := cs.ExpectEnterRound(1, 0, nil)

	// It makes an init chain request.
	icReq := gtest.ReceiveSoon(t, efx.InitChainCh)

	const initAppStateHash = "app_state_0"

	gtest.SendSoon(t, icReq.Resp, tmdriver.InitChainResponse{
		AppStateHash: []byte(initAppStateHash),
	})

	// After we send the response, the engine is ready.
	_ = gtest.ReceiveSoon(t, eReady)

	// The network proposes a block and it is received.
	pb103 := efx.Fx.NextProposedBlock([]byte("app_data_1_0_3"), 3)
	efx.Fx.SignProposal(ctx, &pb103, 3)
	require.Equal(t, tmconsensus.HandleProposedBlockAccepted, engine.HandleProposedBlock(ctx, pb103))
	_ = gtest.ReceiveSoon(t, ercCh)

	// Since the mirror and state machine must have seen the proposed block,
	// it should be safe to assume they have both reported height 1 and round 0 to the metrics collector.
	m := gtest.ReceiveSoon(t, metricsCh)
	require.Equal(t, uint64(1), m.MirrorVotingHeight)
	require.Zero(t, m.MirrorVotingRound)
	require.Zero(t, m.MirrorCommittingHeight)
	require.Equal(t, uint64(1), m.StateMachineHeight)
	require.Zero(t, m.StateMachineRound)
}
