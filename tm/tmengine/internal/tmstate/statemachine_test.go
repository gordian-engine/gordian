package tmstate_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/rollchains/gordian/gcrypto"
	"github.com/rollchains/gordian/internal/gtest"
	"github.com/rollchains/gordian/tm/tmconsensus"
	"github.com/rollchains/gordian/tm/tmconsensus/tmconsensustest"
	"github.com/rollchains/gordian/tm/tmdriver"
	"github.com/rollchains/gordian/tm/tmengine/internal/tmeil"
	"github.com/rollchains/gordian/tm/tmengine/internal/tmemetrics"
	"github.com/rollchains/gordian/tm/tmengine/internal/tmstate/tmstatetest"
	"github.com/rollchains/gordian/tm/tmengine/tmelink"
	"github.com/stretchr/testify/require"
)

func TestStateMachine_initialization(t *testing.T) {
	t.Run("initial round entrance at genesis", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		sfx := tmstatetest.NewFixture(ctx, t, 2)

		// No extra data in any stores, so we are from a natural 1/0 genesis.

		sm := sfx.NewStateMachine()
		defer sm.Wait()
		defer cancel()

		// The state machine sends its initial action set at 1/0.
		re := gtest.ReceiveSoon(t, sfx.RoundEntranceOutCh)
		require.Equal(t, uint64(1), re.H)
		require.Zero(t, re.R)

		// The pubkey matches the signer.
		require.True(t, sfx.Fx.PrivVals[0].Signer.PubKey().Equal(re.PubKey))

		// The created Actions channel is 3-buffered so sends from the state machine do not block.
		require.Equal(t, 3, cap(re.Actions))

		// And the response is 1-buffered so the kernel does not block in sending its response.
		require.Equal(t, 1, cap(re.Response))
	})

	t.Run("empty round view response from mirror", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		sfx := tmstatetest.NewFixture(ctx, t, 2)

		sm := sfx.NewStateMachine()
		defer sm.Wait()
		defer cancel()

		re := gtest.ReceiveSoon(t, sfx.RoundEntranceOutCh)

		// Set up consensus strategy expectation before mocking the response.
		cStrat := sfx.CStrat
		enterCh := cStrat.ExpectEnterRound(1, 0, nil)

		// Channel is 1-buffered, don't have to select.
		vrv := sfx.EmptyVRV(1, 0)
		re.Response <- tmeil.RoundEntranceResponse{VRV: vrv} // No PrevBlockHash at initial height.

		// And since we sent a VRV, the state machine calls into consensus strategy,
		// with vrv's RoundView.
		erc := gtest.ReceiveSoon(t, enterCh)
		require.Equal(t, vrv.RoundView, erc.RV)

		// Now, if the consensus strategy were to send a proposed block,
		// the state machine would pass it on to the mirror.
		p := tmconsensus.Proposal{
			DataID: "foobar",
		}
		gtest.SendSoon(t, erc.ProposalOut, p)

		// Sending the proposal causes a corresponding proposed block action to the mirror.
		action := gtest.ReceiveSoon(t, re.Actions)
		require.Empty(t, action.Prevote.Sig)
		require.Empty(t, action.Precommit.Sig)

		expPB := sfx.Fx.NextProposedBlock([]byte("foobar"), 0)
		sfx.Fx.SignProposal(ctx, &expPB, 0)
		require.Equal(t, expPB, action.PB)
	})

	t.Run("round view response from mirror when a proposed block is present", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		sfx := tmstatetest.NewFixture(ctx, t, 4)

		sm := sfx.NewStateMachine()
		defer sm.Wait()
		defer cancel()

		re := gtest.ReceiveSoon(t, sfx.RoundEntranceOutCh)

		// Set up consensus strategy expectation before mocking the response.
		cStrat := sfx.CStrat
		enterCh := cStrat.ExpectEnterRound(1, 0, nil)

		// Channel is 1-buffered, don't have to select.
		vrv := sfx.EmptyVRV(1, 0)
		re.Response <- tmeil.RoundEntranceResponse{VRV: vrv}

		// And since we sent a VRV, the state machine calls into consensus strategy,
		// with vrv's RoundView.
		erc := gtest.ReceiveSoon(t, enterCh)
		require.Equal(t, vrv.RoundView, erc.RV)

		pb1 := sfx.Fx.NextProposedBlock([]byte("app_data_1"), 1)
		sfx.Fx.SignProposal(ctx, &pb1, 1)
		vrv.ProposedBlocks = []tmconsensus.ProposedBlock{pb1}
		vrv.Version++

		// Sending an updated set of proposed blocks...
		gtest.SendSoon(t, sfx.RoundViewInCh, tmeil.StateMachineRoundView{VRV: vrv})

		// ... forces the consensus strategy to consider the available proposed blocks.
		pbReq := gtest.ReceiveSoon(t, cStrat.ConsiderProposedBlocksRequests)
		require.Equal(t, []tmconsensus.ProposedBlock{pb1}, pbReq.PBs)
		require.Equal(t, []string{string(pb1.Block.Hash)}, pbReq.Reason.NewProposedBlocks)
	})

	t.Run("round view response from mirror where network is in minority prevote", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		sfx := tmstatetest.NewFixture(ctx, t, 4)

		sm := sfx.NewStateMachine()
		defer sm.Wait()
		defer cancel()

		re := gtest.ReceiveSoon(t, sfx.RoundEntranceOutCh)

		vrv := sfx.EmptyVRV(1, 0)
		pb1 := sfx.Fx.NextProposedBlock([]byte("app_data_1"), 1)
		vrv.ProposedBlocks = []tmconsensus.ProposedBlock{pb1}
		vrv = sfx.Fx.UpdateVRVPrevotes(ctx, vrv, map[string][]int{
			string(pb1.Block.Hash): {1, 2},
		})

		// Set up consensus strategy expectation before mocking the response.
		cStrat := sfx.CStrat
		enterCh := cStrat.ExpectEnterRound(1, 0, nil)

		// Channel is 1-buffered, don't have to select.
		re.Response <- tmeil.RoundEntranceResponse{VRV: vrv}

		// And since we sent a VRV, the state machine calls into consensus strategy,
		// with vrv's RoundView.
		erc := gtest.ReceiveSoon(t, enterCh)
		require.Equal(t, vrv.RoundView, erc.RV)

		// And it forces the consensus strategy to consider the available proposed blocks.
		pbReq := gtest.ReceiveSoon(t, cStrat.ConsiderProposedBlocksRequests)
		require.Equal(t, []tmconsensus.ProposedBlock{pb1}, pbReq.PBs)
		require.Equal(t, []string{string(pb1.Block.Hash)}, pbReq.Reason.NewProposedBlocks)

		// Once the consensus strategy chooses a hash...
		gtest.SendSoon(t, pbReq.ChoiceHash, string(pb1.Block.Hash))

		// The state machine constructs a valid vote, saves it to the action store,
		// and sends it to the mirror.
		action := gtest.ReceiveSoon(t, re.Actions)
		// Only prevote is set.
		require.Empty(t, action.PB.Block.Hash)
		require.Empty(t, action.Precommit.Sig)

		// And because the action has been sent,
		// and we are still waiting on other prevotes to reach majority,
		// there is no active timer.
		sfx.RoundTimer.RequireNoActiveTimer(t)

		require.Equal(t, string(pb1.Block.Hash), action.Prevote.TargetHash)

		// Ensure the signature is valid, too.
		signContent, err := tmconsensus.PrevoteSignBytes(tmconsensus.VoteTarget{
			Height: 1, Round: 0,
			BlockHash: string(pb1.Block.Hash),
		}, sfx.Fx.SignatureScheme)
		require.NoError(t, err)
		require.Equal(t, signContent, action.Prevote.SignContent)
		require.True(t, sfx.Cfg.Signer.PubKey().Verify(signContent, action.Prevote.Sig))

		// Once the mirror responds with the state machine's prevote,
		// we will be at 75% prevotes in favor of one block.
		vrv = sfx.Fx.UpdateVRVPrevotes(ctx, vrv, map[string][]int{
			string(pb1.Block.Hash): {0, 1, 2},
		})
		gtest.SendSoon(t, sfx.RoundViewInCh, tmeil.StateMachineRoundView{VRV: vrv})

		// That is majority prevote for one block,
		// so the state machine expects a precommit.
		_ = gtest.ReceiveSoon(t, cStrat.DecidePrecommitRequests)
	})

	t.Run("as follower, ready to commit nil", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		sfx := tmstatetest.NewFixture(ctx, t, 4)
		sfx.Cfg.Signer = nil

		sm := sfx.NewStateMachine()
		defer sm.Wait()
		defer cancel()

		re := gtest.ReceiveSoon(t, sfx.RoundEntranceOutCh)

		vrv := sfx.EmptyVRV(1, 0)
		pb1 := sfx.Fx.NextProposedBlock([]byte("app_data_1"), 3)
		vrv.ProposedBlocks = []tmconsensus.ProposedBlock{pb1}

		// Everyone prevoted for the block.
		vrv = sfx.Fx.UpdateVRVPrevotes(ctx, vrv, map[string][]int{
			string(pb1.Block.Hash): {0, 1, 2, 3},
		})

		// But there was only one precommit for it, perhaps due to some network lag.
		vrv = sfx.Fx.UpdateVRVPrecommits(ctx, vrv, map[string][]int{
			string(pb1.Block.Hash): {3},
			"":                     {0, 1, 2},
		})

		cStrat := sfx.CStrat
		enterCh := cStrat.ExpectEnterRound(1, 0, nil)

		// Channel is 1-buffered, don't have to select.
		re.Response <- tmeil.RoundEntranceResponse{VRV: vrv}

		// Immediately after entering the round, the state machine advances to the next round due to the nil precommit.
		erc := gtest.ReceiveSoon(t, enterCh)
		require.Equal(t, vrv.RoundView, erc.RV)

		as11 := gtest.ReceiveSoon(t, sfx.RoundEntranceOutCh)
		require.Equal(t, uint64(1), as11.H)
		require.Equal(t, uint32(1), as11.R)

		// This will call EnterRound(1, 1).
		enterCh = cStrat.ExpectEnterRound(1, 1, nil)

		emptyVRV11 := sfx.EmptyVRV(1, 1)
		as11.Response <- tmeil.RoundEntranceResponse{VRV: emptyVRV11}

		erc = gtest.ReceiveSoon(t, enterCh)
		require.Equal(t, emptyVRV11.RoundView, erc.RV)
	})

	t.Run("committed block response from mirror", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		sfx := tmstatetest.NewFixture(ctx, t, 4)

		sm := sfx.NewStateMachine()
		defer sm.Wait()
		defer cancel()

		re := gtest.ReceiveSoon(t, sfx.RoundEntranceOutCh)

		// We don't need a negative assertion on the consensus strategy,
		// because it would panic if called without an ExpectEnterRound.

		pb1 := sfx.Fx.NextProposedBlock([]byte("app_data_1"), 1) // Not 0, the signer for the state machine.
		vt := tmconsensus.VoteTarget{Height: 1, Round: 0, BlockHash: string(pb1.Block.Hash)}
		sfx.Fx.CommitBlock(pb1.Block, []byte("app_state_1"), 0, map[string]gcrypto.CommonMessageSignatureProof{
			string(pb1.Block.Hash): sfx.Fx.PrecommitSignatureProof(ctx, vt, nil, []int{1, 2, 3}), // The other 3/4 validators.
		})

		pb2 := sfx.Fx.NextProposedBlock([]byte("app_data_2"), 1)

		re.Response <- tmeil.RoundEntranceResponse{
			CB: tmconsensus.CommittedBlock{
				Block: pb1.Block,
				Proof: pb2.Block.PrevCommitProof,
			},
		}

		// Now the state machine should make a finalize block request.
		req := gtest.ReceiveSoon(t, sfx.FinalizeBlockRequests)

		require.Equal(t, pb1.Block, req.Block)
		require.Zero(t, req.Round)

		require.Equal(t, 1, cap(req.Resp))

		resp := tmdriver.FinalizeBlockResponse{
			Height: 1, Round: 0,
			BlockHash: pb1.Block.Hash,

			Validators: sfx.Fx.Vals(),

			AppStateHash: []byte("app_state_1"),
		}
		gtest.SendSoon(t, req.Resp, resp)

		t.Skip("TODO: assert entry in finalization store, updated round action set sent to mirror")
	})
}

