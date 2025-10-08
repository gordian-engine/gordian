package tmi_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/gordian-engine/gordian/gcrypto"
	"github.com/gordian-engine/gordian/internal/gtest"
	"github.com/gordian-engine/gordian/tm/tmconsensus"
	"github.com/gordian-engine/gordian/tm/tmengine/internal/tmeil"
	"github.com/gordian-engine/gordian/tm/tmengine/internal/tmmirror/internal/tmi"
	"github.com/gordian-engine/gordian/tm/tmengine/tmelink"
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

			// Proposed header at height 1.
			ph1 := kfx.Fx.NextProposedHeader([]byte("app_data_1"), 0)
			kfx.Fx.SignProposal(ctx, &ph1, 0)

			// Proposed headers are sent directly.
			_ = gtest.ReceiveSoon(t, kfx.GossipStrategyOut)
			gtest.SendSoon(t, kfx.AddPHRequests, ph1)
			_ = gtest.ReceiveSoon(t, kfx.GossipStrategyOut)

			commitProof1 := kfx.Fx.PrecommitSignatureProof(
				ctx,
				tmconsensus.VoteTarget{Height: 1, Round: 0, BlockHash: string(ph1.Header.Hash)},
				nil,
				[]int{0, 1},
			)
			commitResp1 := make(chan tmi.AddVoteResult, 1)
			commitReq1 := tmi.AddPrecommitRequest{
				H: 1,
				R: 0,

				PrecommitUpdates: map[string]tmi.VoteUpdate{
					string(ph1.Header.Hash): {
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
			kfx.Fx.CommitBlock(ph1.Header, []byte("app_state_1"), 0, map[string]gcrypto.CommonMessageSignatureProof{
				string(ph1.Header.Hash): commitProof1,
			})

			pb2 := kfx.Fx.NextProposedHeader([]byte("app_data_2"), 0)
			kfx.Fx.SignProposal(ctx, &pb2, 0)
			gtest.SendSoon(t, kfx.AddPHRequests, pb2)
			_ = gtest.ReceiveSoon(t, kfx.GossipStrategyOut)

			commitProof2 := kfx.Fx.PrecommitSignatureProof(
				ctx,
				tmconsensus.VoteTarget{Height: 2, Round: 0, BlockHash: string(pb2.Header.Hash)},
				nil,
				[]int{0, 1},
			)
			commitResp2 := make(chan tmi.AddVoteResult, 1)
			commitReq2 := tmi.AddPrecommitRequest{
				H: 2,
				R: 0,

				PrecommitUpdates: map[string]tmi.VoteUpdate{
					string(pb2.Header.Hash): {
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
				targetBlockHash = string(ph1.Header.Hash)
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
	// Change the version, add a proposed header directly, and modify the vote summary directly.
	// None of these are likely to happen in practice,
	// but they are simple checks to ensure we have a clone, not a reference.
	ph3 := kfx.Fx.NextProposedHeader([]byte("val3"), 3)
	origVersion := rer.VRV.Version
	rer.VRV.Version = 12345
	rer.VRV.ProposedHeaders = append(rer.VRV.ProposedHeaders, ph3)
	rer.VRV.VoteSummary.PrevoteBlockPower["not_a_block"] = 1

	// If those fields were modified on the kernel's copy of the VRV,
	// those would be included in the next update we force by sending a different proposed header.
	ph1 := kfx.Fx.NextProposedHeader([]byte("app_data_1"), 0)
	kfx.Fx.SignProposal(ctx, &ph1, 0)

	gtest.SendSoon(t, kfx.AddPHRequests, ph1)

	vrv := gtest.ReceiveSoon(t, kfx.StateMachineRoundViewOut).VRV

	// It didn't keep our version change.
	require.Equal(t, origVersion+1, vrv.Version)
	// It only has the proposed header we simulated from the network.
	// (Dubious test since the VRV slice may have been nil.)
	require.Equal(t, []tmconsensus.ProposedHeader{ph1}, vrv.ProposedHeaders)
	// And it doesn't have the bogus change we added to our copy of the vote summary.
	require.Empty(t, vrv.VoteSummary.PrevoteBlockPower)
}

func TestKernel_closeRoundEntranceHeightCommitted(t *testing.T) {
	t.Run("with replayed headers", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		kfx := NewKernelFixture(ctx, t, 4)

		k := kfx.NewKernel()
		defer k.Wait()
		defer cancel()

		// Simulate the state machine round action input.
		height1Committed := make(chan struct{})
		re := tmeil.StateMachineRoundEntrance{
			H: 1, R: 0,

			PubKey: nil,

			Actions: make(chan tmeil.StateMachineRoundAction, 3),

			HeightCommitted: height1Committed,

			Response: make(chan tmeil.RoundEntranceResponse, 1),
		}

		gtest.SendSoon(t, kfx.StateMachineRoundEntranceIn, re)

		ph1 := kfx.Fx.NextProposedHeader([]byte("app_data_1"), 0)
		voteMap := map[string][]int{
			string(ph1.Header.Hash): []int{0, 1, 2, 3},
		}
		precommitProofsMap := kfx.Fx.PrecommitProofMap(ctx, 1, 0, voteMap)
		kfx.Fx.CommitBlock(ph1.Header, []byte("app_state_1"), 0, precommitProofsMap)

		// Send it, and the response should indicate success.
		rhResp1 := make(chan tmelink.ReplayedHeaderResponse)
		gtest.SendSoon(t, kfx.ReplayedHeadersIn, tmelink.ReplayedHeaderRequest{
			Header: ph1.Header,
			Proof: tmconsensus.CommitProof{
				Round:      0,
				PubKeyHash: string(ph1.Header.ValidatorSet.PubKeyHash),
				Proofs:     kfx.Fx.SparsePrecommitProofMap(ctx, 1, 0, voteMap),
			},
			Resp: rhResp1,
		})
		require.Nil(t, gtest.ReceiveSoon(t, rhResp1).Err)

		// Height 1 is committing, but not committed.
		gtest.NotSending(t, height1Committed)

		// The state machine has not entered round 2 yet,
		// because it hasn't finished finalizing or whatever.
		// Meanwhile, the next height is replayed.
		ph2 := kfx.Fx.NextProposedHeader([]byte("app_data_2"), 0)
		voteMap = map[string][]int{
			string(ph2.Header.Hash): []int{0, 1, 2, 3},
		}
		precommitProofsMap = kfx.Fx.PrecommitProofMap(ctx, 2, 0, voteMap)
		kfx.Fx.CommitBlock(ph2.Header, []byte("app_state_2"), 0, precommitProofsMap)

		// Send it, and the response should indicate success.
		rhResp2 := make(chan tmelink.ReplayedHeaderResponse)
		gtest.SendSoon(t, kfx.ReplayedHeadersIn, tmelink.ReplayedHeaderRequest{
			Header: ph2.Header,
			Proof: tmconsensus.CommitProof{
				Round:      0,
				PubKeyHash: string(ph2.Header.ValidatorSet.PubKeyHash),
				Proofs:     kfx.Fx.SparsePrecommitProofMap(ctx, 2, 0, voteMap),
			},
			Resp: rhResp2,
		})
		require.Nil(t, gtest.ReceiveSoon(t, rhResp2).Err)

		// This puts height 2 in committing, which means height 1 is now committed.
		_ = gtest.ReceiveSoon(t, height1Committed)
	})

	// TODO: another subtest, with non-committed headers.
}

func TestKernel_lag(t *testing.T) {
	t.Run("initializing at startup", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		kfx := NewKernelFixture(ctx, t, 4)

		k := kfx.NewKernel()
		defer k.Wait()
		defer cancel()

		ls := gtest.ReceiveSoon(t, kfx.LagStateOut)
		require.Equal(t, tmelink.LagState{
			Status:           tmelink.LagStatusInitializing,
			CommittingHeight: 0,
			NeedHeight:       0,
		}, ls)
	})

	t.Run("up to date when new proposed header for voting height received", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		kfx := NewKernelFixture(ctx, t, 4)

		k := kfx.NewKernel()
		defer k.Wait()
		defer cancel()

		require.Equal(
			t,
			tmelink.LagStatusInitializing,
			gtest.ReceiveSoon(t, kfx.LagStateOut).Status,
		)

		ph1 := kfx.Fx.NextProposedHeader([]byte("app_data_1"), 0)
		kfx.Fx.SignProposal(ctx, &ph1, 0)
		gtest.SendSoon(t, kfx.AddPHRequests, ph1)

		ls := gtest.ReceiveSoon(t, kfx.LagStateOut)
		require.Equal(t, tmelink.LagState{
			Status:           tmelink.LagStatusUpToDate,
			CommittingHeight: 0,
			NeedHeight:       0,
		}, ls)
	})
}

func TestKernel_initialViewLoadsPrevCommitProof(t *testing.T) {
	t.Run("when pointing at voting view", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		kfx := NewKernelFixture(ctx, t, 4)

		// Commit the first height so we can act like the state machine arrives at height 2
		// with a previous commit proof for 1.
		require.NoError(t, kfx.Cfg.Store.SetNetworkHeightRound(ctx, 2, 0, 1, 0))

		_, err := kfx.Cfg.ValidatorStore.SavePubKeys(ctx, tmconsensus.ValidatorsToPubKeys(kfx.Fx.ValSet().Validators))
		require.NoError(t, err)

		_, err = kfx.Cfg.ValidatorStore.SaveVotePowers(ctx, tmconsensus.ValidatorsToVotePowers(kfx.Fx.ValSet().Validators))
		require.NoError(t, err)

		ph1 := kfx.Fx.NextProposedHeader([]byte("app_data_1"), 0)
		kfx.Fx.SignProposal(ctx, &ph1, 0)

		vt := tmconsensus.VoteTarget{
			Height:    1,
			BlockHash: string(ph1.Header.Hash),
		}
		kfx.Fx.CommitBlock(ph1.Header, []byte("app_state_1"), 0, map[string]gcrypto.CommonMessageSignatureProof{
			string(ph1.Header.Hash): kfx.Fx.PrecommitSignatureProof(ctx, vt, nil, []int{0, 1, 2, 3}),
		})

		ph2 := kfx.Fx.NextProposedHeader([]byte("app_data_2"), 0)

		require.NoError(t, kfx.Cfg.CommittedHeaderStore.SaveCommittedHeader(ctx, tmconsensus.CommittedHeader{
			Header: ph1.Header,
			Proof:  ph2.Header.PrevCommitProof,
		}))

		require.NoError(t, kfx.Cfg.RoundStore.SaveRoundProposedHeader(ctx, ph1))
		require.NoError(t, kfx.Cfg.RoundStore.OverwriteRoundPrecommitProofs(
			ctx,
			1, 0,
			tmconsensus.SparseSignatureCollection{
				PubKeyHash:      []byte(ph2.Header.PrevCommitProof.PubKeyHash),
				BlockSignatures: ph2.Header.PrevCommitProof.Proofs,
			},
		))

		k := kfx.NewKernel()
		defer k.Wait()
		defer cancel()

		// Now the kernel should be voting 2/0,
		// so if the state machine enters 2/0,
		// it should have the previous commit proof pointing at 1/0.

		rerResp := make(chan tmeil.RoundEntranceResponse, 1)
		gtest.SendSoon(t, kfx.StateMachineRoundEntranceIn, tmeil.StateMachineRoundEntrance{
			H:        2,
			R:        0,
			Response: rerResp,
		})

		rer := gtest.ReceiveSoon(t, rerResp)
		require.Equal(t, ph2.Header.PrevCommitProof, rer.VRV.RoundView.PrevCommitProof)

		// Initially loaded vote summary should be correct.
		require.NotZero(t, rer.VRV.VoteSummary.AvailablePower)
		require.Zero(t, rer.VRV.VoteSummary.TotalPrecommitPower)
	})

	t.Run("when pointing at committing view", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		kfx := NewKernelFixture(ctx, t, 4)

		// This time we are voting on 3/0 and committing 2/0.
		// Therefore, if the state machine enters 2/0,
		// it should still have a valid previous commit proof.
		require.NoError(t, kfx.Cfg.Store.SetNetworkHeightRound(ctx, 3, 0, 2, 0))

		_, err := kfx.Cfg.ValidatorStore.SavePubKeys(ctx, tmconsensus.ValidatorsToPubKeys(kfx.Fx.ValSet().Validators))
		require.NoError(t, err)

		_, err = kfx.Cfg.ValidatorStore.SaveVotePowers(ctx, tmconsensus.ValidatorsToVotePowers(kfx.Fx.ValSet().Validators))
		require.NoError(t, err)

		ph1 := kfx.Fx.NextProposedHeader([]byte("app_data_1"), 0)
		kfx.Fx.SignProposal(ctx, &ph1, 0)

		vt := tmconsensus.VoteTarget{
			Height:    1,
			BlockHash: string(ph1.Header.Hash),
		}
		kfx.Fx.CommitBlock(ph1.Header, []byte("app_state_1"), 0, map[string]gcrypto.CommonMessageSignatureProof{
			string(ph1.Header.Hash): kfx.Fx.PrecommitSignatureProof(ctx, vt, nil, []int{0, 1, 2, 3}),
		})

		ph2 := kfx.Fx.NextProposedHeader([]byte("app_data_2"), 0)
		kfx.Fx.SignProposal(ctx, &ph2, 0)

		require.NoError(t, kfx.Cfg.CommittedHeaderStore.SaveCommittedHeader(ctx, tmconsensus.CommittedHeader{
			Header: ph1.Header,
			Proof:  ph2.Header.PrevCommitProof,
		}))

		require.NoError(t, kfx.Cfg.RoundStore.SaveRoundProposedHeader(ctx, ph1))
		require.NoError(t, kfx.Cfg.RoundStore.OverwriteRoundPrecommitProofs(
			ctx,
			1, 0,
			tmconsensus.SparseSignatureCollection{
				PubKeyHash:      []byte(ph2.Header.PrevCommitProof.PubKeyHash),
				BlockSignatures: ph2.Header.PrevCommitProof.Proofs,
			},
		))

		vt = tmconsensus.VoteTarget{
			Height:    2,
			BlockHash: string(ph2.Header.Hash),
		}
		kfx.Fx.CommitBlock(ph2.Header, []byte("app_state_2"), 0, map[string]gcrypto.CommonMessageSignatureProof{
			string(ph2.Header.Hash): kfx.Fx.PrecommitSignatureProof(ctx, vt, nil, []int{0, 1, 2, 3}),
		})

		ph3 := kfx.Fx.NextProposedHeader([]byte("app_data_3"), 0)
		require.NoError(t, kfx.Cfg.CommittedHeaderStore.SaveCommittedHeader(ctx, tmconsensus.CommittedHeader{
			Header: ph2.Header,
			Proof:  ph3.Header.PrevCommitProof,
		}))

		require.NoError(t, kfx.Cfg.RoundStore.SaveRoundProposedHeader(ctx, ph2))
		require.NoError(t, kfx.Cfg.RoundStore.OverwriteRoundPrecommitProofs(
			ctx,
			2, 0,
			tmconsensus.SparseSignatureCollection{
				PubKeyHash:      []byte(ph3.Header.PrevCommitProof.PubKeyHash),
				BlockSignatures: ph3.Header.PrevCommitProof.Proofs,
			},
		))

		k := kfx.NewKernel()
		defer k.Wait()
		defer cancel()

		// Now the kernel should be committing 2/0 and voting 3/0;
		// so if the state machine enters 2/0,
		// it should have the previous commit proof pointing at 1/0.

		rerResp := make(chan tmeil.RoundEntranceResponse, 1)
		gtest.SendSoon(t, kfx.StateMachineRoundEntranceIn, tmeil.StateMachineRoundEntrance{
			H:        2,
			R:        0,
			Response: rerResp,
		})

		rer := gtest.ReceiveSoon(t, rerResp)
		require.Equal(t, ph2.Header.PrevCommitProof, rer.VRV.RoundView.PrevCommitProof)

		require.NotZero(t, rer.VRV.VoteSummary.AvailablePower)
		require.Equal(t, rer.VRV.VoteSummary.AvailablePower, rer.VRV.VoteSummary.TotalPrecommitPower)
		require.Equal(t, rer.VRV.VoteSummary.AvailablePower, rer.VRV.VoteSummary.PrecommitBlockPower[string(ph2.Header.Hash)])
	})
}

