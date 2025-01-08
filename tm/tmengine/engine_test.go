package tmengine_test

import (
	"bytes"
	"context"
	"sort"
	"testing"
	"time"

	"github.com/bits-and-blooms/bitset"
	"github.com/gordian-engine/gordian/gwatchdog"
	"github.com/gordian-engine/gordian/internal/gtest"
	"github.com/gordian-engine/gordian/tm/tmconsensus"
	"github.com/gordian-engine/gordian/tm/tmconsensus/tmconsensustest"
	"github.com/gordian-engine/gordian/tm/tmdriver"
	"github.com/gordian-engine/gordian/tm/tmengine"
	"github.com/gordian-engine/gordian/tm/tmengine/internal/tmstate/tmstatetest"
	"github.com/gordian-engine/gordian/tm/tmengine/tmelink"
	"github.com/gordian-engine/gordian/tm/tmengine/tmenginetest"
	"github.com/gordian-engine/gordian/tm/tmgossip/tmgossiptest"
	"github.com/gordian-engine/gordian/tm/tmstore/tmmemstore"
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

		require.True(t, efx.Fx.ValSet().Equal(rv0.ValidatorSet))

		require.Empty(t, rv0.ProposedHeaders)

		require.Empty(t, rv0.PrevoteProofs)
		require.Empty(t, rv0.PrecommitProofs)

		var expAvailPow uint64
		for _, v := range efx.Fx.Vals() {
			expAvailPow += v.Power
		}
		require.Equal(t, expAvailPow, rv0.VoteSummary.AvailablePower)
		require.Zero(t, rv0.VoteSummary.TotalPrevotePower)
		require.Zero(t, rv0.VoteSummary.TotalPrecommitPower)
	})

	// Shared across subsequent subtests.
	var ph tmconsensus.ProposedHeader

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

		require.Len(t, vrv.ProposedHeaders, 1)
		ph = vrv.ProposedHeaders[0]
		require.Equal(t, "app_data_1", string(ph.Header.DataID))

		// The proposed header is not identical to the fixture's next proposed header.
		// Arguably that is a bug in the fixture,
		// but the actual engine sets the PrevBlockHash to match the genesis pseudo-block
		// and it has a PrevAppStateHash.
		expPH := efx.Fx.NextProposedHeader([]byte("app_data_1"), 0)
		expPH.Header.PrevAppStateHash = []byte(initAppStateHash)

		g := tmconsensus.Genesis{
			ChainID:             "my-chain", // NOTE: this is hard-coded to the fixture's default chain ID.
			InitialHeight:       1,
			CurrentAppStateHash: []byte(initAppStateHash),
			ValidatorSet:        efx.Fx.ValSet(),
		}

		gBlock, err := g.Header(efx.Fx.HashScheme)
		require.NoError(t, err)
		expPH.Header.PrevBlockHash = gBlock.Hash

		efx.Fx.RecalculateHash(&expPH.Header)

		efx.Fx.SignProposal(ctx, &expPH, 0)
		require.Equal(t, expPH, ph)
	})

	// Used by subsequent subtests.
	blockHash := string(ph.Header.Hash)
	var vrv tmconsensus.VersionedRoundView

	t.Run("mirror presents proposed header back to state machine", func(t *testing.T) {
		// Once the state machine has sent the proposed header to the mirror,
		// the mirror enqueues a view update presented to the state machine.
		// That update will include the new proposed header.
		// When the state machine receives that new proposed header,
		// it calls the consensus strategy's ConsiderProposedBlocks method.
		cReq := gtest.ReceiveSoon(t, efx.ConsensusStrategy.ConsiderProposedBlocksRequests)
		require.Equal(t, []tmconsensus.ProposedHeader{ph}, cReq.PHs)
		require.Equal(t, []string{string(ph.Header.Hash)}, cReq.Reason.NewProposedBlocks)

		// The state machine votes for the block it proposed.
		cReq.ChoiceHash <- blockHash

		// The mirror sends an updated voting view to the gossip strategy.
		vrv = *(gtest.ReceiveSoon(t, efx.GossipStrategy.Updates).Voting)
		require.Len(t, vrv.PrevoteProofs, 1) // Nil prevote automatically set, plus prevoted block.
		proof := vrv.PrevoteProofs[blockHash]
		var bs bitset.BitSet
		proof.SignatureBitSet(&bs)
		require.Equal(t, uint(1), bs.Count())
		require.True(t, bs.Test(0))
	})

	t.Run("state machine decides precommit when rest of prevotes arrive", func(t *testing.T) {
		fullPrevotes := efx.Fx.SparsePrevoteProofMap(ctx, 1, 0, map[string][]int{
			blockHash: {0, 1, 2, 3},
		})
		prevoteSparseProof := tmconsensus.PrevoteSparseProof{
			Height: 1, Round: 0,
			PubKeyHash: string(vrv.ValidatorSet.PubKeyHash),
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

		// It still has the proposed header and prevotes.
		require.Len(t, vrv.ProposedHeaders, 1)
		require.Len(t, vrv.PrevoteProofs, 1)
		var bs bitset.BitSet
		vrv.PrevoteProofs[blockHash].SignatureBitSet(&bs)
		require.Equal(t, uint(4), bs.Count())

		// And it has the new single precommit.
		require.Len(t, vrv.PrecommitProofs, 1)
		vrv.PrecommitProofs[blockHash].SignatureBitSet(&bs)
		require.Equal(t, uint(1), bs.Count())
		require.True(t, bs.Test(0))
	})

	t.Run("state machine finalizes block when rest of precommits arrive", func(t *testing.T) {
		fullPrecommits := efx.Fx.SparsePrecommitProofMap(ctx, 1, 0, map[string][]int{
			blockHash: {0, 1, 2, 3},
		})
		precommitSparseProof := tmconsensus.PrecommitSparseProof{
			Height: 1, Round: 0,
			PubKeyHash: string(vrv.ValidatorSet.PubKeyHash),
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
		require.Equal(t, ph.Header, finReq.Header)
		require.Zero(t, finReq.Round)

		// By the time the state machine received a finalize block request,
		// the round timer should be in commit wait.
		efx.RoundTimer.RequireActiveCommitWaitTimer(t, 1, 0)

		// Under normal circumstances, the finalize request will complete before the timeout.
		gtest.SendSoon(t, finReq.Resp, tmdriver.FinalizeBlockResponse{
			Height: 1, Round: 0,
			BlockHash: ph.Header.Hash,

			// Validators unchanged.
			Validators: efx.Fx.Vals(),

			AppStateHash: []byte("app_state_1"),
		})

		// We are going to propose a block at height 2 later,
		// so we need to commit the block at height 1 within the fixture.
		efx.Fx.CommitBlock(
			committingVRV.ProposedHeaders[0].Header,
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
		// After that timer elapses, the consensus strategy must choose a proposed header
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
			PubKeyHash: string(vrv.ValidatorSet.PubKeyHash),
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
			PubKeyHash: string(vrv.ValidatorSet.PubKeyHash),
			Proofs:     fullPrecommits,
		}
		require.Equal(t, tmconsensus.HandleVoteProofsAccepted, engine.HandlePrecommitProofs(ctx, precommitSparseProof))

		// The enter round call has the correct height and round.
		erReq := gtest.ReceiveSoon(t, ercCh)
		require.Equal(t, uint64(2), erReq.RV.Height)
		require.Equal(t, uint32(1), erReq.RV.Round)
	})

	t.Run("chain advances properly from round 1 on a height", func(t *testing.T) {
		ph21 := efx.Fx.NextProposedHeader([]byte("app_data_2_1"), 1)
		ph21.Round = 1
		efx.Fx.SignProposal(ctx, &ph21, 1)
		require.Equal(t, tmconsensus.HandleProposedHeaderAccepted, engine.HandleProposedHeader(ctx, ph21))

		cpbReq := gtest.ReceiveSoon(t, cs.ConsiderProposedBlocksRequests)
		require.Equal(t, []tmconsensus.ProposedHeader{ph21}, cpbReq.PHs)
		require.Equal(t, []string{string(ph21.Header.Hash)}, cpbReq.Reason.NewProposedBlocks)

		// There is technically a data race on asserting this timer active
		// immediately after making the enter round call.
		// But it certainly must be active by the time a call to ConsiderProposedBlocks happens.
		efx.RoundTimer.RequireActiveProposalTimer(t, 2, 1)

		// Sending the precommit choice updates the voting view to the gossip strategy.
		_ = gtest.ReceiveSoon(t, efx.GossipStrategy.Updates)

		blockHash := string(ph21.Header.Hash)
		gtest.SendSoon(t, cpbReq.ChoiceHash, blockHash)
		_ = gtest.ReceiveSoon(t, efx.GossipStrategy.Updates)

		// The rest of the prevotes arrive.
		fullPrevotes := efx.Fx.SparsePrevoteProofMap(ctx, 2, 1, map[string][]int{
			blockHash: {0, 1, 2, 3},
		})
		prevoteSparseProof := tmconsensus.PrevoteSparseProof{
			Height: 2, Round: 1,
			PubKeyHash: string(vrv.ValidatorSet.PubKeyHash),
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
			PubKeyHash: string(vrv.ValidatorSet.PubKeyHash),
			Proofs:     fullPrecommits,
		}
		require.Equal(t, tmconsensus.HandleVoteProofsAccepted, engine.HandlePrecommitProofs(ctx, precommitSparseProof))

		finReq := gtest.ReceiveSoon(t, efx.FinalizeBlockRequests)
		require.Equal(t, ph21.Header, finReq.Header)
		require.Equal(t, uint32(1), finReq.Round)

		// The commit wait timer elapses before finalization.
		require.NoError(t, efx.RoundTimer.ElapseCommitWaitTimer(2, 1))

		// Expect enter round before we send the finalization response.
		ercCh := cs.ExpectEnterRound(3, 0, nil)

		finReq.Resp <- tmdriver.FinalizeBlockResponse{
			Height: 2, Round: 1,
			BlockHash: ph21.Header.Hash,

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
		require.Empty(t, u.Voting.ProposedHeaders)

		require.Equal(t, uint64(1), u.NextRound.Height)
		require.Equal(t, uint32(1), u.NextRound.Round)
		require.Empty(t, u.NextRound.ProposedHeaders)
	})

	ph103 := efx.Fx.NextProposedHeader([]byte("app_data_1_0_3"), 3)
	ph103.Header.PrevAppStateHash = []byte(initAppStateHash)
	efx.Fx.RecalculateHash(&ph103.Header)
	efx.Fx.SignProposal(ctx, &ph103, 3)
	t.Run("proposed header from network", func(t *testing.T) {
		require.Equal(t, tmconsensus.HandleProposedHeaderAccepted, engine.HandleProposedHeader(ctx, ph103))

		u := gtest.ReceiveSoon(t, gs.Updates)

		require.Equal(t, uint64(1), u.Voting.Height)
		require.Zero(t, u.Voting.Round)
		require.Equal(t, []tmconsensus.ProposedHeader{ph103}, u.Voting.ProposedHeaders)

		require.Nil(t, u.Committing)
		require.Nil(t, u.NextRound)

		cReq := gtest.ReceiveSoon(t, cs.ConsiderProposedBlocksRequests)
		require.Equal(t, []string{string(ph103.Header.Hash)}, cReq.Reason.NewProposedBlocks)
		gtest.SendSoon(t, cReq.ChoiceError, tmconsensus.ErrProposedBlockChoiceNotReady)
	})

	var ph100 tmconsensus.ProposedHeader
	var blockHash100 string
	t.Run("proposed header from state machine", func(t *testing.T) {
		gtest.SendSoon(t, erc.ProposalOut, tmconsensus.Proposal{
			DataID: "app_data_1_0_0",
		})

		// The proposed headers that arrive from the state machine
		// are constructed slightly differently from the fixture --
		// the fixture omits the previous block hash and previous app state hash,
		// at genesis at least.
		// So load the proposed header from the state machine's action store
		// to confirm it matches with what the gossip strategy receives.

		u := gtest.ReceiveSoon(t, gs.Updates)

		ra, err := efx.ActionStore.LoadActions(ctx, 1, 0)
		require.NoError(t, err)
		ph100 = ra.ProposedHeader
		blockHash100 = string(ph100.Header.Hash)
		require.Equal(t, uint64(1), ph100.Header.Height)
		require.Zero(t, ph100.Round)
		require.Equal(t, "app_data_1_0_0", string(ph100.Header.DataID))

		require.Equal(t, uint64(1), u.Voting.Height)
		require.Zero(t, u.Voting.Round)
		require.Equal(t, []tmconsensus.ProposedHeader{ph103, ph100}, u.Voting.ProposedHeaders)

		require.Nil(t, u.Committing)
		require.Nil(t, u.NextRound)
	})

	// From here the network is going to settle on the 1_0_3 block, not the 1_0_0.
	blockHash103 := string(ph103.Header.Hash)
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
		require.Equal(t, []tmconsensus.ProposedHeader{ph103, ph100}, u.Voting.ProposedHeaders)

		proof103 := u.Voting.PrevoteProofs[blockHash103]
		var bs bitset.BitSet
		proof103.SignatureBitSet(&bs)
		require.Equal(t, uint(1), bs.Count())
		require.True(t, bs.Test(3))
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
		var bs bitset.BitSet
		proof103.SignatureBitSet(&bs)
		require.Equal(t, uint(1), bs.Count())

		proof100 := u.Voting.PrevoteProofs[blockHash100]
		proof100.SignatureBitSet(&bs)
		require.True(t, bs.Test(0))
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
		var bs bitset.BitSet
		prevote103.SignatureBitSet(&bs)
		require.Equal(t, uint(3), bs.Count())

		precommit103 := u.Voting.PrecommitProofs[blockHash103]
		precommit103.SignatureBitSet(&bs)
		require.Equal(t, uint(2), bs.Count())
		require.True(t, bs.Test(0))
		require.True(t, bs.Test(3))
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

func TestEngine_plumbing_LagState(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	efx := tmenginetest.NewFixture(ctx, t, 4)

	lagCh := make(chan tmelink.LagState)

	var engine *tmengine.Engine
	eReady := make(chan struct{})
	go func() {
		defer close(eReady)
		// Don't need signing in this test.
		om := efx.BaseOptionMap()
		om["WithLagStateChannel"] = tmengine.WithLagStateChannel(lagCh)
		engine = efx.MustNewEngine(om.ToSlice()...)
	}()

	defer func() {
		cancel()
		<-eReady
		engine.Wait()
	}()

	_ = efx.ConsensusStrategy.ExpectEnterRound(1, 0, nil)

	// Handle chain initialization first to avoid panic in fixture.
	icReq := gtest.ReceiveSoon(t, efx.InitChainCh)
	gtest.SendSoon(t, icReq.Resp, tmdriver.InitChainResponse{
		AppStateHash: []byte("whatever"),
	})

	// After we send the response, the engine is ready.
	_ = gtest.ReceiveSoon(t, eReady)

	// At startup, lag state is initializing.
	ls := gtest.ReceiveSoon(t, lagCh)
	require.Equal(t, tmelink.LagState{
		Status: tmelink.LagStatusInitializing,
	}, ls)

	ph1 := efx.Fx.NextProposedHeader([]byte("app_state_1"), 0)
	efx.Fx.SignProposal(ctx, &ph1, 0)

	_ = engine.HandleProposedHeader(ctx, ph1)

	require.Equal(t, tmelink.LagState{
		Status: tmelink.LagStatusUpToDate,
	}, gtest.ReceiveSoon(t, lagCh))
}

func TestEngine_plumbing_ReplayedHeaders(t *testing.T) {
	t.Run("success when height=1 round=0", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		efx := tmenginetest.NewFixture(ctx, t, 4)

		rhCh := make(chan tmelink.ReplayedHeaderRequest)

		var engine *tmengine.Engine
		eReady := make(chan struct{})
		go func() {
			defer close(eReady)
			// Don't need signing in this test.
			om := efx.BaseOptionMap()
			om["WithReplayedHeaderRequestChannel"] = tmengine.WithReplayedHeaderRequestChannel(rhCh)
			engine = efx.MustNewEngine(om.ToSlice()...)
		}()

		defer func() {
			cancel()
			<-eReady
			engine.Wait()
		}()

		_ = efx.ConsensusStrategy.ExpectEnterRound(1, 0, nil)

		// Handle chain initialization first to avoid panic in fixture.
		icReq := gtest.ReceiveSoon(t, efx.InitChainCh)
		gtest.SendSoon(t, icReq.Resp, tmdriver.InitChainResponse{
			AppStateHash: []byte("whatever"),
		})

		// After we send the response, the engine is ready.
		_ = gtest.ReceiveSoon(t, eReady)

		// Make a committed header through the fixture.
		ph1 := efx.Fx.NextProposedHeader([]byte("app_state_1"), 0)
		efx.Fx.SignProposal(ctx, &ph1, 0)
		voteMap := map[string][]int{
			string(ph1.Header.Hash): {0, 1, 2, 3},
		}
		precommitProofsMap := efx.Fx.PrecommitProofMap(ctx, 1, 0, voteMap)
		efx.Fx.CommitBlock(ph1.Header, []byte("app_state_height_1"), 0, precommitProofsMap)
		ph2 := efx.Fx.NextProposedHeader([]byte("app_state_2"), 0)

		respCh := make(chan tmelink.ReplayedHeaderResponse, 1)
		gtest.SendSoon(t, rhCh, tmelink.ReplayedHeaderRequest{
			Header: ph1.Header,
			Proof:  ph2.Header.PrevCommitProof,
			Resp:   respCh,
		})

		// Replayed block is accepted.
		resp := gtest.ReceiveSoon(t, respCh)
		require.NoError(t, resp.Err)

		// It won't be present in the header store yet,
		// since we only shifted it from voting to committing.
		// But it should be in the round store now.
		phs, _, precommits, err := efx.RoundStore.LoadRoundState(ctx, 1, 0)
		require.NoError(t, err)
		require.Equal(t, []tmconsensus.ProposedHeader{
			{
				Header: ph1.Header,
				// PubKey and Signature missing from round store during replay.
			},
		}, phs)

		fullPrecommits, err := precommits.ToFullPrecommitProofMap(
			1, 0,
			efx.Fx.ValSet(),
			efx.Fx.SignatureScheme, efx.Fx.CommonMessageSignatureProofScheme,
		)
		require.NoError(t, err)

		var bs bitset.BitSet
		fullPrecommits[string(ph1.Header.Hash)].SignatureBitSet(&bs)
		require.Equal(t, uint(4), bs.Count())

		// The state machine should be finalizing 1/0.
		finReq := gtest.ReceiveSoon(t, efx.FinalizeBlockRequests)
		require.Equal(t, uint64(1), finReq.Header.Height)
		require.Zero(t, finReq.Round)
	})

	t.Run("success when height=1 round=1", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		efx := tmenginetest.NewFixture(ctx, t, 4)

		rhCh := make(chan tmelink.ReplayedHeaderRequest)

		var engine *tmengine.Engine
		eReady := make(chan struct{})
		go func() {
			defer close(eReady)
			// Don't need signing in this test.
			om := efx.BaseOptionMap()
			om["WithReplayedHeaderRequestChannel"] = tmengine.WithReplayedHeaderRequestChannel(rhCh)
			engine = efx.MustNewEngine(om.ToSlice()...)
		}()

		defer func() {
			cancel()
			<-eReady
			engine.Wait()
		}()

		// We still expect the consensus strategy to enter 1/0 at startup
		// before we know anything about the external world.
		_ = efx.ConsensusStrategy.ExpectEnterRound(1, 0, nil)

		// And there will be a call to EnterRound(1, 1)
		// when we replay the header for 1/1.
		_ = efx.ConsensusStrategy.ExpectEnterRound(1, 1, nil)

		// Handle chain initialization first to avoid panic in fixture.
		icReq := gtest.ReceiveSoon(t, efx.InitChainCh)
		gtest.SendSoon(t, icReq.Resp, tmdriver.InitChainResponse{
			AppStateHash: []byte("whatever"),
		})

		// After we send the response, the engine is ready.
		_ = gtest.ReceiveSoon(t, eReady)

		// Make a committed header through the fixture.
		// This header has round 1.
		ph1 := efx.Fx.NextProposedHeader([]byte("app_state_1"), 0)
		ph1.Round = 1
		efx.Fx.RecalculateHash(&ph1.Header)
		efx.Fx.SignProposal(ctx, &ph1, 0)
		voteMap := map[string][]int{
			string(ph1.Header.Hash): {0, 1, 2, 3},
		}
		precommitProofsMap := efx.Fx.PrecommitProofMap(ctx, 1, 1, voteMap)
		efx.Fx.CommitBlock(ph1.Header, []byte("app_state_height_1"), 1, precommitProofsMap)
		ph2 := efx.Fx.NextProposedHeader([]byte("app_state_2"), 0)

		respCh := make(chan tmelink.ReplayedHeaderResponse, 1)
		gtest.SendSoon(t, rhCh, tmelink.ReplayedHeaderRequest{
			Header: ph1.Header,
			Proof:  ph2.Header.PrevCommitProof,
			Resp:   respCh,
		})

		// Replayed block is accepted.
		resp := gtest.ReceiveSoon(t, respCh)
		require.NoError(t, resp.Err)

		// It won't be present in the header store yet,
		// since we only shifted it from voting to committing.
		// But it should be in the round store now.
		phs, _, precommits, err := efx.RoundStore.LoadRoundState(ctx, 1, 1)
		require.NoError(t, err)
		require.Equal(t, []tmconsensus.ProposedHeader{
			{
				Header: ph1.Header,
				Round:  0, // Replayed headers always appear as round zero.
				// PubKey and Signature missing from round store during replay.
			},
		}, phs)

		fullPrecommits, err := precommits.ToFullPrecommitProofMap(
			1, 1,
			efx.Fx.ValSet(),
			efx.Fx.SignatureScheme, efx.Fx.CommonMessageSignatureProofScheme,
		)
		require.NoError(t, err)

		var bs bitset.BitSet
		fullPrecommits[string(ph1.Header.Hash)].SignatureBitSet(&bs)
		require.Equal(t, uint(4), bs.Count())

		// The state machine should be finalizing 1/1.
		finReq := gtest.ReceiveSoon(t, efx.FinalizeBlockRequests)
		require.Equal(t, uint64(1), finReq.Header.Height)
		require.Equal(t, uint32(1), finReq.Round)
	})
}

func TestEngine_wiring_validatorChanges(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Begin with two validators, and on the first finalization we will add a third.
	efx := tmenginetest.NewFixture(ctx, t, 2)
	origValSet := efx.Fx.ValSet()

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

	erc1Ch := efx.ConsensusStrategy.ExpectEnterRound(1, 0, nil)

	icReq := gtest.ReceiveSoon(t, efx.InitChainCh)

	gtest.SendSoon(t, icReq.Resp, tmdriver.InitChainResponse{
		AppStateHash: []byte("app_state_0"),
	})

	_ = gtest.ReceiveSoon(t, eReady)

	erc1 := gtest.ReceiveSoon(t, erc1Ch)
	require.True(t, origValSet.Equal(erc1.RV.ValidatorSet))

	// Our state machine proposes a header.
	// (Drain the gossip strategy updates first.
	_ = gtest.ReceiveSoon(t, efx.GossipStrategy.Updates)
	gtest.SendSoon(t, erc1.ProposalOut, tmconsensus.Proposal{
		DataID: "app_data_1",
	})

	// That proposed header gets gossiped out.
	u := gtest.ReceiveSoon(t, efx.GossipStrategy.Updates)
	require.NotNil(t, u.Voting)
	require.Len(t, u.Voting.ProposedHeaders, 1)
	ph1 := u.Voting.ProposedHeaders[0]

	// It has the same data ID that we supplied,
	// and the previous commit proofs are empty since this is initial height.
	require.Equal(t, "app_data_1", string(ph1.Header.DataID))
	require.NotNil(t, ph1.Header.PrevCommitProof.Proofs)
	require.Empty(t, ph1.Header.PrevCommitProof.Proofs)

	consReq1 := gtest.ReceiveSoon(t, efx.ConsensusStrategy.ConsiderProposedBlocksRequests)
	require.Equal(t, []tmconsensus.ProposedHeader{ph1}, consReq1.PHs)
	require.Equal(t, []string{string(ph1.Header.Hash)}, consReq1.Reason.NewProposedBlocks)

	// Our consensus strategy chooses the header we just proposed.
	gtest.SendSoon(t, consReq1.ChoiceHash, string(ph1.Header.Hash))

	// And now the network receives the other validator's prevote for ph1.
	extPrevotes1 := efx.Fx.SparsePrevoteProofMap(ctx, 1, 0, map[string][]int{
		string(ph1.Header.Hash): {1},
	})
	extPrevotes1SparseProof := tmconsensus.PrevoteSparseProof{
		Height: 1, Round: 0,
		PubKeyHash: string(origValSet.PubKeyHash),
		Proofs:     extPrevotes1,
	}
	require.Equal(t, tmconsensus.HandleVoteProofsAccepted, engine.HandlePrevoteProofs(ctx, extPrevotes1SparseProof))

	// Everyone has prevoted, so our consensus strategy needs to decide its precommit.
	dReq1 := gtest.ReceiveSoon(t, efx.ConsensusStrategy.DecidePrecommitRequests)
	require.Equal(t, dReq1.Input.TotalPrevotePower, dReq1.Input.AvailablePower)
	require.Equal(t, string(ph1.Header.Hash), dReq1.Input.MostVotedPrevoteHash)
	gtest.SendSoon(t, dReq1.ChoiceHash, string(ph1.Header.Hash))

	// Then the network receives the other precommit.
	extPrecommits1 := efx.Fx.SparsePrecommitProofMap(ctx, 1, 0, map[string][]int{
		string(ph1.Header.Hash): {1},
	})
	extPrecommits1SparseProof := tmconsensus.PrecommitSparseProof{
		Height: 1, Round: 0,
		PubKeyHash: string(origValSet.PubKeyHash),
		Proofs:     extPrecommits1,
	}
	require.Equal(t, tmconsensus.HandleVoteProofsAccepted, engine.HandlePrecommitProofs(ctx, extPrecommits1SparseProof))

	// This causes a finalization request.
	finReq1 := gtest.ReceiveSoon(t, efx.FinalizeBlockRequests)
	require.Equal(t, ph1.Header, finReq1.Header)
	require.Zero(t, finReq1.Round)

	threeVals := tmconsensustest.DeterministicValidatorsEd25519(3).Vals()
	threeValSet, err := tmconsensus.NewValidatorSet(threeVals, efx.Fx.HashScheme)
	require.NoError(t, err)

	gtest.SendSoon(t, finReq1.Resp, tmdriver.FinalizeBlockResponse{
		Height:    1,
		Round:     0,
		BlockHash: ph1.Header.Hash,

		Validators: threeVals,

		AppStateHash: []byte("app_state_1"),
	})

	// Expect round 2 to start as we finish the commit wait timer.
	erc2Ch := efx.ConsensusStrategy.ExpectEnterRound(2, 0, nil)
	require.NoError(t, efx.RoundTimer.ElapseCommitWaitTimer(1, 0))

	erc2 := gtest.ReceiveSoon(t, erc2Ch)
	require.True(t, origValSet.Equal(erc2.RV.ValidatorSet))
	require.Equal(t, string(origValSet.PubKeyHash), erc2.RV.PrevCommitProof.PubKeyHash)

	// Now we do everything we did at height 1, again.
	// First our state machine proposes a block.
	_ = gtest.ReceiveSoon(t, efx.GossipStrategy.Updates)
	gtest.SendSoon(t, erc2.ProposalOut, tmconsensus.Proposal{
		DataID: "app_data_2",
	})

	// That proposed header gets gossiped out.
	u = gtest.ReceiveSoon(t, efx.GossipStrategy.Updates)
	require.NotNil(t, u.Voting)
	require.Len(t, u.Voting.ProposedHeaders, 1)
	ph2 := u.Voting.ProposedHeaders[0]

	require.Equal(t, string(ph1.Header.Hash), string(ph2.Header.PrevBlockHash))
	require.Equal(t, uint64(2), ph2.Header.Height)

	require.Zero(t, ph2.Header.PrevCommitProof.Round)
	require.Equal(t, string(origValSet.PubKeyHash), ph2.Header.PrevCommitProof.PubKeyHash)

	require.Len(t, ph2.Header.PrevCommitProof.Proofs, 1)
	require.NotNil(t, ph2.Header.PrevCommitProof.Proofs[string(ph1.Header.Hash)])
	// TODO: verify individual signatures?

	require.True(t, origValSet.Equal(ph2.Header.ValidatorSet))
	require.True(t, threeValSet.Equal(ph2.Header.NextValidatorSet))

	require.Equal(t, "app_data_2", string(ph2.Header.DataID))
	require.Equal(t, "app_state_1", string(ph2.Header.PrevAppStateHash))

	// We choose the header we proposed.
	consReq2 := gtest.ReceiveSoon(t, efx.ConsensusStrategy.ConsiderProposedBlocksRequests)
	require.Equal(t, []tmconsensus.ProposedHeader{ph2}, consReq2.PHs)
	require.Equal(t, []string{string(ph2.Header.Hash)}, consReq2.Reason.NewProposedBlocks)
	gtest.SendSoon(t, consReq2.ChoiceHash, string(ph2.Header.Hash))

	// The other validator prevotes for the same header.
	extPrevotes2 := efx.Fx.SparsePrevoteProofMap(ctx, 2, 0, map[string][]int{
		string(ph2.Header.Hash): {1},
	})
	extPrevotes2SparseProof := tmconsensus.PrevoteSparseProof{
		Height: 2, Round: 0,
		PubKeyHash: string(origValSet.PubKeyHash),
		Proofs:     extPrevotes2,
	}
	require.Equal(t, tmconsensus.HandleVoteProofsAccepted, engine.HandlePrevoteProofs(ctx, extPrevotes2SparseProof))

	// Now we decide our precommit.
	dReq2 := gtest.ReceiveSoon(t, efx.ConsensusStrategy.DecidePrecommitRequests)
	require.Equal(t, dReq2.Input.TotalPrevotePower, dReq2.Input.AvailablePower)
	require.Equal(t, string(ph2.Header.Hash), dReq2.Input.MostVotedPrevoteHash)
	gtest.SendSoon(t, dReq2.ChoiceHash, string(ph2.Header.Hash))

	// And the network receives the external precommit too.
	extPrecommits2 := efx.Fx.SparsePrecommitProofMap(ctx, 2, 0, map[string][]int{
		string(ph2.Header.Hash): {1},
	})
	extPrecommits2SparseProof := tmconsensus.PrecommitSparseProof{
		Height: 2, Round: 0,
		PubKeyHash: string(origValSet.PubKeyHash),
		Proofs:     extPrecommits2,
	}
	require.Equal(t, tmconsensus.HandleVoteProofsAccepted, engine.HandlePrecommitProofs(ctx, extPrecommits2SparseProof))

	// So now we have another finalization request.
	finReq2 := gtest.ReceiveSoon(t, efx.FinalizeBlockRequests)
	require.Equal(t, ph2.Header, finReq2.Header)
	require.Zero(t, finReq2.Round)

	fourVals := tmconsensustest.DeterministicValidatorsEd25519(4).Vals()

	gtest.SendSoon(t, finReq2.Resp, tmdriver.FinalizeBlockResponse{
		Height:    2,
		Round:     0,
		BlockHash: ph2.Header.Hash,

		Validators: fourVals,

		AppStateHash: []byte("app_state_2"),
	})

	// Expect round 3 to start as we finish the commit wait timer.
	erc3Ch := efx.ConsensusStrategy.ExpectEnterRound(3, 0, nil)
	require.NoError(t, efx.RoundTimer.ElapseCommitWaitTimer(2, 0))

	// This is the main thing we wanted to verify in this test:
	// the changed validator set does not leak incorrectly into the previous commit proof.
	erc3 := gtest.ReceiveSoon(t, erc3Ch)
	require.True(t, threeValSet.Equal(erc3.RV.ValidatorSet))
	require.Equal(t, string(origValSet.PubKeyHash), erc3.RV.PrevCommitProof.PubKeyHash)
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
		round, _, valSet, appStateHash, err := efx.FinalizationStore.LoadFinalizationByHeight(ctx, 0)
		require.NoError(t, err)
		require.Zero(t, round)
		require.True(t, valSet.Equal(efx.Fx.ValSet()))
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
		newValSet, err := tmconsensus.NewValidatorSet(newVals, efx.Fx.HashScheme)
		require.NoError(t, err)
		round, _, valSet, appStateHash, err := efx.FinalizationStore.LoadFinalizationByHeight(ctx, 0)
		require.NoError(t, err)
		require.Zero(t, round)
		require.True(t, valSet.Equal(newValSet))
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
				ChainID:         "my-chain",
				InitialHeight:   1,
				InitialAppState: new(bytes.Buffer),
				// Explicitly do not set GenesisValidators here.
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
		newValSet, err := tmconsensus.NewValidatorSet(newVals, efx.Fx.HashScheme)
		require.NoError(t, err)
		round, _, valSet, appStateHash, err := efx.FinalizationStore.LoadFinalizationByHeight(ctx, 0)
		require.NoError(t, err)
		require.Zero(t, round)
		require.True(t, valSet.Equal(newValSet))
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
			efx.Fx.ValSet(),
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

		// Before starting the engine, set the network height-round,
		// and set the finalization so we can re-load the initial validator set.
		require.NoError(t, efx.MirrorStore.SetNetworkHeightRound(ctx, 1, 0, 0, 0))
		require.NoError(t, efx.FinalizationStore.SaveFinalization(
			ctx, 0, 0, "some_block_hash", efx.Fx.ValSet(), "some_initial_app_state_hash",
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

		// Shortly after the engine is ready,
		// the init chain channel is closed.
		// This is such a rare setup, that we do not have a gtest helper for it.
		// We can add one if this comes up more in the future.
		timer := time.NewTimer(time.Second) // TODO: use gtest timer.
		defer timer.Stop()
		select {
		case _, ok := <-efx.InitChainCh:
			require.False(t, ok)
		case <-timer.C:
			t.Fatal("init chain channel not closed in time")
		}

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
	sms := tmmemstore.NewStateMachineStore()
	vs := fx.NewMemValidatorStore()

	// Set a network height-round and a finalization to trick the engine into thinking
	// the chain has already been initialized.
	require.NoError(t, ms.SetNetworkHeightRound(context.Background(), 1, 0, 0, 0))
	require.NoError(t, fs.SaveFinalization(
		context.Background(), 0, 0, "some_block_hash", fx.ValSet(), "some_initial_app_state_hash",
	))

	cStrat := tmconsensustest.NewMockConsensusStrategy()

	eg := &tmconsensus.ExternalGenesis{
		ChainID:             "my-chain",
		InitialHeight:       1,
		InitialAppState:     new(bytes.Buffer),
		GenesisValidatorSet: fx.ValSet(),
	}

	// NOTE: Uncancellable root context means this leaks a goroutine.
	// Making this cancellable would require some rework on the parallel subtests.
	wd, _ := gwatchdog.NewNopWatchdog(context.Background(), gtest.NewLogger(t))

	fullOptions := map[string]tmengine.Opt{
		"WithGenesis": tmengine.WithGenesis(eg),

		"WithFinalizationStore": tmengine.WithFinalizationStore(fs),
		"WithMirrorStore":       tmengine.WithMirrorStore(ms),
		"WithRoundStore":        tmengine.WithRoundStore(rs),
		"WithStateMachineStore": tmengine.WithStateMachineStore(sms),
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
		opts = append(opts, tmengine.WithSigner(tmconsensus.PassthroughSigner{
			Signer:          tmconsensustest.DeterministicValidatorsEd25519(1)[0].Signer,
			SignatureScheme: fx.SignatureScheme,
		}))

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
			opts = append(opts, tmengine.WithSigner(tmconsensus.PassthroughSigner{
				Signer:          tmconsensustest.DeterministicValidatorsEd25519(1)[0].Signer,
				SignatureScheme: fx.SignatureScheme,
			}))

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
	ph103 := efx.Fx.NextProposedHeader([]byte("app_data_1_0_3"), 3)
	efx.Fx.SignProposal(ctx, &ph103, 3)
	require.Equal(t, tmconsensus.HandleProposedHeaderAccepted, engine.HandleProposedHeader(ctx, ph103))
	_ = gtest.ReceiveSoon(t, ercCh)

	// Since the mirror and state machine must have seen the proposed header,
	// it should be safe to assume they have both reported height 1 and round 0 to the metrics collector.
	m := gtest.ReceiveSoon(t, metricsCh)
	require.Equal(t, uint64(1), m.MirrorVotingHeight)
	require.Zero(t, m.MirrorVotingRound)
	require.Zero(t, m.MirrorCommittingHeight)
	require.Equal(t, uint64(1), m.StateMachineHeight)
	require.Zero(t, m.StateMachineRound)
}