func TestStateMachine_stateTransitions(t *testing.T) {
	t.Run("from awaiting proposal", func(t *testing.T) {
		for _, tc := range []struct {
			name                  string
			externalPrevotingVals []int
		}{
			// These two cases behave the same (they don't trigger any changes).
			{name: "prevotes arrive below minority threshold", externalPrevotingVals: []int{3}},
			{name: "prevotes arrive above minority but below majority threshold", externalPrevotingVals: []int{2, 3}},
		} {
			tc := tc
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()

				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()

				sfx := tmstatetest.NewFixture(ctx, t, 4)

				sm := sfx.NewStateMachine()
				defer sm.Wait()
				defer cancel()

				re := gtest.ReceiveSoon(t, sfx.RoundEntranceOutCh)

				vrv := sfx.EmptyVRV(1, 0)

				// Set up consensus strategy expectation before mocking the response.
				cStrat := sfx.CStrat
				_ = cStrat.ExpectEnterRound(1, 0, nil)

				// Channel is 1-buffered, don't have to select.
				re.Response <- tmeil.RoundEntranceResponse{VRV: vrv}

				// We are awaiting a proposal because we started with empty state.
				// We receive one prevote for nil, only 25% so below minority threshold.
				vrv = sfx.Fx.UpdateVRVPrevotes(ctx, vrv, map[string][]int{
					"": tc.externalPrevotingVals,
				})
				gtest.SendSoon(t, sfx.RoundViewInCh, tmeil.StateMachineRoundView{VRV: vrv})

				// Then if we receive another proposed block we will consider it,
				// because we are still considered to be awaiting a proposal.
				pb := sfx.Fx.NextProposedBlock([]byte("app_data_1"), 1)
				sfx.Fx.SignProposal(ctx, &pb, 1)
				vrv = vrv.Clone()
				vrv.ProposedBlocks = []tmconsensus.ProposedBlock{pb}
				vrv.Version++
				gtest.SendSoon(t, sfx.RoundViewInCh, tmeil.StateMachineRoundView{VRV: vrv})

				considerReq := gtest.ReceiveSoon(t, cStrat.ConsiderProposedBlocksRequests)
				require.Equal(t, vrv.ProposedBlocks, considerReq.PBs)
				require.Equal(t, []string{string(pb.Block.Hash)}, considerReq.Reason.NewProposedBlocks)
			})
		}

		t.Run("majority prevotes arrive", func(t *testing.T) {
			t.Run("for nil", func(t *testing.T) {
				t.Parallel()

				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()

				sfx := tmstatetest.NewFixture(ctx, t, 4)

				sm := sfx.NewStateMachine()
				defer sm.Wait()
				defer cancel()

				re := gtest.ReceiveSoon(t, sfx.RoundEntranceOutCh)

				vrv := sfx.EmptyVRV(1, 0)

				// Set up consensus strategy expectation before mocking the response.
				cStrat := sfx.CStrat
				_ = cStrat.ExpectEnterRound(1, 0, nil)

				// Channel is 1-buffered, don't have to select.
				re.Response <- tmeil.RoundEntranceResponse{VRV: vrv}

				// We are awaiting a proposal because we started with empty state.
				// We receive one prevote for nil, only 25% so below minority threshold.
				vrv = sfx.Fx.UpdateVRVPrevotes(ctx, vrv, map[string][]int{
					"": {1, 2, 3}, // 75% in favor of nil.
				})
				gtest.SendSoon(t, sfx.RoundViewInCh, tmeil.StateMachineRoundView{VRV: vrv})

				// With 75% prevotes, we are going to immediately choose a proposed block.

				_ = gtest.ReceiveSoon(t, cStrat.ChooseProposedBlockRequests)
			})

			t.Run("but not all for one block or nil", func(t *testing.T) {
				t.Parallel()

				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()

				sfx := tmstatetest.NewFixture(ctx, t, 4)

				sm := sfx.NewStateMachine()
				defer sm.Wait()
				defer cancel()

				re := gtest.ReceiveSoon(t, sfx.RoundEntranceOutCh)

				vrv := sfx.EmptyVRV(1, 0)

				// Set up consensus strategy expectation before mocking the response.
				cStrat := sfx.CStrat
				_ = cStrat.ExpectEnterRound(1, 0, nil)

				// Channel is 1-buffered, don't have to select.
				re.Response <- tmeil.RoundEntranceResponse{VRV: vrv}

				prevoteDelayStarted := sfx.RoundTimer.PrevoteDelayStartNotification(1, 0)
				// We are awaiting a proposal because we started with empty state.
				// We receive one prevote for nil, only 25% so below minority threshold.
				pb := sfx.Fx.NextProposedBlock([]byte("app_data_1"), 1)
				sfx.Fx.SignProposal(ctx, &pb, 1)
				vrv = sfx.Fx.UpdateVRVPrevotes(ctx, vrv, map[string][]int{
					string(pb.Block.Hash): {1},
					"":                    {2, 3},
				})
				vrv.ProposedBlocks = []tmconsensus.ProposedBlock{pb}
				gtest.SendSoon(t, sfx.RoundViewInCh, tmeil.StateMachineRoundView{VRV: vrv})

				// With 75% prevotes, but not consensus, we consider the proposed block and start prevote delay.
				_ = gtest.ReceiveSoon(t, prevoteDelayStarted)
				considerReq := gtest.ReceiveSoon(t, cStrat.ConsiderProposedBlocksRequests)
				require.Equal(t, []string{string(pb.Block.Hash)}, considerReq.Reason.NewProposedBlocks)
				require.True(t, considerReq.Reason.MajorityVotingPowerPresent)
			})
		})

		t.Run("precommits arrive", func(t *testing.T) {
			t.Run("majority precommit power without consensus", func(t *testing.T) {
				t.Parallel()

				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()

				sfx := tmstatetest.NewFixture(ctx, t, 8)

				sm := sfx.NewStateMachine()
				defer sm.Wait()
				defer cancel()

				re := gtest.ReceiveSoon(t, sfx.RoundEntranceOutCh)

				vrv := sfx.EmptyVRV(1, 0)

				// Set up consensus strategy expectation before mocking the response.
				cStrat := sfx.CStrat
				_ = cStrat.ExpectEnterRound(1, 0, nil)

				// Channel is 1-buffered, don't have to select.
				re.Response <- tmeil.RoundEntranceResponse{VRV: vrv}

				precommitDelayStarted := sfx.RoundTimer.PrecommitDelayStartNotification(1, 0)
				pb := sfx.Fx.NextProposedBlock([]byte("app_data_1"), 1)
				sfx.Fx.SignProposal(ctx, &pb, 1)
				// Everyone else prevoted, including one nil prevote
				// so there is plausibility that there could be some nil precommits.
				vrv = sfx.Fx.UpdateVRVPrevotes(ctx, vrv, map[string][]int{
					string(pb.Block.Hash): {1, 2, 3, 4, 5, 6},
					"":                    {7},
				})
				vrv = sfx.Fx.UpdateVRVPrecommits(ctx, vrv, map[string][]int{
					string(pb.Block.Hash): {1, 2, 3, 4},
					"":                    {5, 6, 7},
				})
				vrv.ProposedBlocks = []tmconsensus.ProposedBlock{pb}

				gtest.SendSoon(t, sfx.RoundViewInCh, tmeil.StateMachineRoundView{VRV: vrv})

				// With 75% precommits, but not consensus, we need to decide our own precommit.
				// We do not submit a prevote.
				_ = gtest.ReceiveSoon(t, precommitDelayStarted)
				gtest.NotSending(t, cStrat.ConsiderProposedBlocksRequests)
				gtest.NotSending(t, cStrat.ChooseProposedBlockRequests)
				_ = gtest.ReceiveSoon(t, cStrat.DecidePrecommitRequests)
			})

			t.Run("majority precommit power for nil", func(t *testing.T) {
				t.Parallel()

				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()

				sfx := tmstatetest.NewFixture(ctx, t, 8)

				sm := sfx.NewStateMachine()
				defer sm.Wait()
				defer cancel()

				re := gtest.ReceiveSoon(t, sfx.RoundEntranceOutCh)

				vrv := sfx.EmptyVRV(1, 0)

				// Set up consensus strategy expectation before mocking the response.
				cStrat := sfx.CStrat
				_ = cStrat.ExpectEnterRound(1, 0, nil)

				// Channel is 1-buffered, don't have to select.
				re.Response <- tmeil.RoundEntranceResponse{VRV: vrv}

				vrv = sfx.Fx.UpdateVRVPrevotes(ctx, vrv, map[string][]int{
					"": {1, 2, 3, 4, 5, 6, 7},
				})
				vrv = sfx.Fx.UpdateVRVPrecommits(ctx, vrv, map[string][]int{
					"": {1, 2, 3, 4, 5, 6},
				})

				gtest.SendSoon(t, sfx.RoundViewInCh, tmeil.StateMachineRoundView{VRV: vrv})

				// Upon receiving the 75% precommit for nil,
				// the state machine advances the round.
				// For now, it doesn't consult the consensus strategy about a precommit.
				// That will likely change in the future.
				erc11Ch := cStrat.ExpectEnterRound(1, 1, nil)
				gtest.NotSending(t, cStrat.DecidePrecommitRequests)

				re = gtest.ReceiveSoon(t, sfx.RoundEntranceOutCh)
				require.Equal(t, uint64(1), re.H)
				require.Equal(t, uint32(1), re.R)

				re.Response <- tmeil.RoundEntranceResponse{VRV: sfx.EmptyVRV(1, 1)}
				_ = gtest.ReceiveSoon(t, erc11Ch)
			})

			t.Run("majority precommit power for particular block", func(t *testing.T) {
				t.Parallel()

				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()

				sfx := tmstatetest.NewFixture(ctx, t, 8)

				sm := sfx.NewStateMachine()
				defer sm.Wait()
				defer cancel()

				re := gtest.ReceiveSoon(t, sfx.RoundEntranceOutCh)

				vrv := sfx.EmptyVRV(1, 0)

				// Set up consensus strategy expectation before mocking the response.
				cStrat := sfx.CStrat
				_ = cStrat.ExpectEnterRound(1, 0, nil)

				// Channel is 1-buffered, don't have to select.
				re.Response <- tmeil.RoundEntranceResponse{VRV: vrv}

				pb := sfx.Fx.NextProposedBlock([]byte("app_data_1"), 1)
				sfx.Fx.SignProposal(ctx, &pb, 1)
				vrv = sfx.Fx.UpdateVRVPrevotes(ctx, vrv, map[string][]int{
					string(pb.Block.Hash): {1, 2, 3, 4, 5, 6, 7},
				})
				vrv = sfx.Fx.UpdateVRVPrecommits(ctx, vrv, map[string][]int{
					string(pb.Block.Hash): {1, 2, 3, 4, 5, 6},
				})
				vrv.ProposedBlocks = []tmconsensus.ProposedBlock{pb}

				gtest.SendSoon(t, sfx.RoundViewInCh, tmeil.StateMachineRoundView{VRV: vrv})

				// For now, we don't submit our own precommit because we are jumping ahead.
				// That will probably change in the future.
				finReq := gtest.ReceiveSoon(t, sfx.FinalizeBlockRequests)
				require.Equal(t, pb.Block, finReq.Block)
				require.Zero(t, finReq.Round)
			})

			t.Run("above minority precommit power but below majority", func(t *testing.T) {
				t.Parallel()

				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()

				sfx := tmstatetest.NewFixture(ctx, t, 8)

				sm := sfx.NewStateMachine()
				defer sm.Wait()
				defer cancel()

				re := gtest.ReceiveSoon(t, sfx.RoundEntranceOutCh)

				vrv := sfx.EmptyVRV(1, 0)

				// Set up consensus strategy expectation before mocking the response.
				cStrat := sfx.CStrat
				_ = cStrat.ExpectEnterRound(1, 0, nil)

				// Channel is 1-buffered, don't have to select.
				re.Response <- tmeil.RoundEntranceResponse{VRV: vrv}

				pb := sfx.Fx.NextProposedBlock([]byte("app_data_1"), 1)
				sfx.Fx.SignProposal(ctx, &pb, 1)
				vrv = sfx.Fx.UpdateVRVPrevotes(ctx, vrv, map[string][]int{
					string(pb.Block.Hash): {1, 2, 3, 4, 5, 6, 7},
				})
				vrv = sfx.Fx.UpdateVRVPrecommits(ctx, vrv, map[string][]int{
					// Mixed precommit power, 3/8 is 37.5%, above the minority.
					string(pb.Block.Hash): {1, 2},
					"":                    {3},
				})
				vrv.ProposedBlocks = []tmconsensus.ProposedBlock{pb}

				gtest.SendSoon(t, sfx.RoundViewInCh, tmeil.StateMachineRoundView{VRV: vrv})

				// Even though we haven't sent our own prevote,
				// the rest of the network clearly wasn't waiting for us.
				// So it is time to submit our own precommit
				// based on whatever information we have so far.
				pReq := gtest.ReceiveSoon(t, cStrat.DecidePrecommitRequests)
				require.Equal(t, vrv.VoteSummary, pReq.Input)
			})
		})
	})

	t.Run("from prevoting, precommits arrive", func(t *testing.T) {
		t.Run("majority precommit power without consensus", func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			sfx := tmstatetest.NewFixture(ctx, t, 8)

			sm := sfx.NewStateMachine()
			defer sm.Wait()
			defer cancel()

			re := gtest.ReceiveSoon(t, sfx.RoundEntranceOutCh)

			// Set up consensus strategy expectation before mocking the response.
			cStrat := sfx.CStrat
			_ = cStrat.ExpectEnterRound(1, 0, nil)

			vrv := sfx.EmptyVRV(1, 0)
			pb := sfx.Fx.NextProposedBlock([]byte("app_data_1"), 1)
			sfx.Fx.SignProposal(ctx, &pb, 1)
			vrv.ProposedBlocks = []tmconsensus.ProposedBlock{pb}
			vrv.Version++
			re.Response <- tmeil.RoundEntranceResponse{VRV: vrv}

			// The initial state had a proposed block,
			// so there was a consider request.
			cReq := gtest.ReceiveSoon(t, cStrat.ConsiderProposedBlocksRequests)
			require.Equal(t, []string{string(pb.Block.Hash)}, cReq.Reason.NewProposedBlocks)
			gtest.SendSoon(t, cReq.ChoiceHash, string(pb.Block.Hash))

			// The mirror sends back our own prevote but nobody else's yet.
			vrv = sfx.Fx.UpdateVRVPrevotes(ctx, vrv, map[string][]int{
				string(pb.Block.Hash): {0},
			})
			gtest.SendSoon(t, sfx.RoundViewInCh, tmeil.StateMachineRoundView{VRV: vrv})

			// Next there is an update with a mix of precommits.
			// We didn't see the prevotes but we don't need them at this point.
			precommitDelayStarted := sfx.RoundTimer.PrecommitDelayStartNotification(1, 0)
			vrv = sfx.Fx.UpdateVRVPrecommits(ctx, vrv, map[string][]int{
				string(pb.Block.Hash): {1, 2, 3},
				"":                    {4, 5, 6},
			})
			gtest.SendSoon(t, sfx.RoundViewInCh, tmeil.StateMachineRoundView{VRV: vrv})

			// This update causes both the precommit delay to start
			// and a precommit decision request.
			_ = gtest.ReceiveSoon(t, precommitDelayStarted)
			gtest.NotSending(t, cStrat.ConsiderProposedBlocksRequests)
			gtest.NotSending(t, cStrat.ChooseProposedBlockRequests)
			_ = gtest.ReceiveSoon(t, cStrat.DecidePrecommitRequests)
		})

		t.Run("majority precommit power for nil", func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			sfx := tmstatetest.NewFixture(ctx, t, 8)

			sm := sfx.NewStateMachine()
			defer sm.Wait()
			defer cancel()

			re := gtest.ReceiveSoon(t, sfx.RoundEntranceOutCh)

			// Set up consensus strategy expectation before mocking the response.
			cStrat := sfx.CStrat
			_ = cStrat.ExpectEnterRound(1, 0, nil)

			vrv := sfx.EmptyVRV(1, 0)
			pb := sfx.Fx.NextProposedBlock([]byte("app_data_1"), 1)
			sfx.Fx.SignProposal(ctx, &pb, 1)
			vrv.ProposedBlocks = []tmconsensus.ProposedBlock{pb}
			vrv.Version++
			re.Response <- tmeil.RoundEntranceResponse{VRV: vrv}

			// The initial state had a proposed block,
			// so there was a consider request.
			// In this case we prevote nil.
			cReq := gtest.ReceiveSoon(t, cStrat.ConsiderProposedBlocksRequests)
			require.Equal(t, []string{string(pb.Block.Hash)}, cReq.Reason.NewProposedBlocks)
			gtest.SendSoon(t, cReq.ChoiceHash, "")

			// The mirror sends back our own prevote but nobody else's yet.
			vrv = sfx.Fx.UpdateVRVPrevotes(ctx, vrv, map[string][]int{
				"": {0},
			})
			gtest.SendSoon(t, sfx.RoundViewInCh, tmeil.StateMachineRoundView{VRV: vrv})

			// Now there is an update with majority but not 100% precommits for nil.
			vrv = sfx.Fx.UpdateVRVPrecommits(ctx, vrv, map[string][]int{
				"": {0, 1, 2, 3, 4, 5},
			})

			erc11Ch := cStrat.ExpectEnterRound(1, 1, nil)
			gtest.SendSoon(t, sfx.RoundViewInCh, tmeil.StateMachineRoundView{VRV: vrv})

			// For now, we don't submit our own precommit because we are jumping ahead.
			// That will probably change in the future.

			// Round transition, so the state machine makes a new request to the mirror.
			re = gtest.ReceiveSoon(t, sfx.RoundEntranceOutCh)
			require.Equal(t, uint64(1), re.H)
			require.Equal(t, uint32(1), re.R)
			re.Response <- tmeil.RoundEntranceResponse{VRV: sfx.EmptyVRV(1, 1)}

			_ = gtest.ReceiveSoon(t, erc11Ch)
			gtest.NotSending(t, cStrat.ConsiderProposedBlocksRequests)
			gtest.NotSending(t, cStrat.ChooseProposedBlockRequests)
			gtest.NotSending(t, cStrat.DecidePrecommitRequests)
		})

		t.Run("majority precommit power for a block", func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			sfx := tmstatetest.NewFixture(ctx, t, 8)

			sm := sfx.NewStateMachine()
			defer sm.Wait()
			defer cancel()

			re := gtest.ReceiveSoon(t, sfx.RoundEntranceOutCh)

			// Set up consensus strategy expectation before mocking the response.
			cStrat := sfx.CStrat
			_ = cStrat.ExpectEnterRound(1, 0, nil)

			vrv := sfx.EmptyVRV(1, 0)
			pb := sfx.Fx.NextProposedBlock([]byte("app_data_1"), 1)
			sfx.Fx.SignProposal(ctx, &pb, 1)
			vrv.ProposedBlocks = []tmconsensus.ProposedBlock{pb}
			vrv.Version++
			re.Response <- tmeil.RoundEntranceResponse{VRV: vrv}

			// The initial state had a proposed block,
			// so there was a consider request.
			// In this case we prevote nil.
			cReq := gtest.ReceiveSoon(t, cStrat.ConsiderProposedBlocksRequests)
			require.Equal(t, []string{string(pb.Block.Hash)}, cReq.Reason.NewProposedBlocks)
			gtest.SendSoon(t, cReq.ChoiceHash, "")

			// The mirror sends back our own prevote but nobody else's yet.
			vrv = sfx.Fx.UpdateVRVPrevotes(ctx, vrv, map[string][]int{
				string(pb.Block.Hash): {0},
			})
			gtest.SendSoon(t, sfx.RoundViewInCh, tmeil.StateMachineRoundView{VRV: vrv})

			// Now there is an update with majority but not 100% precommits for the block.
			vrv = sfx.Fx.UpdateVRVPrecommits(ctx, vrv, map[string][]int{
				string(pb.Block.Hash): {0, 1, 2, 3, 4, 5},
			})

			gtest.SendSoon(t, sfx.RoundViewInCh, tmeil.StateMachineRoundView{VRV: vrv})

			// For now, we don't submit our own precommit because we are jumping ahead.
			// That will probably change in the future.

			// Receiving that view update begins a finalization.
			finReq := gtest.ReceiveSoon(t, sfx.FinalizeBlockRequests)
			require.Equal(t, pb.Block, finReq.Block)
			require.Zero(t, finReq.Round)
		})
	})
}

func TestStateMachine_enterRoundProposal(t *testing.T) {
	t.Run("annotations on proposal", func(t *testing.T) {
		for _, tc := range tmconsensustest.AnnotationCombinations() {
			tc := tc
			t.Run(tc.Name, func(t *testing.T) {
				t.Parallel()

				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()

				sfx := tmstatetest.NewFixture(ctx, t, 4)

				sm := sfx.NewStateMachine()
				defer sm.Wait()
				defer cancel()

				re := gtest.ReceiveSoon(t, sfx.RoundEntranceOutCh)

				vrv := sfx.EmptyVRV(1, 0)

				// Set up consensus strategy expectation before mocking the response.
				cStrat := sfx.CStrat
				ercCh := cStrat.ExpectEnterRound(1, 0, nil)

				// Channel is 1-buffered, don't have to select.
				re.Response <- tmeil.RoundEntranceResponse{VRV: vrv}

				erc := gtest.ReceiveSoon(t, ercCh)

				require.Equal(t, 1, cap(erc.ProposalOut))
				erc.ProposalOut <- tmconsensus.Proposal{
					DataID:              "app_data",
					ProposalAnnotations: tc.Annotations,
				}

				// Synchronize on the action output.
				sentPB := gtest.ReceiveSoon(t, re.Actions).PB

				// Now the proposed block should be in the action store.
				ra, err := sfx.Cfg.ActionStore.Load(ctx, 1, 0)
				require.NoError(t, err)

				gotPB := ra.ProposedBlock
				require.Equal(t, sentPB, gotPB)
				require.Equal(t, tc.Annotations, gotPB.Annotations)
			})
		}
	})

	t.Run("annotations on block", func(t *testing.T) {
		for _, tc := range tmconsensustest.AnnotationCombinations() {
			tc := tc
			t.Run(tc.Name, func(t *testing.T) {
				t.Parallel()

				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()

				sfx := tmstatetest.NewFixture(ctx, t, 4)

				sm := sfx.NewStateMachine()
				defer sm.Wait()
				defer cancel()

				re := gtest.ReceiveSoon(t, sfx.RoundEntranceOutCh)

				vrv := sfx.EmptyVRV(1, 0)

				// Set up consensus strategy expectation before mocking the response.
				cStrat := sfx.CStrat
				ercCh := cStrat.ExpectEnterRound(1, 0, nil)

				// Channel is 1-buffered, don't have to select.
				re.Response <- tmeil.RoundEntranceResponse{VRV: vrv}

				erc := gtest.ReceiveSoon(t, ercCh)

				require.Equal(t, 1, cap(erc.ProposalOut))
				erc.ProposalOut <- tmconsensus.Proposal{
					DataID:           "app_data",
					BlockAnnotations: tc.Annotations,
				}

				// Synchronize on the action output.
				sentPB := gtest.ReceiveSoon(t, re.Actions).PB

				// Now the proposed block should be in the action store.
				ra, err := sfx.Cfg.ActionStore.Load(ctx, 1, 0)
				require.NoError(t, err)

				gotPB := ra.ProposedBlock
				require.Equal(t, sentPB, gotPB)
				require.Equal(t, tc.Annotations, gotPB.Block.Annotations)
			})
		}
	})
}