func TestKernel_viewLookupResponse_futureViewValidators(t *testing.T) {
	t.Run("later round in voting height", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		kfx := NewKernelFixture(ctx, t, 2)

		k := kfx.NewKernel()
		defer k.Wait()
		defer cancel()

		respCh := make(chan tmi.ViewLookupResponse, 1)
		vrv := new(tmconsensus.VersionedRoundView)
		req := tmi.ViewLookupRequest{
			H: 1,
			R: 9,

			// Only need the validators for this.
			Fields: tmi.RVValidators,

			VRV: vrv,

			Reason: "test",

			Resp: respCh,
		}

		// It's reported as future status.
		gtest.SendSoon(t, kfx.ViewLookupRequests, req)
		resp := gtest.ReceiveSoon(t, respCh)
		require.Equal(t, tmi.ViewFuture, resp.Status)

		// And the validators are populated correctly.
		require.True(t, kfx.Fx.ValSet().Equal(vrv.ValidatorSet))
	})

	t.Run("later height", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		kfx := NewKernelFixture(ctx, t, 2)

		k := kfx.NewKernel()
		defer k.Wait()
		defer cancel()

		respCh := make(chan tmi.ViewLookupResponse, 1)
		vrv := new(tmconsensus.VersionedRoundView)
		req := tmi.ViewLookupRequest{
			H: 8,
			R: 0,

			// Only need the validators for this.
			Fields: tmi.RVValidators,

			VRV: vrv,

			Reason: "test",

			Resp: respCh,
		}

		// It's reported as future status.
		gtest.SendSoon(t, kfx.ViewLookupRequests, req)
		resp := gtest.ReceiveSoon(t, respCh)
		require.Equal(t, tmi.ViewFuture, resp.Status)

		// The validators are cleared --
		// the kernel does not attempt any lookup,
		// and the request does not currently contain the public key hash
		// for the kernel to compare it against the maintained views.
		require.Zero(t, vrv.ValidatorSet)
	})
}
