package tmi_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/rollchains/gordian/gcrypto"
	"github.com/rollchains/gordian/internal/gtest"
	"github.com/rollchains/gordian/tm/tmconsensus"
	"github.com/rollchains/gordian/tm/tmengine/internal/tmeil"
	"github.com/rollchains/gordian/tm/tmengine/internal/tmmirror/internal/tmi"
	"github.com/stretchr/testify/require"
)

// Normally, the Mirror does a view lookup before attempting to add a prevote or precommit.
// But, if there is a view shift between the lookup and the attempt to apply the vote,
// there is a chance that the next lookup will fail.
// This is difficult to test at the Mirror layer,
// so we construct the request against the kernel directly in this test.
func TestKernel_votesBeforeVotingRound(t *testing.T) {
	for _, tc := range []struct {
		voteType   string
		viewStatus tmi.ViewLookupStatus
	}{
		{voteType: "prevote", viewStatus: tmi.ViewBeforeCommitting},
		{voteType: "prevote", viewStatus: tmi.ViewOrphaned},
		{voteType: "precommit", viewStatus: tmi.ViewBeforeCommitting},
		{voteType: "precommit", viewStatus: tmi.ViewOrphaned},
	} {
		tc := tc
		t.Run(fmt.Sprintf("%s into %s", tc.voteType, tc.viewStatus.String()), func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			kfx := NewKernelFixture(ctx, t, 2)

			k := kfx.NewKernel()
			defer k.Wait()
			defer cancel()

			// Proposed block at height 1.
			pb1 := kfx.Fx.NextProposedBlock([]byte("app_data_1"), 0)
			kfx.Fx.SignProposal(ctx, &pb1, 0)

			// Proposed blocks are sent directly.
			_ = gtest.ReceiveSoon(t, kfx.GossipStrategyOut)
			gtest.SendSoon(t, kfx.AddPBRequests, pb1)
			_ = gtest.ReceiveSoon(t, kfx.GossipStrategyOut)

			commitProof1 := kfx.Fx.PrecommitSignatureProof(
				ctx,
				tmconsensus.VoteTarget{Height: 1, Round: 0, BlockHash: string(pb1.Block.Hash)},
				nil,
				[]int{0, 1},
			)
			commitResp1 := make(chan tmi.AddVoteResult, 1)
			commitReq1 := tmi.AddPrecommitRequest{
				H: 1,
				R: 0,

				PrecommitUpdates: map[string]tmi.VoteUpdate{
					string(pb1.Block.Hash): {
						PrevVersion: 0, // First precommit for the given block: zero means it didn't exist before.
						Proof:       commitProof1,
					},
				},

				Response: commitResp1,
			}

			gtest.SendSoon(t, kfx.AddPrecommitRequests, commitReq1)

			resp := gtest.ReceiveSoon(t, commitResp1)
			require.Equal(t, tmi.AddVoteAccepted, resp)

			// Confirm vote applied after being accepted
			// (since the kernel does some work in the background here).
			votingVRV := gtest.ReceiveSoon(t, kfx.GossipStrategyOut).Voting
			require.Equal(t, uint64(2), votingVRV.Height)

			// Update the fixture and go through the next height.
			kfx.Fx.CommitBlock(pb1.Block, []byte("app_state_1"), 0, map[string]gcrypto.CommonMessageSignatureProof{
				string(pb1.Block.Hash): commitProof1,
			})

			pb2 := kfx.Fx.NextProposedBlock([]byte("app_data_2"), 0)
			kfx.Fx.SignProposal(ctx, &pb2, 0)
			gtest.SendSoon(t, kfx.AddPBRequests, pb2)
			_ = gtest.ReceiveSoon(t, kfx.GossipStrategyOut)

			commitProof2 := kfx.Fx.PrecommitSignatureProof(
				ctx,
				tmconsensus.VoteTarget{Height: 2, Round: 0, BlockHash: string(pb2.Block.Hash)},
				nil,
				[]int{0, 1},
			)
			commitResp2 := make(chan tmi.AddVoteResult, 1)
			commitReq2 := tmi.AddPrecommitRequest{
				H: 2,
				R: 0,

				PrecommitUpdates: map[string]tmi.VoteUpdate{
					string(pb2.Block.Hash): {
						PrevVersion: 0, // First precommit for the given block: zero means it didn't exist before.
						Proof:       commitProof2,
					},
				},

				Response: commitResp2,
			}

			gtest.SendSoon(t, kfx.AddPrecommitRequests, commitReq2)

			resp = gtest.ReceiveSoon(t, commitResp2)
			require.Equal(t, tmi.AddVoteAccepted, resp)

			// Confirm on voting height 3.
			votingVRV = gtest.ReceiveSoon(t, kfx.GossipStrategyOut).Voting
			require.Equal(t, uint64(3), votingVRV.Height)

			// Check if we need to advance the voting round.
			if tc.viewStatus == tmi.ViewOrphaned {
				commitProof3 := kfx.Fx.PrecommitSignatureProof(
					ctx,
					tmconsensus.VoteTarget{Height: 3, Round: 0, BlockHash: ""},
					nil,
					[]int{0, 1},
				)
				commitResp3 := make(chan tmi.AddVoteResult, 1)
				commitReq3 := tmi.AddPrecommitRequest{
					H: 3,
					R: 0,

					PrecommitUpdates: map[string]tmi.VoteUpdate{
						"": {
							PrevVersion: 0, // First precommit for the given block: zero means it didn't exist before.
							Proof:       commitProof3,
						},
					},

					Response: commitResp3,
				}

				gtest.SendSoon(t, kfx.AddPrecommitRequests, commitReq3)
				resp = gtest.ReceiveSoon(t, commitResp3)
				require.Equal(t, tmi.AddVoteAccepted, resp)

				// Confirm on voting height 3, round 1.
				votingVRV = gtest.ReceiveSoon(t, kfx.GossipStrategyOut).Voting
				require.Equal(t, uint64(3), votingVRV.Height)
				require.Equal(t, uint32(1), votingVRV.Round)
			}

			var targetHeight uint64
			var targetBlockHash string
			switch tc.viewStatus {
			case tmi.ViewOrphaned:
				// Nil vote at 3/0.
				targetHeight = 3
				targetBlockHash = ""
			case tmi.ViewBeforeCommitting:
				targetHeight = 1
				targetBlockHash = string(pb1.Block.Hash)
			default:
				t.Fatalf("BUG: unhandled view status %s", tc.viewStatus)
			}

			switch tc.voteType {
			case "prevote":
				proof := kfx.Fx.PrevoteSignatureProof(
					ctx,
					tmconsensus.VoteTarget{Height: targetHeight, Round: 0, BlockHash: targetBlockHash},
					nil,
					[]int{0, 1},
				)
				resp := make(chan tmi.AddVoteResult, 1)
				req := tmi.AddPrevoteRequest{
					H: targetHeight,
					R: 0,

					PrevoteUpdates: map[string]tmi.VoteUpdate{
						targetBlockHash: {
							PrevVersion: 0, // First precommit for the given block: zero means it didn't exist before.
							Proof:       proof,
						},
					},

					Response: resp,
				}

				gtest.SendSoon(t, kfx.AddPrevoteRequests, req)
				result := gtest.ReceiveSoon(t, resp)
				require.Equal(t, tmi.AddVoteOutOfDate, result)
			case "precommit":
				proof := kfx.Fx.PrecommitSignatureProof(
					ctx,
					tmconsensus.VoteTarget{Height: targetHeight, Round: 0, BlockHash: targetBlockHash},
					nil,
					[]int{0, 1},
				)
				resp := make(chan tmi.AddVoteResult, 1)
				req := tmi.AddPrecommitRequest{
					H: targetHeight,
					R: 0,

					PrecommitUpdates: map[string]tmi.VoteUpdate{
						targetBlockHash: {
							PrevVersion: 0, // First precommit for the given block: zero means it didn't exist before.
							Proof:       proof,
						},
					},

					Response: resp,
				}

				gtest.SendSoon(t, kfx.AddPrecommitRequests, req)
				result := gtest.ReceiveSoon(t, resp)
				require.Equal(t, tmi.AddVoteOutOfDate, result)
			default:
				t.Fatalf("BUG: unhandled vote type %s", tc.voteType)
			}
		})
	}
}