func TestStateMachine_proposedBlockFiltering(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in short mode due to many sleeps")
	}

	for _, tc := range tmstatetest.UnacceptableProposedBlockMutations(4, 4) {
		tc := tc
		t.Run("on initial height and round entrance", func(t *testing.T) {
			t.Run("when the only proposed block is unacceptable", func(t *testing.T) {
				t.Run(tc.Name, func(t *testing.T) {
					t.Parallel()

					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()

					sfx := tmstatetest.NewFixture(ctx, t, 4)

					sm := sfx.NewStateMachine()
					defer sm.Wait()
					defer cancel()

					re := gtest.ReceiveSoon(t, sfx.RoundEntranceOutCh)

					vrv := sfx.EmptyVRV(1, 0)

					// A different validator produces a proposed block.
					pb1 := sfx.Fx.NextProposedBlock([]byte("app_data_1"), 3)

					// But, something is wrong with the proposed block.
					tc.Mutate(&pb1)

					vrv.ProposedBlocks = []tmconsensus.ProposedBlock{pb1}

					cStrat := sfx.CStrat
					_ = cStrat.ExpectEnterRound(1, 0, nil)

					re.Response <- tmeil.RoundEntranceResponse{VRV: vrv}

					gtest.NotSendingSoon(t, cStrat.ConsiderProposedBlocksRequests)
				})
			})

			t.Run("when one proposed block is unacceptable and another is fine", func(t *testing.T) {
				t.Run(tc.Name, func(t *testing.T) {
					t.Parallel()

					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()

					sfx := tmstatetest.NewFixture(ctx, t, 4)

					sm := sfx.NewStateMachine()
					defer sm.Wait()
					defer cancel()

					re := gtest.ReceiveSoon(t, sfx.RoundEntranceOutCh)

					vrv := sfx.EmptyVRV(1, 0)

					// Other validators produce proposed blocks.
					pbGood := sfx.Fx.NextProposedBlock([]byte("app_data_1_2"), 2)
					pbBad := sfx.Fx.NextProposedBlock([]byte("app_data_1_3"), 3)

					// But, something is wrong with the proposed block.
					tc.Mutate(&pbBad)

					vrv.ProposedBlocks = []tmconsensus.ProposedBlock{pbGood, pbBad}

					cStrat := sfx.CStrat
					_ = cStrat.ExpectEnterRound(1, 0, nil)

					re.Response <- tmeil.RoundEntranceResponse{VRV: vrv}

					req := gtest.ReceiveSoon(t, cStrat.ConsiderProposedBlocksRequests)
					require.Equal(t, []tmconsensus.ProposedBlock{pbGood}, req.PBs)
					require.Equal(t, []string{string(pbGood.Block.Hash)}, req.Reason.NewProposedBlocks)
				})
			})
		})

		t.Run("on first new view update at initial height", func(t *testing.T) {
			t.Run("when the first update is only proposal data", func(t *testing.T) {
				t.Run("when the only proposed block is unacceptable", func(t *testing.T) {
					t.Run(tc.Name, func(t *testing.T) {
						t.Parallel()

						ctx, cancel := context.WithCancel(context.Background())
						defer cancel()

						sfx := tmstatetest.NewFixture(ctx, t, 4)

						sm := sfx.NewStateMachine()
						defer sm.Wait()
						defer cancel()

						re := gtest.ReceiveSoon(t, sfx.RoundEntranceOutCh)

						vrv := sfx.EmptyVRV(1, 0)

						cStrat := sfx.CStrat
						_ = cStrat.ExpectEnterRound(1, 0, nil)

						re.Response <- tmeil.RoundEntranceResponse{VRV: vrv}

						// Now a new round view arrives, with only one invalid block.
						pb1 := sfx.Fx.NextProposedBlock([]byte("app_data_1"), 3)
						tc.Mutate(&pb1)
						vrv.ProposedBlocks = []tmconsensus.ProposedBlock{pb1}
						vrv.Version++

						gtest.SendSoon(t, sfx.RoundViewInCh, tmeil.StateMachineRoundView{VRV: vrv})

						sfx.RoundTimer.RequireActiveProposalTimer(t, 1, 0)

						// Elapse the timer.
						require.NoError(t, sfx.RoundTimer.ElapseProposalTimer(1, 0))

						// Because the proposed block was a mismatch,
						// there is no call to ConsiderProposedBlocksRequests.
						gtest.NotSending(t, cStrat.ConsiderProposedBlocksRequests)

						// But following the timer elapsing, there is a request to choose a proposed block;
						// however, the input set of proposed blocks is empty.
						choosePBReq := gtest.ReceiveSoon(t, cStrat.ChooseProposedBlockRequests)
						require.Empty(t, choosePBReq.Input)
					})
				})

				t.Run("when one proposed block is unacceptable and another is fine", func(t *testing.T) {
					t.Run(tc.Name, func(t *testing.T) {
						t.Parallel()

						ctx, cancel := context.WithCancel(context.Background())
						defer cancel()

						sfx := tmstatetest.NewFixture(ctx, t, 4)

						sm := sfx.NewStateMachine()
						defer sm.Wait()
						defer cancel()

						re := gtest.ReceiveSoon(t, sfx.RoundEntranceOutCh)

						vrv := sfx.EmptyVRV(1, 0)

						cStrat := sfx.CStrat
						_ = cStrat.ExpectEnterRound(1, 0, nil)

						re.Response <- tmeil.RoundEntranceResponse{VRV: vrv}

						// Now a new round view arrives, with one good and one bad proposed block.
						pbGood := sfx.Fx.NextProposedBlock([]byte("app_data_1_2"), 2)
						pbBad := sfx.Fx.NextProposedBlock([]byte("app_data_1_3"), 3)

						// But, something is wrong with the proposed block.
						tc.Mutate(&pbBad)

						vrv.ProposedBlocks = []tmconsensus.ProposedBlock{pbGood, pbBad}
						vrv.Version++

						gtest.SendSoon(t, sfx.RoundViewInCh, tmeil.StateMachineRoundView{VRV: vrv})

						sfx.RoundTimer.RequireActiveProposalTimer(t, 1, 0)

						// There is a consider request with only the good proposed block.
						req := gtest.ReceiveSoon(t, cStrat.ConsiderProposedBlocksRequests)
						require.Equal(t, []tmconsensus.ProposedBlock{pbGood}, req.PBs)
						require.Equal(t, []string{string(pbGood.Block.Hash)}, req.Reason.NewProposedBlocks)

						// But the consensus strategy isn't ready to decide yet.
						gtest.SendSoon(t, req.ChoiceError, tmconsensus.ErrProposedBlockChoiceNotReady)

						// Elapse the timer.
						require.NoError(t, sfx.RoundTimer.ElapseProposalTimer(1, 0))

						// But following the timer elapsing, there is a request to choose a proposed block;
						// however, the input set of proposed blocks is empty.
						choosePBReq := gtest.ReceiveSoon(t, cStrat.ChooseProposedBlockRequests)
						require.Equal(t, []tmconsensus.ProposedBlock{pbGood}, choosePBReq.Input)
					})
				})
			})

			t.Run("when the first update is proposal data and some prevotes", func(t *testing.T) {
				t.Run("majority prevotes without consensus", func(t *testing.T) {
					t.Run("the only proposed block is unacceptable", func(t *testing.T) {
						t.Parallel()

						ctx, cancel := context.WithCancel(context.Background())
						defer cancel()

						sfx := tmstatetest.NewFixture(ctx, t, 4)

						sm := sfx.NewStateMachine()
						defer sm.Wait()
						defer cancel()

						re := gtest.ReceiveSoon(t, sfx.RoundEntranceOutCh)

						vrv := sfx.EmptyVRV(1, 0)

						cStrat := sfx.CStrat
						_ = cStrat.ExpectEnterRound(1, 0, nil)

						re.Response <- tmeil.RoundEntranceResponse{VRV: vrv}

						// Now a new round view arrives.
						// It has one invalid proposed block.
						pb1 := sfx.Fx.NextProposedBlock([]byte("app_data_1"), 3)
						tc.Mutate(&pb1)
						vrv.ProposedBlocks = []tmconsensus.ProposedBlock{pb1}
						// And 3/4 prevotes are present, so we have crossed majority voting power,
						// but there is not consensus among the votes.
						vrv = sfx.Fx.UpdateVRVPrevotes(ctx, vrv, map[string][]int{
							"":                     {1, 2},
							string(pb1.Block.Hash): {3},
						})
						prevotingCh := sfx.RoundTimer.PrevoteDelayStartNotification(1, 0)

						gtest.SendSoon(t, sfx.RoundViewInCh, tmeil.StateMachineRoundView{VRV: vrv})

						// This means we should be in prevote delay.
						_ = gtest.ReceiveSoon(t, prevotingCh)

						// Immediately after the state machine starts the prevote timer,
						// it sends a consider proposed blocks request.
						// But in this case, we expect no send,
						// because the only proposed block was unacceptable.
						// We still do a long check here, to reduce flakiness if running on a loaded machine.
						gtest.NotSendingSoon(t, cStrat.ConsiderProposedBlocksRequests)
					})

					t.Run("one unacceptable and one acceptable proposed block", func(t *testing.T) {
						t.Parallel()

						ctx, cancel := context.WithCancel(context.Background())
						defer cancel()

						sfx := tmstatetest.NewFixture(ctx, t, 4)

						sm := sfx.NewStateMachine()
						defer sm.Wait()
						defer cancel()

						re := gtest.ReceiveSoon(t, sfx.RoundEntranceOutCh)

						vrv := sfx.EmptyVRV(1, 0)

						cStrat := sfx.CStrat
						_ = cStrat.ExpectEnterRound(1, 0, nil)

						re.Response <- tmeil.RoundEntranceResponse{VRV: vrv}

						// Now a new round view arrives.
						// It has one invalid proposed block.
						pbBad := sfx.Fx.NextProposedBlock([]byte("app_data_1_3"), 3)
						tc.Mutate(&pbBad)
						pbGood := sfx.Fx.NextProposedBlock([]byte("app_data_1_2"), 2)
						vrv.ProposedBlocks = []tmconsensus.ProposedBlock{pbBad, pbGood}
						// And 3/4 prevotes are present, so we have crossed majority voting power,
						// but there is not consensus among the votes.
						vrv = sfx.Fx.UpdateVRVPrevotes(ctx, vrv, map[string][]int{
							string(pbGood.Block.Hash): {1, 2},
							string(pbBad.Block.Hash):  {3},
						})
						prevotingCh := sfx.RoundTimer.PrevoteDelayStartNotification(1, 0)

						gtest.SendSoon(t, sfx.RoundViewInCh, tmeil.StateMachineRoundView{VRV: vrv})

						// This means we should be in prevote delay.
						_ = gtest.ReceiveSoon(t, prevotingCh)

						// Now we do receive a consider request.
						considerReq := gtest.ReceiveSoon(t, cStrat.ConsiderProposedBlocksRequests)
						require.Equal(t, []tmconsensus.ProposedBlock{pbGood}, considerReq.PBs)
						require.Equal(t, []string{string(pbGood.Block.Hash)}, considerReq.Reason.NewProposedBlocks)
						require.True(t, considerReq.Reason.MajorityVotingPowerPresent)
					})
				})

				t.Run("majority prevotes with consensus", func(t *testing.T) {
					t.Run("the only proposed block is unacceptable", func(t *testing.T) {
						t.Parallel()

						ctx, cancel := context.WithCancel(context.Background())
						defer cancel()

						sfx := tmstatetest.NewFixture(ctx, t, 4)

						sm := sfx.NewStateMachine()
						defer sm.Wait()
						defer cancel()

						re := gtest.ReceiveSoon(t, sfx.RoundEntranceOutCh)

						vrv := sfx.EmptyVRV(1, 0)

						cStrat := sfx.CStrat
						_ = cStrat.ExpectEnterRound(1, 0, nil)

						re.Response <- tmeil.RoundEntranceResponse{VRV: vrv}

						// Now a new round view arrives.
						// It has one invalid proposed block.
						pb1 := sfx.Fx.NextProposedBlock([]byte("app_data_1"), 3)
						tc.Mutate(&pb1)
						vrv.ProposedBlocks = []tmconsensus.ProposedBlock{pb1}
						vrv = sfx.Fx.UpdateVRVPrevotes(ctx, vrv, map[string][]int{
							// 3/4 prevotes for nil here.
							// Weird to have the proposer vote nil, but acceptable.
							"": {1, 2, 3},
						})

						gtest.SendSoon(t, sfx.RoundViewInCh, tmeil.StateMachineRoundView{VRV: vrv})

						// Since prevoting is over, we force a choose, not a consider.
						chooseReq := gtest.ReceiveSoon(t, cStrat.ChooseProposedBlockRequests)
						require.Empty(t, chooseReq.Input)
					})

					t.Run("one unacceptable and one acceptable proposed block", func(t *testing.T) {
						t.Parallel()

						ctx, cancel := context.WithCancel(context.Background())
						defer cancel()

						sfx := tmstatetest.NewFixture(ctx, t, 4)

						sm := sfx.NewStateMachine()
						defer sm.Wait()
						defer cancel()

						re := gtest.ReceiveSoon(t, sfx.RoundEntranceOutCh)

						vrv := sfx.EmptyVRV(1, 0)

						cStrat := sfx.CStrat
						_ = cStrat.ExpectEnterRound(1, 0, nil)

						re.Response <- tmeil.RoundEntranceResponse{VRV: vrv}

						// Now a new round view arrives.
						// It has one invalid proposed block.
						pbBad := sfx.Fx.NextProposedBlock([]byte("app_data_1_3"), 3)
						tc.Mutate(&pbBad)
						pbGood := sfx.Fx.NextProposedBlock([]byte("app_data_1_2"), 2)
						vrv.ProposedBlocks = []tmconsensus.ProposedBlock{pbBad, pbGood}
						vrv = sfx.Fx.UpdateVRVPrevotes(ctx, vrv, map[string][]int{
							// 3/4 prevotes for the good block.
							string(pbGood.Block.Hash): {1, 2, 3},
						})

						gtest.SendSoon(t, sfx.RoundViewInCh, tmeil.StateMachineRoundView{VRV: vrv})

						// Since prevoting is over, we force a choose, not a consider.
						chooseReq := gtest.ReceiveSoon(t, cStrat.ChooseProposedBlockRequests)
						require.Equal(t, []tmconsensus.ProposedBlock{pbGood}, chooseReq.Input)
					})
				})
			})
		})

		t.Run("on entering the second round at initial height", func(t *testing.T) {
			t.Run("when the only proposed block is unacceptable", func(t *testing.T) {
				t.Run(tc.Name, func(t *testing.T) {
					t.Parallel()

					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()

					sfx := tmstatetest.NewFixture(ctx, t, 4)

					sm := sfx.NewStateMachine()
					defer sm.Wait()
					defer cancel()

					re10 := gtest.ReceiveSoon(t, sfx.RoundEntranceOutCh)
					require.Equal(t, uint64(1), re10.H)
					require.Zero(t, re10.R)

					// Everyone else already prevoted and precommitted for nil.
					vrv := sfx.EmptyVRV(1, 0)
					vrv = sfx.Fx.UpdateVRVPrevotes(ctx, vrv, map[string][]int{
						"": {1, 2, 3},
					})
					vrv = sfx.Fx.UpdateVRVPrecommits(ctx, vrv, map[string][]int{
						"": {1, 2, 3}, // Everyone else already prevoted for the block.
					})

					cStrat := sfx.CStrat
					_ = cStrat.ExpectEnterRound(1, 0, nil)
					_ = cStrat.ExpectEnterRound(1, 1, nil)

					re10.Response <- tmeil.RoundEntranceResponse{VRV: vrv}

					re11 := gtest.ReceiveSoon(t, sfx.RoundEntranceOutCh)
					require.Equal(t, uint64(1), re11.H)
					require.Equal(t, uint32(1), re11.R)

					vrv = sfx.EmptyVRV(1, 1)
					pb11 := sfx.Fx.NextProposedBlock([]byte("app_data_1"), 3)
					pb11.Round = 1
					tc.Mutate(&pb11)
					vrv.ProposedBlocks = []tmconsensus.ProposedBlock{pb11}

					re11.Response <- tmeil.RoundEntranceResponse{VRV: vrv}

					// The only proposed block was filtered out,
					// so nothing is sending on the consider channel.
					gtest.NotSendingSoon(t, cStrat.ConsiderProposedBlocksRequests)
				})
			})

			t.Run("when one proposed block is unacceptable and another is fine", func(t *testing.T) {
				t.Run(tc.Name, func(t *testing.T) {
					t.Parallel()

					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()

					sfx := tmstatetest.NewFixture(ctx, t, 4)

					sm := sfx.NewStateMachine()
					defer sm.Wait()
					defer cancel()

					re10 := gtest.ReceiveSoon(t, sfx.RoundEntranceOutCh)
					require.Equal(t, uint64(1), re10.H)
					require.Zero(t, re10.R)

					// Everyone else already prevoted and precommitted for nil.
					vrv := sfx.EmptyVRV(1, 0)
					vrv = sfx.Fx.UpdateVRVPrevotes(ctx, vrv, map[string][]int{
						"": {1, 2, 3},
					})
					vrv = sfx.Fx.UpdateVRVPrecommits(ctx, vrv, map[string][]int{
						"": {1, 2, 3}, // Everyone else already prevoted for the block.
					})

					cStrat := sfx.CStrat
					_ = cStrat.ExpectEnterRound(1, 0, nil)
					_ = cStrat.ExpectEnterRound(1, 1, nil)

					re10.Response <- tmeil.RoundEntranceResponse{VRV: vrv}

					re11 := gtest.ReceiveSoon(t, sfx.RoundEntranceOutCh)
					require.Equal(t, uint64(1), re11.H)
					require.Equal(t, uint32(1), re11.R)

					vrv = sfx.EmptyVRV(1, 1)

					pb11Bad := sfx.Fx.NextProposedBlock([]byte("app_data_1_3"), 3)
					pb11Bad.Round = 1
					tc.Mutate(&pb11Bad)

					pb11Good := sfx.Fx.NextProposedBlock([]byte("app_data_1_2"), 2)
					pb11Good.Round = 1

					vrv.ProposedBlocks = []tmconsensus.ProposedBlock{pb11Bad, pb11Good}

					re11.Response <- tmeil.RoundEntranceResponse{VRV: vrv}

					// The mutated proposed block is filtered out,
					// but the good one is included.
					considerReq := gtest.ReceiveSoon(t, cStrat.ConsiderProposedBlocksRequests)
					require.Equal(t, []tmconsensus.ProposedBlock{pb11Good}, considerReq.PBs)
					require.Equal(t, []string{string(pb11Good.Block.Hash)}, considerReq.Reason.NewProposedBlocks)
				})
			})
		})
	}
}

func TestStateMachine_decidePrecommit(t *testing.T) {
	t.Run("majority prevotes at initialization", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		sfx := tmstatetest.NewFixture(ctx, t, 4)

		sm := sfx.NewStateMachine()
		defer sm.Wait()
		defer cancel()

		re := gtest.ReceiveSoon(t, sfx.RoundEntranceOutCh)

		vrv := sfx.EmptyVRV(1, 0)
		pb1 := sfx.Fx.NextProposedBlock([]byte("app_data_1"), 1)
		vrv.ProposedBlocks = []tmconsensus.ProposedBlock{pb1}
		vrv = sfx.Fx.UpdateVRVPrevotes(ctx, vrv, map[string][]int{
			string(pb1.Block.Hash): {1, 2, 3}, // Everyone else already prevoted for the block.
		})

		// Set up consensus strategy expectation before mocking the response.
		cStrat := sfx.CStrat
		_ = cStrat.ExpectEnterRound(1, 0, nil)

		// Channel is 1-buffered, don't have to select.
		re.Response <- tmeil.RoundEntranceResponse{VRV: vrv}

		// Since there was already a majority prevote for a block,
		// we don't need to submit our prevote -- we jump straight into the precommit decision.
		cReq := gtest.ReceiveSoon(t, cStrat.DecidePrecommitRequests)
		require.Equal(t, vrv.VoteSummary, cReq.Input)

		// And when the consensus strategy responds, the state machine forwards it to the mirror.
		gtest.SendSoon(t, cReq.ChoiceHash, string(pb1.Block.Hash))

		act := gtest.ReceiveSoon(t, re.Actions)
		require.NotEmpty(t, act.Precommit.Sig)

		precommitSignContent, err := tmconsensus.PrecommitSignBytes(tmconsensus.VoteTarget{
			Height: 1, Round: 0,
			BlockHash: string(pb1.Block.Hash),
		}, sfx.Fx.SignatureScheme)
		require.NoError(t, err)
		require.Equal(t, precommitSignContent, act.Precommit.SignContent)
		require.True(t, sfx.Cfg.Signer.PubKey().Verify(act.Precommit.SignContent, act.Precommit.Sig))

		// And at that point, it is present in the action store too.
		ra, err := sfx.Cfg.ActionStore.Load(ctx, 1, 0)
		require.NoError(t, err)
		require.Equal(t, string(pb1.Block.Hash), ra.PrecommitTarget)
		require.Equal(t, string(act.Precommit.Sig), ra.PrecommitSignature)
	})

	t.Run("after prevote delay elapses", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		sfx := tmstatetest.NewFixture(ctx, t, 4)

		sm := sfx.NewStateMachine()
		defer sm.Wait()
		defer cancel()

		re := gtest.ReceiveSoon(t, sfx.RoundEntranceOutCh)

		// Set up consensus strategy expectation before mocking the response.
		cStrat := sfx.CStrat
		_ = cStrat.ExpectEnterRound(1, 0, nil)

		prevoteDelayTimerStarted := sfx.RoundTimer.PrevoteDelayStartNotification(1, 0)

		// Initial state has the proposed block.
		vrv := sfx.EmptyVRV(1, 0)
		pb1 := sfx.Fx.NextProposedBlock([]byte("app_data_1"), 3)
		vrv.ProposedBlocks = []tmconsensus.ProposedBlock{pb1}
		// Channel is 1-buffered, don't have to select.
		re.Response <- tmeil.RoundEntranceResponse{VRV: vrv} // No PrevBlockHash at initial height.

		// This causes a Consider request to the consensus strategy,
		// and we will prevote for the block.
		considerReq := gtest.ReceiveSoon(t, cStrat.ConsiderProposedBlocksRequests)
		gtest.SendSoon(t, considerReq.ChoiceHash, string(pb1.Block.Hash))
		require.Equal(t, []string{string(pb1.Block.Hash)}, considerReq.Reason.NewProposedBlocks)

		// The choice is sent to the mirror as an action.
		// We have other coverage asserting it sends the hash correctly.
		_ = gtest.ReceiveSoon(t, re.Actions)

		// Now when the mirror responds, we are at 75% votes without consensus.
		vrv = sfx.Fx.UpdateVRVPrevotes(ctx, vrv, map[string][]int{
			string(pb1.Block.Hash): {0, 1},
			"":                     {2},
		})

		gtest.SendSoon(t, sfx.RoundViewInCh, tmeil.StateMachineRoundView{VRV: vrv})

		// Synchronize on the prevote delay starting, then make it elapse.
		_ = gtest.ReceiveSoon(t, prevoteDelayTimerStarted)
		sfx.RoundTimer.ElapsePrevoteDelayTimer(1, 0)

		// Upon elapse, the state machine makes a decide precommit request.
		req := gtest.ReceiveSoon(t, cStrat.DecidePrecommitRequests)
		require.Equal(t, vrv.VoteSummary, req.Input)

		// And the state machine would typically precommit nil at this point.
		gtest.SendSoon(t, req.ChoiceHash, "")

		// That precommit is sent to the mirror.
		act := gtest.ReceiveSoon(t, re.Actions)
		require.NotEmpty(t, act.Precommit.Sig)

		precommitSignContent, err := tmconsensus.PrecommitSignBytes(tmconsensus.VoteTarget{
			Height: 1, Round: 0,
		}, sfx.Fx.SignatureScheme)
		require.NoError(t, err)
		require.Equal(t, precommitSignContent, act.Precommit.SignContent)
		require.True(t, sfx.Cfg.Signer.PubKey().Verify(act.Precommit.SignContent, act.Precommit.Sig))

		// And at that point, it is present in the action store too.
		ra, err := sfx.Cfg.ActionStore.Load(ctx, 1, 0)
		require.NoError(t, err)
		require.Empty(t, ra.PrecommitTarget)
		require.Equal(t, string(act.Precommit.Sig), ra.PrecommitSignature)
	})

	t.Run("when majority prevotes reached while delay timer is active", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		sfx := tmstatetest.NewFixture(ctx, t, 4)

		sm := sfx.NewStateMachine()
		defer sm.Wait()
		defer cancel()

		re := gtest.ReceiveSoon(t, sfx.RoundEntranceOutCh)

		// Set up consensus strategy expectation before mocking the response.
		cStrat := sfx.CStrat
		_ = cStrat.ExpectEnterRound(1, 0, nil)

		prevoteDelayTimerStarted := sfx.RoundTimer.PrevoteDelayStartNotification(1, 0)

		// Initial state has the proposed block.
		vrv := sfx.EmptyVRV(1, 0)
		pb1 := sfx.Fx.NextProposedBlock([]byte("app_data_1"), 3)
		vrv.ProposedBlocks = []tmconsensus.ProposedBlock{pb1}
		// Channel is 1-buffered, don't have to select.
		re.Response <- tmeil.RoundEntranceResponse{VRV: vrv} // No PrevBlockHash at initial height.

		// This causes a Consider request to the consensus strategy,
		// and we will prevote for the block.
		considerReq := gtest.ReceiveSoon(t, cStrat.ConsiderProposedBlocksRequests)
		gtest.SendSoon(t, considerReq.ChoiceHash, string(pb1.Block.Hash))
		require.Equal(t, []string{string(pb1.Block.Hash)}, considerReq.Reason.NewProposedBlocks)

		// The choice is sent to the mirror as an action.
		// We have other coverage asserting it sends the hash correctly.
		_ = gtest.ReceiveSoon(t, re.Actions)

		// Now when the mirror responds, we are at 75% votes without consensus.
		vrv = sfx.Fx.UpdateVRVPrevotes(ctx, vrv, map[string][]int{
			string(pb1.Block.Hash): {0, 1},
			"":                     {2},
		})
		gtest.SendSoon(t, sfx.RoundViewInCh, tmeil.StateMachineRoundView{VRV: vrv})

		// We don't have a synchronization point to detect the active prevote delay timer.
		// Poll for it to be active, then make it elapse.
		_ = gtest.ReceiveSoon(t, prevoteDelayTimerStarted)

		// Now while the timer is active, the final prevote arrives, pushing the proposed block to a majority prevote.
		vrv = sfx.Fx.UpdateVRVPrevotes(ctx, vrv, map[string][]int{
			string(pb1.Block.Hash): {0, 1, 3},
			"":                     {2},
		})
		gtest.SendSoon(t, sfx.RoundViewInCh, tmeil.StateMachineRoundView{VRV: vrv})

		// The state machine makes a decide precommit request.
		req := gtest.ReceiveSoon(t, cStrat.DecidePrecommitRequests)
		require.Equal(t, vrv.VoteSummary, req.Input)

		// And the timer is no longer active since we are in awaiting precommits at this point.
		sfx.RoundTimer.RequireNoActiveTimer(t)

		// Since precommitting during a delay is an edge case,
		// do the full precommit assertion here.
		gtest.SendSoon(t, req.ChoiceHash, string(pb1.Block.Hash))

		// That precommit is sent to the mirror.
		act := gtest.ReceiveSoon(t, re.Actions)
		require.NotEmpty(t, act.Precommit.Sig)

		precommitSignContent, err := tmconsensus.PrecommitSignBytes(tmconsensus.VoteTarget{
			Height: 1, Round: 0,
			BlockHash: string(pb1.Block.Hash),
		}, sfx.Fx.SignatureScheme)
		require.NoError(t, err)
		require.Equal(t, precommitSignContent, act.Precommit.SignContent)
		require.True(t, sfx.Cfg.Signer.PubKey().Verify(act.Precommit.SignContent, act.Precommit.Sig))

		// And at that point, it is present in the action store too.
		ra, err := sfx.Cfg.ActionStore.Load(ctx, 1, 0)
		require.NoError(t, err)
		require.Equal(t, string(pb1.Block.Hash), ra.PrecommitTarget)
		require.Equal(t, string(act.Precommit.Sig), ra.PrecommitSignature)
	})

	t.Run("after full prevote received", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		sfx := tmstatetest.NewFixture(ctx, t, 4)

		sm := sfx.NewStateMachine()
		defer sm.Wait()
		defer cancel()

		re := gtest.ReceiveSoon(t, sfx.RoundEntranceOutCh)

		// Set up consensus strategy expectation before mocking the response.
		cStrat := sfx.CStrat
		_ = cStrat.ExpectEnterRound(1, 0, nil)

		// Initial state has the proposed block.
		vrv := sfx.EmptyVRV(1, 0)
		pb1 := sfx.Fx.NextProposedBlock([]byte("app_data_1"), 3)
		vrv.ProposedBlocks = []tmconsensus.ProposedBlock{pb1}
		// Channel is 1-buffered, don't have to select.
		re.Response <- tmeil.RoundEntranceResponse{VRV: vrv} // No PrevBlockHash at initial height.

		// This causes a Consider request to the consensus strategy,
		// and we will prevote for the block.
		considerReq := gtest.ReceiveSoon(t, cStrat.ConsiderProposedBlocksRequests)
		gtest.SendSoon(t, considerReq.ChoiceHash, string(pb1.Block.Hash))
		require.Equal(t, []string{string(pb1.Block.Hash)}, considerReq.Reason.NewProposedBlocks)

		// The choice is sent to the mirror as an action.
		// We have other coverage asserting it sends the hash correctly.
		_ = gtest.ReceiveSoon(t, re.Actions)

		vrv = sfx.Fx.UpdateVRVPrevotes(ctx, vrv, map[string][]int{
			string(pb1.Block.Hash): {0, 1, 2, 3},
		})

		gtest.SendSoon(t, sfx.RoundViewInCh, tmeil.StateMachineRoundView{VRV: vrv})

		// With full prevotes present, the state machine makes a decide precommit request.
		req := gtest.ReceiveSoon(t, cStrat.DecidePrecommitRequests)
		require.Equal(t, vrv.VoteSummary, req.Input)
		gtest.SendSoon(t, req.ChoiceHash, string(pb1.Block.Hash))

		// That precommit is sent to the mirror.
		act := gtest.ReceiveSoon(t, re.Actions)
		require.NotEmpty(t, act.Precommit.Sig)

		precommitSignContent, err := tmconsensus.PrecommitSignBytes(tmconsensus.VoteTarget{
			Height: 1, Round: 0,
			BlockHash: string(pb1.Block.Hash),
		}, sfx.Fx.SignatureScheme)
		require.NoError(t, err)
		require.Equal(t, precommitSignContent, act.Precommit.SignContent)
		require.True(t, sfx.Cfg.Signer.PubKey().Verify(act.Precommit.SignContent, act.Precommit.Sig))

		// And at that point, it is present in the action store too.
		ra, err := sfx.Cfg.ActionStore.Load(ctx, 1, 0)
		require.NoError(t, err)
		require.Equal(t, string(pb1.Block.Hash), ra.PrecommitTarget)
		require.Equal(t, string(act.Precommit.Sig), ra.PrecommitSignature)
	})
}

func TestStateMachine_nilPrecommit(t *testing.T) {
	t.Run("normal flow from initial height", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		sfx := tmstatetest.NewFixture(ctx, t, 4)

		sm := sfx.NewStateMachine()
		defer sm.Wait()
		defer cancel()

		re := gtest.ReceiveSoon(t, sfx.RoundEntranceOutCh)

		vrv := sfx.EmptyVRV(1, 0)
		vrv = sfx.Fx.UpdateVRVPrevotes(ctx, vrv, map[string][]int{
			"": {1, 2, 3}, // Everyone else already prevoted for the block.
		})

		// Set up consensus strategy expectation before mocking the response.
		cStrat := sfx.CStrat
		_ = cStrat.ExpectEnterRound(1, 0, nil)

		// Channel is 1-buffered, don't have to select.
		re.Response <- tmeil.RoundEntranceResponse{VRV: vrv}

		// Since there was already a majority prevote for a block,
		// we don't need to submit our prevote -- we jump straight into the precommit decision.
		cReq := gtest.ReceiveSoon(t, cStrat.DecidePrecommitRequests)
		require.Equal(t, vrv.VoteSummary, cReq.Input)

		// And when the consensus strategy responds, the state machine forwards it to the mirror.
		gtest.SendSoon(t, cReq.ChoiceHash, "")

		act := gtest.ReceiveSoon(t, re.Actions)
		require.NotEmpty(t, act.Precommit.Sig)

		// Once the full nil precommits arrive, we will go to the next round.
		ercCh := cStrat.ExpectEnterRound(1, 1, nil)

		// Now we get a live update with everyone's precommit.
		vrv = sfx.Fx.UpdateVRVPrecommits(ctx, vrv, map[string][]int{
			"": {0, 1, 2, 3},
		})
		gtest.SendSoon(t, sfx.RoundViewInCh, tmeil.StateMachineRoundView{VRV: vrv})

		// The state machine requests any existing state at 1/1 first.
		re = gtest.ReceiveSoon(t, sfx.RoundEntranceOutCh)
		vrv = sfx.EmptyVRV(1, 1)
		re.Response <- tmeil.RoundEntranceResponse{VRV: vrv}

		// Then it calls enter round on the gossip strategy.
		// We already set an expectation for this.
		erc := gtest.ReceiveSoon(t, ercCh)
		rv := erc.RV
		require.Equal(t, uint64(1), rv.Height)
		require.Equal(t, uint32(1), rv.Round)
	})

	t.Run("precommit delay timer canceled when advancing round", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		sfx := tmstatetest.NewFixture(ctx, t, 4)

		sm := sfx.NewStateMachine()
		defer sm.Wait()
		defer cancel()

		re := gtest.ReceiveSoon(t, sfx.RoundEntranceOutCh)

		pb1 := sfx.Fx.NextProposedBlock([]byte("app_data_1"), 3)
		vrv := sfx.EmptyVRV(1, 0)
		vrv.ProposedBlocks = []tmconsensus.ProposedBlock{pb1}
		vrv = sfx.Fx.UpdateVRVPrevotes(ctx, vrv, map[string][]int{
			string(pb1.Block.Hash): {1, 2, 3}, // Everyone else already prevoted for the block.
		})

		// Set up consensus strategy expectation before mocking the response.
		cStrat := sfx.CStrat
		_ = cStrat.ExpectEnterRound(1, 0, nil)

		// Channel is 1-buffered, don't have to select.
		re.Response <- tmeil.RoundEntranceResponse{VRV: vrv}

		// Since there was already a majority prevote for a block,
		// we don't need to submit our prevote -- we jump straight into the precommit decision.
		cReq := gtest.ReceiveSoon(t, cStrat.DecidePrecommitRequests)
		require.Equal(t, vrv.VoteSummary, cReq.Input)

		// And when the consensus strategy responds, the state machine forwards it to the mirror.
		gtest.SendSoon(t, cReq.ChoiceHash, string(pb1.Block.Hash))

		act := gtest.ReceiveSoon(t, re.Actions)
		require.NotEmpty(t, act.Precommit.Sig)

		// We expect a precommit delay due to how the precommits arrive.
		precommitDelayStarted := sfx.RoundTimer.PrecommitDelayStartNotification(1, 0)

		// Now we get a live update with 75% and no consensus yet.
		vrv = sfx.Fx.UpdateVRVPrecommits(ctx, vrv, map[string][]int{
			string(pb1.Block.Hash): {0, 3},
			"":                     {1},
		})
		gtest.SendSoon(t, sfx.RoundViewInCh, tmeil.StateMachineRoundView{VRV: vrv})

		// This causes a precommit delay.
		_ = gtest.ReceiveSoon(t, precommitDelayStarted)

		ercCh := cStrat.ExpectEnterRound(1, 1, nil)
		proposalDelayStarted := sfx.RoundTimer.ProposalStartNotification(1, 1)

		// Without the precommit delay elapsing, we receive the last precommit.
		vrv = sfx.Fx.UpdateVRVPrecommits(ctx, vrv, map[string][]int{
			string(pb1.Block.Hash): {0, 3},
			"":                     {1, 2},
		})
		gtest.SendSoon(t, sfx.RoundViewInCh, tmeil.StateMachineRoundView{VRV: vrv})

		// Once the full nil precommits arrive, we will go to the next round.
		as11 := gtest.ReceiveSoon(t, sfx.RoundEntranceOutCh)
		require.Equal(t, uint64(1), as11.H)
		require.Equal(t, uint32(1), as11.R)
		as11.Response <- tmeil.RoundEntranceResponse{VRV: sfx.EmptyVRV(1, 1)}

		_ = gtest.ReceiveSoon(t, ercCh)

		// And the proposal timer starts too.
		_ = gtest.ReceiveSoon(t, proposalDelayStarted)
	})
}