// Regression test: if the state update is not a clone of the kernel's VRV,
// there is a possible data race when the kernel next modifies that VRV.
func TestKernel_initialStateUpdateToStateMachineUsesVRVClone(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	kfx := NewKernelFixture(ctx, t, 4)

	k := kfx.NewKernel()
	defer k.Wait()
	defer cancel()

	// Simulate the state machine round action input.
	re := tmeil.StateMachineRoundEntrance{
		H: 1, R: 0,

		PubKey: nil,

		Actions: make(chan tmeil.StateMachineRoundAction, 3),

		Response: make(chan tmeil.RoundEntranceResponse, 1),
	}

	gtest.SendSoon(t, kfx.StateMachineRoundEntranceIn, re)

	rer := gtest.ReceiveSoon(t, re.Response)

	// Now we will do three modifications to be extra sure this is a clone.
	// Change the version, add a proposed block directly, and modify the vote summary directly.
	// None of these are likely to happen in practice,
	// but they are simple checks to ensure we have a clone, not a reference.
	pb3 := kfx.Fx.NextProposedBlock([]byte("val3"), 3)
	origVersion := rer.VRV.Version
	rer.VRV.Version = 12345
	rer.VRV.ProposedBlocks = append(rer.VRV.ProposedBlocks, pb3)
	rer.VRV.VoteSummary.PrevoteBlockPower["not_a_block"] = 1

	// If those fields were modified on the kernel's copy of the VRV,
	// those would be included in the next update we force by sending a different proposed block.
	pb1 := kfx.Fx.NextProposedBlock([]byte("app_data_1"), 0)
	kfx.Fx.SignProposal(ctx, &pb1, 0)

	gtest.SendSoon(t, kfx.AddPBRequests, pb1)

	vrv := gtest.ReceiveSoon(t, kfx.StateMachineRoundViewOut).VRV

	// It didn't keep our version change.
	require.Equal(t, origVersion+1, vrv.Version)
	// It only has the proposed block we simulated from the network.
	// (Dubious test since the VRV slice may have been nil.)
	require.Equal(t, []tmconsensus.ProposedBlock{pb1}, vrv.ProposedBlocks)
	// And it doesn't have the bogus change we added to our copy of the vote summary.
	require.Empty(t, vrv.VoteSummary.PrevoteBlockPower)
}