// These tests are focused on events that happen outside the happy path flow.
func TestStateMachine_unexpectedSteps(t *testing.T) {
	t.Run("view update during commit wait", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		sfx := tmstatetest.NewFixture(ctx, t, 4)

		sm := sfx.NewStateMachine()
		defer sm.Wait()
		defer cancel()

		re := gtest.ReceiveSoon(t, sfx.RoundEntranceOutCh)

		pb1 := sfx.Fx.NextProposedBlock([]byte("app_data_1"), 3)
		vrv := sfx.EmptyVRV(1, 0)
		vrv.ProposedBlocks = []tmconsensus.ProposedBlock{pb1}
		vrv = sfx.Fx.UpdateVRVPrevotes(ctx, vrv, map[string][]int{
			string(pb1.Block.Hash): {1, 2, 3}, // Everyone else already prevoted for the block.
		})

		// Set up consensus strategy expectation before mocking the response.
		cStrat := sfx.CStrat
		_ = cStrat.ExpectEnterRound(1, 0, nil)

		// Channel is 1-buffered, don't have to select.
		re.Response <- tmeil.RoundEntranceResponse{VRV: vrv}

		// Since there was already a majority prevote for a block,
		// we don't need to submit our prevote -- we jump straight into the precommit decision.
		cReq := gtest.ReceiveSoon(t, cStrat.DecidePrecommitRequests)
		require.Equal(t, vrv.VoteSummary, cReq.Input)

		// And when the consensus strategy responds, the state machine forwards it to the mirror.
		gtest.SendSoon(t, cReq.ChoiceHash, string(pb1.Block.Hash))

		act := gtest.ReceiveSoon(t, re.Actions)
		require.NotEmpty(t, act.Precommit.Sig)

		// We expect commit wait timer after we get the 3/4 precommits.
		commitWaitStarted := sfx.RoundTimer.CommitWaitStartNotification(1, 0)

		// Now we get a live update with majority consensus for the block.
		vrv = sfx.Fx.UpdateVRVPrecommits(ctx, vrv, map[string][]int{
			string(pb1.Block.Hash): {0, 1, 2},
		})
		gtest.SendSoon(t, sfx.RoundViewInCh, tmeil.StateMachineRoundView{VRV: vrv})

		// Upon receiving that update, we are in commit wait.
		gtest.ReceiveSoon(t, commitWaitStarted)

		// And there is a finalization.
		finReq := gtest.ReceiveSoon(t, sfx.FinalizeBlockRequests)
		require.Equal(t, pb1.Block, finReq.Block)
		require.Zero(t, pb1.Round)

		// Now we get the view update with the last precommit.
		vrv = sfx.Fx.UpdateVRVPrecommits(ctx, vrv, map[string][]int{
			string(pb1.Block.Hash): {0, 1, 2, 3},
		})
		gtest.SendSoon(t, sfx.RoundViewInCh, tmeil.StateMachineRoundView{VRV: vrv})

		// Just handling the view update successfully at least means
		// there is general handling for view updates while in commit wait.
		//
		// In the future we should have a way, on the finalization request,
		// to indicate that there are updated precommits available.
	})

	t.Run("view update during awaiting finalization", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		sfx := tmstatetest.NewFixture(ctx, t, 4)

		sm := sfx.NewStateMachine()
		defer sm.Wait()
		defer cancel()

		re := gtest.ReceiveSoon(t, sfx.RoundEntranceOutCh)

		pb1 := sfx.Fx.NextProposedBlock([]byte("app_data_1"), 3)
		vrv := sfx.EmptyVRV(1, 0)
		vrv.ProposedBlocks = []tmconsensus.ProposedBlock{pb1}
		vrv = sfx.Fx.UpdateVRVPrevotes(ctx, vrv, map[string][]int{
			string(pb1.Block.Hash): {1, 2, 3}, // Everyone else already prevoted for the block.
		})

		// Set up consensus strategy expectation before mocking the response.
		cStrat := sfx.CStrat
		_ = cStrat.ExpectEnterRound(1, 0, nil)

		// Channel is 1-buffered, don't have to select.
		re.Response <- tmeil.RoundEntranceResponse{VRV: vrv}

		// Since there was already a majority prevote for a block,
		// we don't need to submit our prevote -- we jump straight into the precommit decision.
		cReq := gtest.ReceiveSoon(t, cStrat.DecidePrecommitRequests)
		require.Equal(t, vrv.VoteSummary, cReq.Input)

		// And when the consensus strategy responds, the state machine forwards it to the mirror.
		gtest.SendSoon(t, cReq.ChoiceHash, string(pb1.Block.Hash))

		act := gtest.ReceiveSoon(t, re.Actions)
		require.NotEmpty(t, act.Precommit.Sig)

		// We expect commit wait timer after we get the 3/4 precommits.
		commitWaitStarted := sfx.RoundTimer.CommitWaitStartNotification(1, 0)

		// Now we get a live update with majority consensus for the block.
		vrv = sfx.Fx.UpdateVRVPrecommits(ctx, vrv, map[string][]int{
			string(pb1.Block.Hash): {0, 1, 2},
		})
		gtest.SendSoon(t, sfx.RoundViewInCh, tmeil.StateMachineRoundView{VRV: vrv})

		// Upon receiving that update, we are in commit wait.
		gtest.ReceiveSoon(t, commitWaitStarted)

		// The commit wait timer elapses before the finalization request is handled.
		require.NoError(t, sfx.RoundTimer.ElapseCommitWaitTimer(1, 0))

		// Accept the finalization request.
		finReq := gtest.ReceiveSoon(t, sfx.FinalizeBlockRequests)
		require.Equal(t, pb1.Block, finReq.Block)
		require.Zero(t, pb1.Round)

		// Now we get the view update with the last precommit.
		vrv = sfx.Fx.UpdateVRVPrecommits(ctx, vrv, map[string][]int{
			string(pb1.Block.Hash): {0, 1, 2, 3},
		})
		gtest.SendSoon(t, sfx.RoundViewInCh, tmeil.StateMachineRoundView{VRV: vrv})

		// Just handling the view update successfully at least means
		// there is general handling for view updates while in commit wait.
		//
		// In the future we should have a way, on the finalization request,
		// to indicate that there are updated precommits available.
	})
}

func TestStateMachine_finalization(t *testing.T) {
	t.Run("majority precommits at initialization", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		sfx := tmstatetest.NewFixture(ctx, t, 4)

		sm := sfx.NewStateMachine()
		defer sm.Wait()
		defer cancel()

		re := gtest.ReceiveSoon(t, sfx.RoundEntranceOutCh)

		vrv := sfx.EmptyVRV(1, 0)
		pb1 := sfx.Fx.NextProposedBlock([]byte("app_data_1"), 1)
		vrv.ProposedBlocks = []tmconsensus.ProposedBlock{pb1}
		vrv = sfx.Fx.UpdateVRVPrecommits(ctx, vrv, map[string][]int{
			string(pb1.Block.Hash): {1, 2, 3}, // Everyone else already precommited for the block.
		})

		// Set up consensus strategy expectation before mocking the response.
		cStrat := sfx.CStrat
		_ = cStrat.ExpectEnterRound(1, 0, nil)

		// Channel is 1-buffered, don't have to select.
		re.Response <- tmeil.RoundEntranceResponse{VRV: vrv}

		// Since there was already a majority precommit for a block,
		// we don't need to submit our precommit -- we jump straight into the finalization request.
		// NOTE: this may be a dubious assumption.
		// If everyone else precommitted, and we are live, there is an argument that the presence of precommits
		// implies that prevotes were also present.
		// But, if we somehow missed the prevotes, then we would need another method on the consensus strategy
		// to handle this special case.
		// So for now we will finalize without submitting our own precommit.
		finReq := gtest.ReceiveSoon(t, sfx.FinalizeBlockRequests)

		// Not asserting anything about the context.
		// That field is likely going away.

		require.Equal(t, pb1.Block, finReq.Block)
		require.Zero(t, finReq.Round)

		// Response channel must be 1-buffered to avoid the app blocking on send.
		require.Equal(t, 1, cap(finReq.Resp))

		// By the time that the finalize request has been made,
		// the commit wait timer has begun.
		sfx.RoundTimer.RequireActiveCommitWaitTimer(t, 1, 0)

		// Simulate the driver responding.
		finReq.Resp <- tmdriver.FinalizeBlockResponse{
			Height: 1, Round: 0,
			BlockHash: pb1.Block.Hash,

			Validators: sfx.Fx.Vals(),

			AppStateHash: []byte("app_state_1"),
		}

		// We don't have a synchronization point for the finalization being stored.
		// So, if we elapse the commit wait timer...
		require.NoError(t, sfx.RoundTimer.ElapseCommitWaitTimer(1, 0))

		// Then the state machine will have completed the round,
		// and it will submit a new action set to the mirror.
		re2 := gtest.ReceiveSoon(t, sfx.RoundEntranceOutCh)
		require.Equal(t, uint64(2), re2.H)
		require.Zero(t, re2.R)
		require.True(t, sfx.Cfg.Signer.PubKey().Equal(re2.PubKey))

		// Actions channel is buffered so state machine doesn't block sending to mirror.
		require.Equal(t, 3, cap(re2.Actions))

		// State response is buffered so state machine doesn't risk blocking on send.
		require.Equal(t, 1, cap(re2.Response))

		// And now that the state machine has sent the action set,
		// we can be sure the finalization store has the finalization for height 1.
		r, blockHash, vals, appHash, err := sfx.Cfg.FinalizationStore.LoadFinalizationByHeight(ctx, 1)
		require.NoError(t, err)
		require.Zero(t, r)
		require.Equal(t, string(pb1.Block.Hash), blockHash)
		require.True(t, tmconsensus.ValidatorSlicesEqual(vals, pb1.Block.Validators))
		require.Equal(t, "app_state_1", appHash) // String from the hand-coded response earlier in this test.
	})

	t.Run("when precommits arrive during a normal live update", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		sfx := tmstatetest.NewFixture(ctx, t, 4)

		sm := sfx.NewStateMachine()
		defer sm.Wait()
		defer cancel()

		re := gtest.ReceiveSoon(t, sfx.RoundEntranceOutCh)

		vrv := sfx.EmptyVRV(1, 0)
		pb1 := sfx.Fx.NextProposedBlock([]byte("app_data_1"), 1)
		vrv.ProposedBlocks = []tmconsensus.ProposedBlock{pb1}
		vrv = sfx.Fx.UpdateVRVPrevotes(ctx, vrv, map[string][]int{
			string(pb1.Block.Hash): {1, 2, 3}, // Everyone else already prevoted for the block.
		})

		// Set up consensus strategy expectation before mocking the response.
		cStrat := sfx.CStrat
		_ = cStrat.ExpectEnterRound(1, 0, nil)

		// Channel is 1-buffered, don't have to select.
		re.Response <- tmeil.RoundEntranceResponse{VRV: vrv}

		// Since there was already a majority prevote for a block,
		// we don't need to submit our prevote -- we jump straight into the precommit decision.
		cReq := gtest.ReceiveSoon(t, cStrat.DecidePrecommitRequests)
		require.Equal(t, vrv.VoteSummary, cReq.Input)

		// And when the consensus strategy responds, the state machine forwards it to the mirror.
		gtest.SendSoon(t, cReq.ChoiceHash, string(pb1.Block.Hash))

		act := gtest.ReceiveSoon(t, re.Actions)
		require.NotEmpty(t, act.Precommit.Sig)

		// Now we get a live update with everyone's precommit.
		vrv = sfx.Fx.UpdateVRVPrecommits(ctx, vrv, map[string][]int{
			string(pb1.Block.Hash): {0, 1, 2, 3},
		})
		gtest.SendSoon(t, sfx.RoundViewInCh, tmeil.StateMachineRoundView{VRV: vrv})

		// This causes a finalization request.
		finReq := gtest.ReceiveSoon(t, sfx.FinalizeBlockRequests)

		// Not asserting anything about the context.
		// That field is likely going away.

		require.Equal(t, pb1.Block, finReq.Block)
		require.Zero(t, finReq.Round)

		// Response channel must be 1-buffered to avoid the app blocking on send.
		require.Equal(t, 1, cap(finReq.Resp))

		// By the time that the finalize request has been made,
		// the commit wait timer has begun.
		sfx.RoundTimer.RequireActiveCommitWaitTimer(t, 1, 0)

		// Simulate the driver responding.
		finReq.Resp <- tmdriver.FinalizeBlockResponse{
			Height: 1, Round: 0,
			BlockHash: pb1.Block.Hash,

			Validators: sfx.Fx.Vals(),

			AppStateHash: []byte("app_state_1"),
		}

		// We don't have a synchronization point for the finalization being stored.
		// So, if we elapse the commit wait timer...
		require.NoError(t, sfx.RoundTimer.ElapseCommitWaitTimer(1, 0))

		// Then the state machine will have completed the round,
		// and it will submit a new action set to the mirror.
		re2 := gtest.ReceiveSoon(t, sfx.RoundEntranceOutCh)
		require.Equal(t, uint64(2), re2.H)
		require.Zero(t, re2.R)
		require.True(t, sfx.Cfg.Signer.PubKey().Equal(re2.PubKey))

		// Actions channel is buffered so state machine doesn't block sending to mirror.
		require.Equal(t, 3, cap(re2.Actions))

		// State response is buffered so state machine doesn't risk blocking on send.
		require.Equal(t, 1, cap(re2.Response))

		// And now that the state machine has sent the action set,
		// we can be sure the finalization store has the finalization for height 1.
		r, blockHash, vals, appHash, err := sfx.Cfg.FinalizationStore.LoadFinalizationByHeight(ctx, 1)
		require.NoError(t, err)
		require.Zero(t, r)
		require.Equal(t, string(pb1.Block.Hash), blockHash)
		require.True(t, tmconsensus.ValidatorSlicesEqual(vals, pb1.Block.Validators))
		require.Equal(t, "app_state_1", appHash) // String from the hand-coded response earlier in this test.
	})

	t.Run("when precommits are finalized during a precommit delay", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		sfx := tmstatetest.NewFixture(ctx, t, 4)

		sm := sfx.NewStateMachine()
		defer sm.Wait()
		defer cancel()

		re := gtest.ReceiveSoon(t, sfx.RoundEntranceOutCh)

		vrv := sfx.EmptyVRV(1, 0)
		pb1 := sfx.Fx.NextProposedBlock([]byte("app_data_1"), 1)
		vrv.ProposedBlocks = []tmconsensus.ProposedBlock{pb1}
		vrv = sfx.Fx.UpdateVRVPrevotes(ctx, vrv, map[string][]int{
			string(pb1.Block.Hash): {1, 2, 3}, // Everyone else already prevoted for the block.
		})

		// Set up consensus strategy expectation before mocking the response.
		cStrat := sfx.CStrat
		_ = cStrat.ExpectEnterRound(1, 0, nil)

		// Channel is 1-buffered, don't have to select.
		re.Response <- tmeil.RoundEntranceResponse{VRV: vrv}

		// Since there was already a majority prevote for a block,
		// we don't need to submit our prevote -- we jump straight into the precommit decision.
		cReq := gtest.ReceiveSoon(t, cStrat.DecidePrecommitRequests)
		require.Equal(t, vrv.VoteSummary, cReq.Input)

		// And when the consensus strategy responds, the state machine forwards it to the mirror.
		gtest.SendSoon(t, cReq.ChoiceHash, string(pb1.Block.Hash))

		act := gtest.ReceiveSoon(t, re.Actions)
		require.NotEmpty(t, act.Precommit.Sig)

		precommitDelayTimerStarted := sfx.RoundTimer.PrecommitDelayStartNotification(1, 0)

		// Now we get a live update with 3/4 of precommits.
		// The last validator precommitted nil, so we enter precommit delay.
		vrv = sfx.Fx.UpdateVRVPrecommits(ctx, vrv, map[string][]int{
			string(pb1.Block.Hash): {0, 1},
			"":                     {3},
		})
		gtest.SendSoon(t, sfx.RoundViewInCh, tmeil.StateMachineRoundView{VRV: vrv})

		_ = gtest.ReceiveSoon(t, precommitDelayTimerStarted)

		// Then there is another precommit update, causing the proposed block to cross the majority threshold.
		vrv = sfx.Fx.UpdateVRVPrecommits(ctx, vrv, map[string][]int{
			string(pb1.Block.Hash): {0, 1, 2},
			"":                     {3},
		})
		gtest.SendSoon(t, sfx.RoundViewInCh, tmeil.StateMachineRoundView{VRV: vrv})

		// This causes a finalization request.
		finReq := gtest.ReceiveSoon(t, sfx.FinalizeBlockRequests)
		require.Equal(t, pb1.Block, finReq.Block)
		require.Zero(t, finReq.Round)

		// Response channel must be 1-buffered to avoid the app blocking on send.
		require.Equal(t, 1, cap(finReq.Resp))

		// By the time the finalization request is made, the precommit delay timer is no longer active,
		// but the commit wait timer is.
		sfx.RoundTimer.RequireActiveCommitWaitTimer(t, 1, 0)

		// Simulate the driver responding.
		finReq.Resp <- tmdriver.FinalizeBlockResponse{
			Height: 1, Round: 0,
			BlockHash: pb1.Block.Hash,

			Validators: sfx.Fx.Vals(),

			AppStateHash: []byte("app_state_1"),
		}

		// We don't have a synchronization point for the finalization being stored.
		// So, if we elapse the commit wait timer...
		require.NoError(t, sfx.RoundTimer.ElapseCommitWaitTimer(1, 0))

		// Then the state machine will have completed the round,
		// and it will submit a new action set to the mirror.
		re2 := gtest.ReceiveSoon(t, sfx.RoundEntranceOutCh)
		require.Equal(t, uint64(2), re2.H)
		require.Zero(t, re2.R)
		require.True(t, sfx.Cfg.Signer.PubKey().Equal(re2.PubKey))

		// Actions channel is buffered so state machine doesn't block sending to mirror.
		require.Equal(t, 3, cap(re2.Actions))

		// State response is buffered so state machine doesn't risk blocking on send.
		require.Equal(t, 1, cap(re2.Response))

		// And now that the state machine has sent the action set,
		// we can be sure the finalization store has the finalization for height 1.
		r, blockHash, vals, appHash, err := sfx.Cfg.FinalizationStore.LoadFinalizationByHeight(ctx, 1)
		require.NoError(t, err)
		require.Zero(t, r)
		require.Equal(t, string(pb1.Block.Hash), blockHash)
		require.True(t, tmconsensus.ValidatorSlicesEqual(vals, pb1.Block.Validators))
		require.Equal(t, "app_state_1", appHash) // String from the hand-coded response earlier in this test.
	})

	t.Run("when the commit wait timeout elapses before the finalization arrives", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		sfx := tmstatetest.NewFixture(ctx, t, 4)

		sm := sfx.NewStateMachine()
		defer sm.Wait()
		defer cancel()

		re := gtest.ReceiveSoon(t, sfx.RoundEntranceOutCh)

		vrv := sfx.EmptyVRV(1, 0)
		pb1 := sfx.Fx.NextProposedBlock([]byte("app_data_1"), 1)
		vrv.ProposedBlocks = []tmconsensus.ProposedBlock{pb1}
		vrv = sfx.Fx.UpdateVRVPrevotes(ctx, vrv, map[string][]int{
			string(pb1.Block.Hash): {1, 2, 3}, // Everyone else already prevoted for the block.
		})

		// Set up consensus strategy expectation before mocking the response.
		cStrat := sfx.CStrat
		_ = cStrat.ExpectEnterRound(1, 0, nil)

		// Channel is 1-buffered, don't have to select.
		re.Response <- tmeil.RoundEntranceResponse{VRV: vrv}

		// Since there was already a majority prevote for a block,
		// we don't need to submit our prevote -- we jump straight into the precommit decision.
		cReq := gtest.ReceiveSoon(t, cStrat.DecidePrecommitRequests)
		require.Equal(t, vrv.VoteSummary, cReq.Input)

		// And when the consensus strategy responds, the state machine forwards it to the mirror.
		gtest.SendSoon(t, cReq.ChoiceHash, string(pb1.Block.Hash))

		act := gtest.ReceiveSoon(t, re.Actions)
		require.NotEmpty(t, act.Precommit.Sig)

		// Now we get a live update with everyone's precommit.
		vrv = sfx.Fx.UpdateVRVPrecommits(ctx, vrv, map[string][]int{
			string(pb1.Block.Hash): {0, 1, 2, 3},
		})
		gtest.SendSoon(t, sfx.RoundViewInCh, tmeil.StateMachineRoundView{VRV: vrv})

		// This causes a finalization request.
		finReq := gtest.ReceiveSoon(t, sfx.FinalizeBlockRequests)

		// And by the time we have the request, there is an active commit wait timer.
		sfx.RoundTimer.RequireActiveCommitWaitTimer(t, 1, 0)

		// Elapse it before we send the finalization response.
		require.NoError(t, sfx.RoundTimer.ElapseCommitWaitTimer(1, 0))
		finReq.Resp <- tmdriver.FinalizeBlockResponse{
			Height: 1, Round: 0,
			BlockHash: pb1.Block.Hash,

			Validators: sfx.Fx.Vals(),

			AppStateHash: []byte("app_state_1"),
		}

		// The state machine tells the mirror we are on the next height.
		re = gtest.ReceiveSoon(t, sfx.RoundEntranceOutCh)
		require.Equal(t, uint64(2), re.H)
		require.Zero(t, re.R)
	})

	t.Run("when proposed block is missing and it is time to finalize", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		sfx := tmstatetest.NewFixture(ctx, t, 4)

		sm := sfx.NewStateMachine()
		defer sm.Wait()
		defer cancel()

		re := gtest.ReceiveSoon(t, sfx.RoundEntranceOutCh)

		// Make the proposed block but don't set it in the vrv yet.
		pb1 := sfx.Fx.NextProposedBlock([]byte("app_data_1"), 1)

		vrv := sfx.EmptyVRV(1, 0)
		vrv = sfx.Fx.UpdateVRVPrevotes(ctx, vrv, map[string][]int{
			string(pb1.Block.Hash): {1, 2, 3}, // Everyone else already prevoted for the block.
		})

		// Set up consensus strategy expectation before mocking the response.
		cStrat := sfx.CStrat
		_ = cStrat.ExpectEnterRound(1, 0, nil)

		// Channel is 1-buffered, don't have to select.
		re.Response <- tmeil.RoundEntranceResponse{VRV: vrv}

		// Since there was already a majority prevote for a block,
		// we don't need to submit our prevote -- we jump straight into the precommit decision.
		cReq := gtest.ReceiveSoon(t, cStrat.DecidePrecommitRequests)
		require.Equal(t, vrv.VoteSummary, cReq.Input)

		// And when the consensus strategy responds, the state machine forwards it to the mirror.
		gtest.SendSoon(t, cReq.ChoiceHash, string(pb1.Block.Hash))

		act := gtest.ReceiveSoon(t, re.Actions)
		require.NotEmpty(t, act.Precommit.Sig)

		// Now we get a live update with everyone's precommit.
		// We are not going to make a finalization request,
		// but this will still start the commit wait timer.
		commitWaitStarted := sfx.RoundTimer.CommitWaitStartNotification(1, 0)
		vrv = sfx.Fx.UpdateVRVPrecommits(ctx, vrv, map[string][]int{
			string(pb1.Block.Hash): {0, 1, 2, 3},
		})
		gtest.SendSoon(t, sfx.RoundViewInCh, tmeil.StateMachineRoundView{VRV: vrv})
		gtest.ReceiveSoon(t, commitWaitStarted)

		// Normally this would cause a finalization request,
		// but we don't have the proposed block yet,
		// so we can't tell the app to finalize.
		gtest.NotSending(t, sfx.FinalizeBlockRequests)

		// If the commit wait elapses, we still do not start the finalize request.
		require.NoError(t, sfx.RoundTimer.ElapseCommitWaitTimer(1, 0))
		gtest.NotSending(t, sfx.FinalizeBlockRequests)

		// Now with the commit wait elapsed and the finalization not started,
		// if we receive an update with the missing proposed block,
		// we get a finalization request, and there is still no active timer.
		vrv = vrv.Clone()
		vrv.ProposedBlocks = []tmconsensus.ProposedBlock{pb1}
		vrv.Version++
		gtest.SendSoon(t, sfx.RoundViewInCh, tmeil.StateMachineRoundView{VRV: vrv})

		finReq := gtest.ReceiveSoon(t, sfx.FinalizeBlockRequests)
		sfx.RoundTimer.RequireNoActiveTimer(t)

		// Responding to the finalization request completes correctly.
		finReq.Resp <- tmdriver.FinalizeBlockResponse{
			Height: 1, Round: 0,
			BlockHash: pb1.Block.Hash,

			Validators: sfx.Fx.Vals(),

			AppStateHash: []byte("app_state_1"),
		}

		// Then the state machine tells the mirror we are on the next height.
		re = gtest.ReceiveSoon(t, sfx.RoundEntranceOutCh)
		require.Equal(t, uint64(2), re.H)
		require.Zero(t, re.R)
	})

	// Regression test for a case where the finalization response channel was being set to nil
	// when it should have been unmodified, depending on the order of finalization response
	// and commit wait timeout.
	t.Run("multiple finalizations in sequence", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		sfx := tmstatetest.NewFixture(ctx, t, 3)
		sfx.Cfg.Signer = nil

		sm := sfx.NewStateMachine()
		defer sm.Wait()
		defer cancel()

		cStrat := sfx.CStrat
		_ = cStrat.ExpectEnterRound(1, 0, nil)
		_ = cStrat.ExpectEnterRound(2, 0, nil)
		_ = cStrat.ExpectEnterRound(3, 0, nil)

		re := gtest.ReceiveSoon(t, sfx.RoundEntranceOutCh)

		// Send an empty state response so we aren't sending a committable state for the initial state.
		vrv := sfx.EmptyVRV(1, 0)
		re.Response <- tmeil.RoundEntranceResponse{VRV: vrv}

		pb1 := sfx.Fx.NextProposedBlock([]byte("app_data_1"), 0)
		sfx.Fx.SignProposal(ctx, &pb1, 0)
		vrv = vrv.Clone()
		vrv.ProposedBlocks = []tmconsensus.ProposedBlock{pb1}
		vrv = sfx.Fx.UpdateVRVPrevotes(ctx, vrv, map[string][]int{
			string(pb1.Block.Hash): {0, 1, 2},
		})
		vrv = sfx.Fx.UpdateVRVPrecommits(ctx, vrv, map[string][]int{
			string(pb1.Block.Hash): {0, 1, 2},
		})
		gtest.SendSoon(t, sfx.RoundViewInCh, tmeil.StateMachineRoundView{VRV: vrv})

		// On the first height, we send the finalize response first and then elapse the commit wait timer.
		finReq := gtest.ReceiveSoon(t, sfx.FinalizeBlockRequests)
		sfx.RoundTimer.RequireActiveCommitWaitTimer(t, 1, 0)

		finReq.Resp <- tmdriver.FinalizeBlockResponse{
			Height: 1, Round: 0,
			BlockHash:    pb1.Block.Hash,
			Validators:   sfx.Fx.Vals(),
			AppStateHash: []byte("state_1"),
		}
		// No good synchronization point to ensure the finalization has been handled,
		// so just a short sleep to give the state machine a chance.
		// Could alternatively poll the finalization store.
		gtest.Sleep(gtest.ScaleMs(10))
		require.NoError(t, sfx.RoundTimer.ElapseCommitWaitTimer(1, 0))

		// Now for height 2, same initial setup.
		re = gtest.ReceiveSoon(t, sfx.RoundEntranceOutCh)
		require.Equal(t, uint64(2), re.H)

		vrv = sfx.EmptyVRV(2, 0)
		re.Response <- tmeil.RoundEntranceResponse{VRV: vrv}

		sfx.Fx.CommitBlock(pb1.Block, []byte("state_1"), 0, map[string]gcrypto.CommonMessageSignatureProof{
			string(pb1.Block.Hash): sfx.Fx.PrecommitSignatureProof(ctx, tmconsensus.VoteTarget{
				Height:    1,
				Round:     0,
				BlockHash: string(pb1.Block.Hash),
			}, nil, []int{0, 1, 2}),
		})
		pb2 := sfx.Fx.NextProposedBlock([]byte("app_data_2"), 0)
		sfx.Fx.SignProposal(ctx, &pb2, 0)
		vrv = vrv.Clone()
		vrv.ProposedBlocks = []tmconsensus.ProposedBlock{pb2}
		vrv = sfx.Fx.UpdateVRVPrevotes(ctx, vrv, map[string][]int{
			string(pb2.Block.Hash): {0, 1, 2},
		})
		vrv = sfx.Fx.UpdateVRVPrecommits(ctx, vrv, map[string][]int{
			string(pb2.Block.Hash): {0, 1, 2},
		})
		gtest.SendSoon(t, sfx.RoundViewInCh, tmeil.StateMachineRoundView{VRV: vrv})

		// For the second height, we elapse the commit wait timer first and then send the finalization request,
		// the opposite order of the first height.
		finReq = gtest.ReceiveSoon(t, sfx.FinalizeBlockRequests)

		sfx.RoundTimer.RequireActiveCommitWaitTimer(t, 2, 0)
		require.NoError(t, sfx.RoundTimer.ElapseCommitWaitTimer(2, 0))

		// Again lacking a good synchronization point here.
		gtest.Sleep(gtest.ScaleMs(10))

		finReq.Resp <- tmdriver.FinalizeBlockResponse{
			Height: 2, Round: 0,
			BlockHash:    pb2.Block.Hash,
			Validators:   sfx.Fx.Vals(),
			AppStateHash: []byte("state_2"),
		}

		// Now one more finalization, to ensure that either finalize-timeout order does not break finalizations.
		re = gtest.ReceiveSoon(t, sfx.RoundEntranceOutCh)
		require.Equal(t, uint64(3), re.H)

		vrv = sfx.EmptyVRV(3, 0)
		re.Response <- tmeil.RoundEntranceResponse{VRV: vrv}

		sfx.Fx.CommitBlock(pb2.Block, []byte("state_2"), 0, map[string]gcrypto.CommonMessageSignatureProof{
			string(pb2.Block.Hash): sfx.Fx.PrecommitSignatureProof(ctx, tmconsensus.VoteTarget{
				Height:    2,
				Round:     0,
				BlockHash: string(pb2.Block.Hash),
			}, nil, []int{0, 1, 2}),
		})
		pb3 := sfx.Fx.NextProposedBlock([]byte("app_data_3"), 0)
		sfx.Fx.SignProposal(ctx, &pb3, 0)
		vrv = vrv.Clone()
		vrv.ProposedBlocks = []tmconsensus.ProposedBlock{pb3}
		vrv = sfx.Fx.UpdateVRVPrevotes(ctx, vrv, map[string][]int{
			string(pb3.Block.Hash): {0, 1, 2},
		})
		vrv = sfx.Fx.UpdateVRVPrecommits(ctx, vrv, map[string][]int{
			string(pb3.Block.Hash): {0, 1, 2},
		})
		gtest.SendSoon(t, sfx.RoundViewInCh, tmeil.StateMachineRoundView{VRV: vrv})

		finReq = gtest.ReceiveSoon(t, sfx.FinalizeBlockRequests)
		require.NoError(t, sfx.RoundTimer.ElapseCommitWaitTimer(3, 0))
		finReq.Resp <- tmdriver.FinalizeBlockResponse{
			Height: 3, Round: 0,
			BlockHash:    pb2.Block.Hash,
			Validators:   sfx.Fx.Vals(),
			AppStateHash: []byte("state_3"),
		}
		_ = gtest.ReceiveSoon(t, sfx.RoundEntranceOutCh)
	})
}

func TestStateMachine_followerMode(t *testing.T) {
	t.Run("happy path at initial height", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		sfx := tmstatetest.NewFixture(ctx, t, 4)
		sfx.Cfg.Signer = nil // Nil signer means "follower mode"; will never participate in consensus.

		sm := sfx.NewStateMachine()
		defer sm.Wait()
		defer cancel()

		re := gtest.ReceiveSoon(t, sfx.RoundEntranceOutCh)

		// Still expect consensus strategy calls, for any local state.
		cStrat := sfx.CStrat
		_ = cStrat.ExpectEnterRound(1, 0, nil)

		vrv := sfx.EmptyVRV(1, 0)
		re.Response <- tmeil.RoundEntranceResponse{VRV: vrv} // No PrevBlockHash at initial height.

		pb := sfx.Fx.NextProposedBlock([]byte("app_data_1"), 1)

		vrv = vrv.Clone()
		vrv.ProposedBlocks = []tmconsensus.ProposedBlock{pb}
		vrv.Version++

		gtest.SendSoon(t, sfx.RoundViewInCh, tmeil.StateMachineRoundView{VRV: vrv})

		// This causes a Consider call.
		// Even in follower mode, the state machine is allowed to consider and choose proposed blocks.
		considerReq := gtest.ReceiveSoon(t, cStrat.ConsiderProposedBlocksRequests)
		require.Equal(t, []string{string(pb.Block.Hash)}, considerReq.Reason.NewProposedBlocks)

		// The proposal timer is active before we make a decision.
		sfx.RoundTimer.RequireActiveProposalTimer(t, 1, 0)
		gtest.SendSoon(t, considerReq.ChoiceHash, string(pb.Block.Hash))

		// If this was not in follower mode, we could watch the mirror output channel.
		// So to ensure the prevote is complete, we will poll for the proposal timer to be inactive.
		require.Eventually(t, func() bool {
			tName, _, _ := sfx.RoundTimer.ActiveTimer()
			return tName == ""
		}, time.Duration(gtest.ScaleMs(100)), 2*time.Millisecond)

		// The majority of the network also prevotes for the block.
		vrv = sfx.Fx.UpdateVRVPrevotes(ctx, vrv, map[string][]int{string(pb.Block.Hash): {0, 1, 2}})
		gtest.SendSoon(t, sfx.RoundViewInCh, tmeil.StateMachineRoundView{VRV: vrv})

		// Now that we have majority prevotes, the state machine makes a precommit decision request.
		precommitReq := gtest.ReceiveSoon(t, cStrat.DecidePrecommitRequests)

		// We precommit for the block.
		gtest.SendSoon(t, precommitReq.ChoiceHash, string(pb.Block.Hash))

		// Then the rest of the precommits arrive.
		vrv = sfx.Fx.UpdateVRVPrecommits(ctx, vrv, map[string][]int{string(pb.Block.Hash): {0, 1, 2, 3}})
		gtest.SendSoon(t, sfx.RoundViewInCh, tmeil.StateMachineRoundView{VRV: vrv})

		// This causes a finalize block request.
		finReq := gtest.ReceiveSoon(t, sfx.FinalizeBlockRequests)

		require.Equal(t, pb.Block, finReq.Block)
		require.Zero(t, finReq.Round)

		// Response channel must be 1-buffered to avoid the app blocking on send.
		require.Equal(t, 1, cap(finReq.Resp))

		// By the time that the finalize request has been made,
		// the commit wait timer has begun.
		sfx.RoundTimer.RequireActiveCommitWaitTimer(t, 1, 0)

		// Simulate the driver responding.
		finReq.Resp <- tmdriver.FinalizeBlockResponse{
			Height: 1, Round: 0,
			BlockHash: pb.Block.Hash,

			Validators: sfx.Fx.Vals(),

			AppStateHash: []byte("app_state_1"),
		}

		// We don't have a synchronization point for the finalization being stored.
		// So, if we elapse the commit wait timer...
		require.NoError(t, sfx.RoundTimer.ElapseCommitWaitTimer(1, 0))

		// Then the state machine will have completed the round,
		// and it will submit a new action set to the mirror.
		re2 := gtest.ReceiveSoon(t, sfx.RoundEntranceOutCh)
		require.Equal(t, uint64(2), re2.H)
		require.Zero(t, re2.R)
		require.Nil(t, re2.PubKey)

		// Actions channel is nil in follower mode.
		require.Nil(t, re2.Actions)

		// State response is buffered so state machine doesn't risk blocking on send.
		// Not nil even in follower mode.
		require.Equal(t, 1, cap(re2.Response))
	})
}

func TestStateMachine_timers(t *testing.T) {
	t.Run("proposal", func(t *testing.T) {
		t.Run("choose from empty proposed block set when elapsed before receiving a proposed block", func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			sfx := tmstatetest.NewFixture(ctx, t, 2)

			sm := sfx.NewStateMachine()
			defer sm.Wait()
			defer cancel()

			re := gtest.ReceiveSoon(t, sfx.RoundEntranceOutCh)

			// Set up consensus strategy expectation before mocking the response.
			cStrat := sfx.CStrat
			_ = cStrat.ExpectEnterRound(1, 0, nil)

			proposalTimerStarted := sfx.RoundTimer.ProposalStartNotification(1, 0)

			// Channel is 1-buffered, don't have to select.
			re.Response <- tmeil.RoundEntranceResponse{VRV: sfx.EmptyVRV(1, 0)} // No PrevBlockHash at initial height.

			// Synchronize on proposal timer starting.
			_ = gtest.ReceiveSoon(t, proposalTimerStarted)

			// We haven't sent any proposed blocks, so if the timer elapses,
			// the state machine calls ChooseProposedBlock on the consensus strategy
			// with an empty set of proposed blocks.
			require.NoError(t, sfx.RoundTimer.ElapseProposalTimer(1, 0))
			choosePBReq := gtest.ReceiveSoon(t, cStrat.ChooseProposedBlockRequests)
			require.Empty(t, choosePBReq.Input)

			// And if the strategy makes a choice, it gets sent to the mirror.
			gtest.SendSoon(t, choosePBReq.ChoiceHash, "")

			action := gtest.ReceiveSoon(t, re.Actions)
			prevote := action.Prevote
			require.Empty(t, prevote.TargetHash)
			require.True(t, sfx.Cfg.Signer.PubKey().Verify(prevote.SignContent, prevote.Sig))
		})

		t.Run("choose from received proposed block set when elapsed", func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			sfx := tmstatetest.NewFixture(ctx, t, 4)

			sm := sfx.NewStateMachine()
			defer sm.Wait()
			defer cancel()

			re := gtest.ReceiveSoon(t, sfx.RoundEntranceOutCh)

			// Set up consensus strategy expectation before mocking the response.
			cStrat := sfx.CStrat
			_ = cStrat.ExpectEnterRound(1, 0, nil)

			// Channel is 1-buffered, don't have to select.
			vrv := sfx.EmptyVRV(1, 0)
			re.Response <- tmeil.RoundEntranceResponse{VRV: vrv} // No PrevBlockHash at initial height.

			pbs := []tmconsensus.ProposedBlock{
				sfx.Fx.NextProposedBlock([]byte("val1"), 1),
				sfx.Fx.NextProposedBlock([]byte("val2"), 2),
			}

			vrv = vrv.Clone()
			vrv.ProposedBlocks = slices.Clone(pbs)
			vrv.Version++

			gtest.SendSoon(t, sfx.RoundViewInCh, tmeil.StateMachineRoundView{VRV: vrv})

			// This causes a Consider call, and we won't pick one at this point.
			considerReq := gtest.ReceiveSoon(t, cStrat.ConsiderProposedBlocksRequests)
			require.ElementsMatch(t, []string{
				string(pbs[0].Block.Hash),
				string(pbs[1].Block.Hash),
			}, considerReq.Reason.NewProposedBlocks)
			gtest.SendSoon(t, considerReq.ChoiceError, tmconsensus.ErrProposedBlockChoiceNotReady)

			require.NoError(t, sfx.RoundTimer.ElapseProposalTimer(1, 0))
			choosePBReq := gtest.ReceiveSoon(t, cStrat.ChooseProposedBlockRequests)
			require.Equal(t, pbs, choosePBReq.Input)

			// Now choosing one of the PBs causes if the strategy makes a choice, it gets sent to the mirror.
			gtest.SendSoon(t, choosePBReq.ChoiceHash, string(pbs[0].Block.Hash))

			action := gtest.ReceiveSoon(t, re.Actions)
			prevote := action.Prevote
			require.Equal(t, string(pbs[0].Block.Hash), prevote.TargetHash)
			require.True(t, sfx.Cfg.Signer.PubKey().Verify(prevote.SignContent, prevote.Sig))
		})

		t.Run("choosing during ConsiderProposedBlocks cancels the timer", func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			sfx := tmstatetest.NewFixture(ctx, t, 2)

			sm := sfx.NewStateMachine()
			defer sm.Wait()
			defer cancel()

			re := gtest.ReceiveSoon(t, sfx.RoundEntranceOutCh)

			// Set up consensus strategy expectation before mocking the response.
			cStrat := sfx.CStrat
			_ = cStrat.ExpectEnterRound(1, 0, nil)

			// Channel is 1-buffered, don't have to select.
			vrv := sfx.EmptyVRV(1, 0)
			re.Response <- tmeil.RoundEntranceResponse{VRV: vrv} // No PrevBlockHash at initial height.

			pb := sfx.Fx.NextProposedBlock([]byte("app_data_1"), 1)

			vrv = vrv.Clone()
			vrv.ProposedBlocks = []tmconsensus.ProposedBlock{pb}
			vrv.Version++

			gtest.SendSoon(t, sfx.RoundViewInCh, tmeil.StateMachineRoundView{VRV: vrv})

			// This causes a Consider call.
			considerReq := gtest.ReceiveSoon(t, cStrat.ConsiderProposedBlocksRequests)
			require.Equal(t, []string{string(pb.Block.Hash)}, considerReq.Reason.NewProposedBlocks)

			// The proposal timer is active before we make a decision.
			sfx.RoundTimer.RequireActiveProposalTimer(t, 1, 0)
			gtest.SendSoon(t, considerReq.ChoiceHash, string(pb.Block.Hash))

			// Making a decision causes the prevote to be submitted.
			action := gtest.ReceiveSoon(t, re.Actions)
			prevote := action.Prevote
			require.Equal(t, string(pb.Block.Hash), prevote.TargetHash)
			require.True(t, sfx.Cfg.Signer.PubKey().Verify(prevote.SignContent, prevote.Sig))

			// And at that point the timer is no longer active.
			sfx.RoundTimer.RequireNoActiveTimer(t)
		})

		t.Run("crossing majority prevotes cancels the timer", func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			sfx := tmstatetest.NewFixture(ctx, t, 4)

			sm := sfx.NewStateMachine()
			defer sm.Wait()
			defer cancel()

			re := gtest.ReceiveSoon(t, sfx.RoundEntranceOutCh)

			// Set up consensus strategy expectation before mocking the response.
			cStrat := sfx.CStrat
			_ = cStrat.ExpectEnterRound(1, 0, nil)

			// Channel is 1-buffered, don't have to select.
			vrv := sfx.EmptyVRV(1, 0)
			re.Response <- tmeil.RoundEntranceResponse{VRV: vrv} // No PrevBlockHash at initial height.

			vrv = sfx.Fx.UpdateVRVPrevotes(ctx, vrv, map[string][]int{"": {1}}) // A quarter of the votes.

			gtest.SendSoon(t, sfx.RoundViewInCh, tmeil.StateMachineRoundView{VRV: vrv})

			// After the first prevote, the proposal timer is still active,
			// and no PB requests have started.
			sfx.RoundTimer.RequireActiveProposalTimer(t, 1, 0)
			gtest.NotSending(t, cStrat.ConsiderProposedBlocksRequests)
			gtest.NotSending(t, cStrat.ChooseProposedBlockRequests)

			// Now more nil prevotes arrive, which tells the state machine that
			// it is time to prevote.
			vrv = sfx.Fx.UpdateVRVPrevotes(ctx, vrv, map[string][]int{"": {1, 2, 3}})
			gtest.SendSoon(t, sfx.RoundViewInCh, tmeil.StateMachineRoundView{VRV: vrv})

			// Seeing the >1/3 prevotes causes a ChooseProposedBlock request
			// (with an empty set of proposed blocks since the VRV doesn't have any).
			choosePBReq := gtest.ReceiveSoon(t, cStrat.ChooseProposedBlockRequests)
			require.Empty(t, choosePBReq.Input)

			// The timer is cancelled even before the choose response.
			sfx.RoundTimer.RequireNoActiveTimer(t)
		})

		t.Run("crossing minority prevotes remains in awaiting proposal", func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			sfx := tmstatetest.NewFixture(ctx, t, 4)

			sm := sfx.NewStateMachine()
			defer sm.Wait()
			defer cancel()

			re := gtest.ReceiveSoon(t, sfx.RoundEntranceOutCh)

			// Set up consensus strategy expectation before mocking the response.
			cStrat := sfx.CStrat
			_ = cStrat.ExpectEnterRound(1, 0, nil)

			// Channel is 1-buffered, don't have to select.
			vrv := sfx.EmptyVRV(1, 0)
			re.Response <- tmeil.RoundEntranceResponse{VRV: vrv} // No PrevBlockHash at initial height.

			vrv = sfx.Fx.UpdateVRVPrevotes(ctx, vrv, map[string][]int{"": {1}}) // A quarter of the votes.

			gtest.SendSoon(t, sfx.RoundViewInCh, tmeil.StateMachineRoundView{VRV: vrv})

			// After the first prevote, the proposal timer is still active,
			// and no PB requests have started.
			sfx.RoundTimer.RequireActiveProposalTimer(t, 1, 0)
			gtest.NotSending(t, cStrat.ConsiderProposedBlocksRequests)
			gtest.NotSending(t, cStrat.ChooseProposedBlockRequests)

			// Now another prevote arrives and we are at 50% prevotes,
			// above the minority threshold but below majority.
			vrv = sfx.Fx.UpdateVRVPrevotes(ctx, vrv, map[string][]int{"": {1, 2}})
			gtest.SendSoon(t, sfx.RoundViewInCh, tmeil.StateMachineRoundView{VRV: vrv})

			// We are still waiting for proposals,
			// and there is no new consider or choose request.
			sfx.RoundTimer.RequireActiveProposalTimer(t, 1, 0)
			gtest.NotSending(t, cStrat.ConsiderProposedBlocksRequests)
			gtest.NotSending(t, cStrat.ChooseProposedBlockRequests)
		})
	})

	t.Run("prevote", func(t *testing.T) {
		t.Run("starts when majority prevote without consensus", func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			sfx := tmstatetest.NewFixture(ctx, t, 8)

			sm := sfx.NewStateMachine()
			defer sm.Wait()
			defer cancel()

			re := gtest.ReceiveSoon(t, sfx.RoundEntranceOutCh)

			// Set up consensus strategy expectation before mocking the response.
			cStrat := sfx.CStrat
			_ = cStrat.ExpectEnterRound(1, 0, nil)

			pb1 := sfx.Fx.NextProposedBlock([]byte("app_data_1"), 1)
			vrv := sfx.EmptyVRV(1, 0)
			vrv.ProposedBlocks = []tmconsensus.ProposedBlock{pb1}

			// One prevote for the block first.
			vrv = sfx.Fx.UpdateVRVPrevotes(ctx, vrv, map[string][]int{
				string(pb1.Block.Hash): {1},
			})

			prevoteDelayTimerStarted := sfx.RoundTimer.PrevoteDelayStartNotification(1, 0)

			// Channel is 1-buffered, don't have to select.
			re.Response <- tmeil.RoundEntranceResponse{VRV: vrv} // No PrevBlockHash at initial height.

			// Our validator votes for the proposed block.
			considerPBReq := gtest.ReceiveSoon(t, cStrat.ConsiderProposedBlocksRequests)
			require.Equal(t, []tmconsensus.ProposedBlock{pb1}, considerPBReq.PBs)
			require.Equal(t, []string{string(pb1.Block.Hash)}, considerPBReq.Reason.NewProposedBlocks)
			gtest.SendSoon(t, considerPBReq.ChoiceHash, string(pb1.Block.Hash))

			// Just drain the action; we have other coverage that this behaves correctly.
			_ = gtest.ReceiveSoon(t, re.Actions)

			// At this point, with only 12.5% or 25% of prevotes in, there should be no active timer.
			// But we don't have a good synchronization point to match this.

			// Then if we get more prevotes and they are split...
			vrv = sfx.Fx.UpdateVRVPrevotes(ctx, vrv, map[string][]int{
				string(pb1.Block.Hash): {0, 1, 2},
				"":                     {3, 4, 5},
			})
			gtest.SendSoon(t, sfx.RoundViewInCh, tmeil.StateMachineRoundView{VRV: vrv})

			// Then we have an active prevote delay timer.
			_ = gtest.ReceiveSoon(t, prevoteDelayTimerStarted)

			// And when that timer elapses, there is a decide precommit request.
			require.NoError(t, sfx.RoundTimer.ElapsePrevoteDelayTimer(1, 0))
			req := gtest.ReceiveSoon(t, cStrat.DecidePrecommitRequests)
			require.Equal(t, vrv.VoteSummary, req.Input)
		})
	})

	t.Run("precommit", func(t *testing.T) {
		t.Run("starts when majority precommit without consensus", func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			sfx := tmstatetest.NewFixture(ctx, t, 8)

			sm := sfx.NewStateMachine()
			defer sm.Wait()
			defer cancel()

			re := gtest.ReceiveSoon(t, sfx.RoundEntranceOutCh)

			// Set up consensus strategy expectation before mocking the response.
			cStrat := sfx.CStrat
			_ = cStrat.ExpectEnterRound(1, 0, nil)

			pb1 := sfx.Fx.NextProposedBlock([]byte("app_data_1"), 1)
			vrv := sfx.EmptyVRV(1, 0)
			vrv.ProposedBlocks = []tmconsensus.ProposedBlock{pb1}

			// Everyone else already prevoted for the block.
			vrv = sfx.Fx.UpdateVRVPrevotes(ctx, vrv, map[string][]int{
				string(pb1.Block.Hash): {1, 2, 3, 4, 5, 6, 7},
			})

			precommitDelayTimerStarted := sfx.RoundTimer.PrecommitDelayStartNotification(1, 0)

			// Channel is 1-buffered, don't have to select.
			re.Response <- tmeil.RoundEntranceResponse{VRV: vrv} // No PrevBlockHash at initial height.

			// Since there are majority prevotes, we go straight to precommit.
			decidePrecommitReq := gtest.ReceiveSoon(t, cStrat.DecidePrecommitRequests)
			gtest.SendSoon(t, decidePrecommitReq.ChoiceHash, string(pb1.Block.Hash))

			// Now the mirror responds with some other precommits, enough to start the timer.
			vrv = sfx.Fx.UpdateVRVPrecommits(ctx, vrv, map[string][]int{
				string(pb1.Block.Hash): {0, 1, 2},
				"":                     {3, 4, 5},
			})
			gtest.SendSoon(t, sfx.RoundViewInCh, tmeil.StateMachineRoundView{VRV: vrv})

			// Then we have an active prevote delay timer.
			_ = gtest.ReceiveSoon(t, precommitDelayTimerStarted)

			// When it elapses, we are going to enter the next round.
			ercCh := cStrat.ExpectEnterRound(1, 1, nil)

			require.NoError(t, sfx.RoundTimer.ElapsePrecommitDelayTimer(1, 0))

			// Elapsing advances to the next round now.
			as11 := gtest.ReceiveSoon(t, sfx.RoundEntranceOutCh)
			as11.Response <- tmeil.RoundEntranceResponse{VRV: sfx.EmptyVRV(1, 1)}

			erc := gtest.ReceiveSoon(t, ercCh)
			require.Equal(t, uint64(1), erc.RV.Height)
			require.Equal(t, uint32(1), erc.RV.Round)
		})
	})
}

func TestStateMachine_jumpAhead(t *testing.T) {
	t.Run("without a current VRV update", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		sfx := tmstatetest.NewFixture(ctx, t, 4)

		sm := sfx.NewStateMachine()
		defer sm.Wait()
		defer cancel()

		re := gtest.ReceiveSoon(t, sfx.RoundEntranceOutCh)

		cStrat := sfx.CStrat
		_ = cStrat.ExpectEnterRound(1, 0, nil)

		vrv := sfx.EmptyVRV(1, 0)

		re.Response <- tmeil.RoundEntranceResponse{VRV: vrv}

		er11Ch := cStrat.ExpectEnterRound(1, 1, nil)

		nextVRV := sfx.EmptyVRV(1, 1)
		nextVRV = sfx.Fx.UpdateVRVPrevotes(ctx, nextVRV, map[string][]int{
			"": {1, 2, 3},
		})

		gtest.SendSoon(t, sfx.RoundViewInCh, tmeil.StateMachineRoundView{
			// No VRV details supplied.

			JumpAheadRoundView: &nextVRV,
		})

		re = gtest.ReceiveSoon(t, sfx.RoundEntranceOutCh)
		require.Equal(t, uint64(1), re.H)
		require.Equal(t, uint32(1), re.R)
		re.Response <- tmeil.RoundEntranceResponse{VRV: nextVRV}

		_ = gtest.ReceiveSoon(t, er11Ch)
	})

	t.Run("including a current VRV update", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		sfx := tmstatetest.NewFixture(ctx, t, 4)

		sm := sfx.NewStateMachine()
		defer sm.Wait()
		defer cancel()

		re := gtest.ReceiveSoon(t, sfx.RoundEntranceOutCh)

		cStrat := sfx.CStrat
		er10Ch := cStrat.ExpectEnterRound(1, 0, nil)

		vrv := sfx.EmptyVRV(1, 0)
		re.Response <- tmeil.RoundEntranceResponse{VRV: vrv}

		_ = gtest.ReceiveSoon(t, er10Ch)

		// Now update the original VRV with a proposed block.
		pb1 := sfx.Fx.NextProposedBlock([]byte("app_data_1"), 3)
		sfx.Fx.SignProposal(ctx, &pb1, 3)
		vrv = vrv.Clone()
		vrv.ProposedBlocks = []tmconsensus.ProposedBlock{pb1}
		vrv.Version++

		er11Ch := cStrat.ExpectEnterRound(1, 1, nil)

		nextVRV := sfx.EmptyVRV(1, 1)
		nextVRV = sfx.Fx.UpdateVRVPrevotes(ctx, nextVRV, map[string][]int{
			"": {1, 2, 3},
		})

		gtest.SendSoon(t, sfx.RoundViewInCh, tmeil.StateMachineRoundView{
			VRV: vrv.Clone(),

			JumpAheadRoundView: &nextVRV,
		})

		// The state machine will want to handle the round entrance,
		// but the mock consensus strategy may be blocked on the proposed block for 1/0,
		// so unblock that in order to allow the round entrance to proceed.
		cbReq := gtest.ReceiveSoon(t, cStrat.ConsiderProposedBlocksRequests)
		require.Equal(t, []string{string(pb1.Block.Hash)}, cbReq.Reason.NewProposedBlocks)
		gtest.SendSoon(t, cbReq.ChoiceHash, "")

		re = gtest.ReceiveSoon(t, sfx.RoundEntranceOutCh)
		require.Equal(t, uint64(1), re.H)
		require.Equal(t, uint32(1), re.R)
		re.Response <- tmeil.RoundEntranceResponse{VRV: nextVRV}

		_ = gtest.ReceiveSoon(t, er11Ch)
	})
}

func TestStateMachine_blockDataArrival(t *testing.T) {
	t.Run("matching, after proposed block received on first update", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		sfx := tmstatetest.NewFixture(ctx, t, 4)

		sm := sfx.NewStateMachine()
		defer sm.Wait()
		defer cancel()

		re := gtest.ReceiveSoon(t, sfx.RoundEntranceOutCh)

		// Set up consensus strategy expectation before mocking the response.
		cStrat := sfx.CStrat
		enterCh := cStrat.ExpectEnterRound(1, 0, nil)

		// Channel is 1-buffered, don't have to select.
		vrv := sfx.EmptyVRV(1, 0)
		re.Response <- tmeil.RoundEntranceResponse{VRV: vrv}

		// And since we sent a VRV, the state machine calls into consensus strategy,
		// with vrv's RoundView.
		erc := gtest.ReceiveSoon(t, enterCh)
		require.Equal(t, vrv.RoundView, erc.RV)

		pb1 := sfx.Fx.NextProposedBlock([]byte("app_data_1"), 1)
		sfx.Fx.SignProposal(ctx, &pb1, 1)
		vrv.ProposedBlocks = []tmconsensus.ProposedBlock{pb1}
		vrv.Version++

		// Sending an updated set of proposed blocks...
		gtest.SendSoon(t, sfx.RoundViewInCh, tmeil.StateMachineRoundView{VRV: vrv})

		// ... forces the consensus strategy to consider the available proposed blocks.
		pbReq := gtest.ReceiveSoon(t, cStrat.ConsiderProposedBlocksRequests)
		require.Equal(t, []tmconsensus.ProposedBlock{pb1}, pbReq.PBs)
		require.Equal(t, []string{string(pb1.Block.Hash)}, pbReq.Reason.NewProposedBlocks)

		// Don't make a decision yet.
		// This send synchronizes the test, which is blocked on the consensus strategy handling.
		gtest.SendSoon(t, pbReq.ChoiceError, tmconsensus.ErrProposedBlockChoiceNotReady)

		// Now the block data arrives.
		gtest.SendSoon(t, sfx.BlockDataArrivalCh, tmelink.BlockDataArrival{
			Height: 1, Round: 0,
			ID: string(pb1.Block.DataID),
		})

		// This triggers a new consider request.
		pbReq = gtest.ReceiveSoon(t, cStrat.ConsiderProposedBlocksRequests)

		// Proposed blocks unchanged.
		require.Equal(t, []tmconsensus.ProposedBlock{pb1}, pbReq.PBs)

		// No new blocks.
		require.Empty(t, pbReq.Reason.NewProposedBlocks)

		// The block data is indicated as the reason.
		require.Equal(t, []string{string(pb1.Block.DataID)}, pbReq.Reason.UpdatedBlockDataIDs)
	})

	t.Run("matching, after proposed block received during enter round", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		sfx := tmstatetest.NewFixture(ctx, t, 4)

		sm := sfx.NewStateMachine()
		defer sm.Wait()
		defer cancel()

		re := gtest.ReceiveSoon(t, sfx.RoundEntranceOutCh)

		// Set up consensus strategy expectation before mocking the response.
		cStrat := sfx.CStrat
		enterCh := cStrat.ExpectEnterRound(1, 0, nil)

		// Channel is 1-buffered, don't have to select.
		vrv := sfx.EmptyVRV(1, 0)
		pb1 := sfx.Fx.NextProposedBlock([]byte("app_data_1"), 1)
		sfx.Fx.SignProposal(ctx, &pb1, 1)
		vrv.ProposedBlocks = []tmconsensus.ProposedBlock{pb1}

		re.Response <- tmeil.RoundEntranceResponse{VRV: vrv}

		// And since we sent a VRV, the state machine calls into consensus strategy,
		// with vrv's RoundView.
		erc := gtest.ReceiveSoon(t, enterCh)
		require.Equal(t, vrv.RoundView, erc.RV)

		// ... forces the consensus strategy to consider the available proposed blocks.
		pbReq := gtest.ReceiveSoon(t, cStrat.ConsiderProposedBlocksRequests)
		require.Equal(t, []tmconsensus.ProposedBlock{pb1}, pbReq.PBs)
		require.Equal(t, []string{string(pb1.Block.Hash)}, pbReq.Reason.NewProposedBlocks)

		// Don't make a decision yet.
		// This send synchronizes the test, which is blocked on the consensus strategy handling.
		gtest.SendSoon(t, pbReq.ChoiceError, tmconsensus.ErrProposedBlockChoiceNotReady)

		// Now the block data arrives.
		gtest.SendSoon(t, sfx.BlockDataArrivalCh, tmelink.BlockDataArrival{
			Height: 1, Round: 0,
			ID: string(pb1.Block.DataID),
		})

		// This triggers a new consider request.
		pbReq = gtest.ReceiveSoon(t, cStrat.ConsiderProposedBlocksRequests)

		// Proposed blocks unchanged.
		require.Equal(t, []tmconsensus.ProposedBlock{pb1}, pbReq.PBs)

		// No new blocks.
		require.Empty(t, pbReq.Reason.NewProposedBlocks)

		// The block data is indicated as the reason.
		require.Equal(t, []string{string(pb1.Block.DataID)}, pbReq.Reason.UpdatedBlockDataIDs)
	})

	t.Run("separate, multiple, valid block data arrivals", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		sfx := tmstatetest.NewFixture(ctx, t, 4)

		sm := sfx.NewStateMachine()
		defer sm.Wait()
		defer cancel()

		re := gtest.ReceiveSoon(t, sfx.RoundEntranceOutCh)

		// Set up consensus strategy expectation before mocking the response.
		cStrat := sfx.CStrat
		enterCh := cStrat.ExpectEnterRound(1, 0, nil)

		// Channel is 1-buffered, don't have to select.
		vrv := sfx.EmptyVRV(1, 0)
		pb1 := sfx.Fx.NextProposedBlock([]byte("app_data_1_1"), 1)
		sfx.Fx.SignProposal(ctx, &pb1, 1)
		pb2 := sfx.Fx.NextProposedBlock([]byte("app_data_1_2"), 2)
		sfx.Fx.SignProposal(ctx, &pb1, 2)
		vrv.ProposedBlocks = []tmconsensus.ProposedBlock{pb1, pb2}

		re.Response <- tmeil.RoundEntranceResponse{VRV: vrv}

		// And since we sent a VRV, the state machine calls into consensus strategy,
		// with vrv's RoundView.
		erc := gtest.ReceiveSoon(t, enterCh)
		require.Equal(t, vrv.RoundView, erc.RV)

		// ... forces the consensus strategy to consider the available proposed blocks.
		pbReq := gtest.ReceiveSoon(t, cStrat.ConsiderProposedBlocksRequests)
		require.Equal(t, []tmconsensus.ProposedBlock{pb1, pb2}, pbReq.PBs)
		require.ElementsMatch(t, []string{string(pb1.Block.Hash), string(pb2.Block.Hash)}, pbReq.Reason.NewProposedBlocks)

		// Don't make a decision yet.
		// This send synchronizes the test, which is blocked on the consensus strategy handling.
		gtest.SendSoon(t, pbReq.ChoiceError, tmconsensus.ErrProposedBlockChoiceNotReady)

		// Now the block data arrives for one.
		gtest.SendSoon(t, sfx.BlockDataArrivalCh, tmelink.BlockDataArrival{
			Height: 1, Round: 0,
			ID: string(pb1.Block.DataID),
		})

		// This triggers a new consider request.
		pbReq = gtest.ReceiveSoon(t, cStrat.ConsiderProposedBlocksRequests)

		// Proposed blocks unchanged.
		require.Equal(t, []tmconsensus.ProposedBlock{pb1, pb2}, pbReq.PBs)

		// No new blocks.
		require.Empty(t, pbReq.Reason.NewProposedBlocks)

		// The block data is indicated as the reason.
		require.Equal(t, []string{string(pb1.Block.DataID)}, pbReq.Reason.UpdatedBlockDataIDs)

		// No choice yet.
		gtest.SendSoon(t, pbReq.ChoiceError, tmconsensus.ErrProposedBlockChoiceNotReady)

		// Now the other data arrives.
		gtest.SendSoon(t, sfx.BlockDataArrivalCh, tmelink.BlockDataArrival{
			Height: 1, Round: 0,
			ID: string(pb2.Block.DataID),
		})

		// This triggers a new consider request.
		pbReq = gtest.ReceiveSoon(t, cStrat.ConsiderProposedBlocksRequests)
		require.Equal(t, []tmconsensus.ProposedBlock{pb1, pb2}, pbReq.PBs)
		require.Empty(t, pbReq.Reason.NewProposedBlocks)
		require.Equal(t, []string{string(pb2.Block.DataID)}, pbReq.Reason.UpdatedBlockDataIDs)
	})

	t.Run("block data arrives before first proposed block", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		sfx := tmstatetest.NewFixture(ctx, t, 4)

		sm := sfx.NewStateMachine()
		defer sm.Wait()
		defer cancel()

		re := gtest.ReceiveSoon(t, sfx.RoundEntranceOutCh)

		// Set up consensus strategy expectation before mocking the response.
		cStrat := sfx.CStrat
		enterCh := cStrat.ExpectEnterRound(1, 0, nil)

		// Channel is 1-buffered, don't have to select.
		vrv := sfx.EmptyVRV(1, 0)
		re.Response <- tmeil.RoundEntranceResponse{VRV: vrv}

		// And since we sent a VRV, the state machine calls into consensus strategy,
		// with vrv's RoundView.
		erc := gtest.ReceiveSoon(t, enterCh)
		require.Equal(t, vrv.RoundView, erc.RV)

		// Make the proposed block, but don't send it yet.
		pb1 := sfx.Fx.NextProposedBlock([]byte("app_data_1"), 1)
		sfx.Fx.SignProposal(ctx, &pb1, 1)

		// The block data arrives.
		// Maybe it's a push model, or maybe the proposed block connection was just slow.
		gtest.SendSoon(t, sfx.BlockDataArrivalCh, tmelink.BlockDataArrival{
			Height: 1, Round: 0,
			ID: string(pb1.Block.DataID),
		})

		// The consider proposed block request isn't sending yet.
		gtest.NotSending(t, cStrat.ConsiderProposedBlocksRequests)

		// Now a new VRV arrives.
		vrv.ProposedBlocks = []tmconsensus.ProposedBlock{pb1}
		vrv.Version++
		gtest.SendSoon(t, sfx.RoundViewInCh, tmeil.StateMachineRoundView{VRV: vrv})

		// And we get a new consider request.
		pbReq := gtest.ReceiveSoon(t, cStrat.ConsiderProposedBlocksRequests)
		require.Equal(t, []tmconsensus.ProposedBlock{pb1}, pbReq.PBs)
		require.Equal(t, []string{string(pb1.Block.Hash)}, pbReq.Reason.NewProposedBlocks)

		// Because we got the block data before the new block,
		// the updated block data IDs is empty.
		// It is the consensus strategy's responsibility to check the new blocks' data.
		require.Empty(t, pbReq.Reason.UpdatedBlockDataIDs)
	})

	t.Run("height and round mismatches", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		sfx := tmstatetest.NewFixture(ctx, t, 4)

		sm := sfx.NewStateMachine()
		defer sm.Wait()
		defer cancel()

		re := gtest.ReceiveSoon(t, sfx.RoundEntranceOutCh)

		// Set up consensus strategy expectation before mocking the response.
		cStrat := sfx.CStrat
		enterCh := cStrat.ExpectEnterRound(1, 0, nil)

		// Channel is 1-buffered, don't have to select.
		vrv := sfx.EmptyVRV(1, 0)
		pb1 := sfx.Fx.NextProposedBlock([]byte("app_data_1"), 1)
		sfx.Fx.SignProposal(ctx, &pb1, 1)
		vrv.ProposedBlocks = []tmconsensus.ProposedBlock{pb1}

		re.Response <- tmeil.RoundEntranceResponse{VRV: vrv}

		// And since we sent a VRV, the state machine calls into consensus strategy,
		// with vrv's RoundView.
		erc := gtest.ReceiveSoon(t, enterCh)
		require.Equal(t, vrv.RoundView, erc.RV)

		// ... forces the consensus strategy to consider the available proposed blocks.
		pbReq := gtest.ReceiveSoon(t, cStrat.ConsiderProposedBlocksRequests)
		require.Equal(t, []tmconsensus.ProposedBlock{pb1}, pbReq.PBs)
		require.Equal(t, []string{string(pb1.Block.Hash)}, pbReq.Reason.NewProposedBlocks)

		// Don't make a decision yet.
		// This send synchronizes the test, which is blocked on the consensus strategy handling.
		gtest.SendSoon(t, pbReq.ChoiceError, tmconsensus.ErrProposedBlockChoiceNotReady)

		// Now a variety of block data arrives.
		gtest.SendSoon(t, sfx.BlockDataArrivalCh, tmelink.BlockDataArrival{
			Height: 1, Round: 1, // Right height, wrong round.
			ID: string(pb1.Block.DataID),
		})
		gtest.SendSoon(t, sfx.BlockDataArrivalCh, tmelink.BlockDataArrival{
			Height: 2, Round: 0, // Wrong height, right round.
			ID: string(pb1.Block.DataID),
		})
		gtest.SendSoon(t, sfx.BlockDataArrivalCh, tmelink.BlockDataArrival{
			Height: 1, Round: 0, // Right height and round.
			ID: string(pb1.Block.DataID) + "!", // Modified ID.
		})

		// None of these triggered a consider request.
		gtest.NotSendingSoon(t, cStrat.ConsiderProposedBlocksRequests)
	})
}

func TestStateMachine_metrics(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sfx := tmstatetest.NewFixture(ctx, t, 4)

	mCh := make(chan tmemetrics.Metrics)
	mc := tmemetrics.NewCollector(ctx, 4, mCh)
	defer mc.Wait()
	defer cancel()
	mc.UpdateMirror(tmemetrics.MirrorMetrics{
		VH: 1, VR: 0,
	})
	sfx.Cfg.MetricsCollector = mc

	sm := sfx.NewStateMachine()
	defer sm.Wait()
	defer cancel()

	re := gtest.ReceiveSoon(t, sfx.RoundEntranceOutCh)

	// Set up consensus strategy expectation before mocking the response.
	cStrat := sfx.CStrat
	_ = cStrat.ExpectEnterRound(1, 0, nil)

	vrv := sfx.EmptyVRV(1, 0)
	re.Response <- tmeil.RoundEntranceResponse{VRV: vrv} // No PrevBlockHash at initial height.

	m := gtest.ReceiveSoon(t, mCh)
	require.Equal(t, uint64(1), m.StateMachineHeight)
	require.Zero(t, m.StateMachineRound)

	pb1 := sfx.Fx.NextProposedBlock([]byte("app_data_1"), 1)
	sfx.Fx.SignProposal(ctx, &pb1, 1)
	vrv.ProposedBlocks = []tmconsensus.ProposedBlock{pb1}
	vrv.Version++

	vrv = sfx.Fx.UpdateVRVPrevotes(ctx, vrv, map[string][]int{
		string(pb1.Block.Hash): {0, 1, 2, 3},
	})
	vrv = sfx.Fx.UpdateVRVPrecommits(ctx, vrv, map[string][]int{
		string(pb1.Block.Hash): {0, 1, 2, 3},
	})

	gtest.SendSoon(t, sfx.RoundViewInCh, tmeil.StateMachineRoundView{VRV: vrv})

	finReq := gtest.ReceiveSoon(t, sfx.FinalizeBlockRequests)
	finReq.Resp <- tmdriver.FinalizeBlockResponse{
		Height: 1, Round: 0,
		BlockHash:  pb1.Block.Hash,
		Validators: pb1.Block.Validators,
	}
	require.NoError(t, sfx.RoundTimer.ElapseCommitWaitTimer(1, 0))

	// Don't inspect the metrics again until after the state machine enters the next round.
	enter2Ch := cStrat.ExpectEnterRound(2, 0, nil)

	re = gtest.ReceiveSoon(t, sfx.RoundEntranceOutCh)
	re.Response <- tmeil.RoundEntranceResponse{VRV: sfx.EmptyVRV(2, 0)}

	_ = gtest.ReceiveSoon(t, enter2Ch)

	m = gtest.ReceiveSoon(t, mCh)
	require.Equal(t, uint64(2), m.StateMachineHeight)
	require.Zero(t, m.StateMachineRound)
}
