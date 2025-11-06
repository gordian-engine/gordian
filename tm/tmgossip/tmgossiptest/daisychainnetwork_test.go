package tmgossiptest_test

import (
	"context"
	"testing"

	"github.com/gordian-engine/gordian/internal/gtest"
	"github.com/gordian-engine/gordian/tm/tmconsensus"
	"github.com/gordian-engine/gordian/tm/tmconsensus/tmconsensustest"
	"github.com/gordian-engine/gordian/tm/tmengine/tmelink"
	"github.com/gordian-engine/gordian/tm/tmgossip/tmgossiptest"
	"github.com/stretchr/testify/require"
)

func TestDaisyChainNetwork_messagePropagation(t *testing.T) {
	t.Run("proposed headers", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		const nStrats = 3
		nfx := tmgossiptest.NewDaisyChainFixture(ctx, nStrats)
		defer nfx.Wait()
		defer cancel()

		fx := tmconsensustest.NewEd25519Fixture(nStrats)

		// Update from left side.
		ph0 := fx.NextProposedHeader([]byte("data0"), 0)
		nfx.Stores[0].PutData(ph0.Header.DataID, []byte("some data"))

		gtest.SendSoon(t, nfx.UpdateChs[0], tmelink.NetworkViewUpdate{
			Voting: &tmconsensus.VersionedRoundView{
				RoundView: tmconsensus.RoundView{
					Height: 1,
					Round:  0,

					ValidatorSet: fx.ValSet(),

					ProposedHeaders: []tmconsensus.ProposedHeader{ph0},
				},
				Version: 1,
			},
		})

		ph01 := gtest.ReceiveSoon(t, nfx.Handlers[1].IncomingProposals())
		require.Equal(t, ph0, ph01)

		ph02 := gtest.ReceiveSoon(t, nfx.Handlers[2].IncomingProposals())
		require.Equal(t, ph0, ph02)

		// Update from right side.
		ph2 := fx.NextProposedHeader([]byte("data2"), 2)
		nfx.Stores[2].PutData(ph2.Header.DataID, []byte("some more data"))

		gtest.SendSoon(t, nfx.UpdateChs[2], tmelink.NetworkViewUpdate{
			Voting: &tmconsensus.VersionedRoundView{
				RoundView: tmconsensus.RoundView{
					Height: 1,
					Round:  0,

					ValidatorSet: fx.ValSet(),

					ProposedHeaders: []tmconsensus.ProposedHeader{ph2},
				},
				Version: 1,
			},
		})

		ph21 := gtest.ReceiveSoon(t, nfx.Handlers[1].IncomingProposals())
		require.Equal(t, ph2, ph21)

		ph20 := gtest.ReceiveSoon(t, nfx.Handlers[0].IncomingProposals())
		require.Equal(t, ph2, ph20)
	})

	t.Run("prevotes", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		const nStrats = 3
		nfx := tmgossiptest.NewDaisyChainFixture(ctx, nStrats)
		defer nfx.Wait()
		defer cancel()

		fx := tmconsensustest.NewEd25519Fixture(nStrats)

		// Update from left side.
		gtest.SendSoon(t, nfx.UpdateChs[0], tmelink.NetworkViewUpdate{
			Voting: &tmconsensus.VersionedRoundView{
				RoundView: tmconsensus.RoundView{
					Height: 1,
					Round:  0,

					ValidatorSet: fx.ValSet(),

					PrevoteProofs: fx.PrevoteProofMap(
						ctx, 1, 0,
						map[string][]int{
							"": {0},
						},
					),
				},
				Version: 1,
			},
		})

		exp0 := tmconsensus.PrevoteSparseProof{
			Height:     1,
			Round:      0,
			PubKeyHash: string(fx.ValSet().PubKeyHash),
			Proofs: fx.SparsePrevoteProofMap(
				ctx, 1, 0,
				map[string][]int{"": {0}},
			),
		}
		v01 := gtest.ReceiveSoon(t, nfx.Handlers[1].IncomingPrevoteProofs())
		require.Equal(t, exp0, v01)
		v02 := gtest.ReceiveSoon(t, nfx.Handlers[2].IncomingPrevoteProofs())
		require.Equal(t, exp0, v02)

		// Now update from the right side.
		gtest.SendSoon(t, nfx.UpdateChs[2], tmelink.NetworkViewUpdate{
			Voting: &tmconsensus.VersionedRoundView{
				RoundView: tmconsensus.RoundView{
					Height: 1,
					Round:  0,

					ValidatorSet: fx.ValSet(),

					PrevoteProofs: fx.PrevoteProofMap(
						ctx, 1, 0,
						map[string][]int{
							"": {2},
						},
					),
				},
				Version: 1,
			},
		})

		exp2 := tmconsensus.PrevoteSparseProof{
			Height:     1,
			Round:      0,
			PubKeyHash: string(fx.ValSet().PubKeyHash),
			Proofs: fx.SparsePrevoteProofMap(
				ctx, 1, 0,
				map[string][]int{"": {2}},
			),
		}
		v21 := gtest.ReceiveSoon(t, nfx.Handlers[1].IncomingPrevoteProofs())
		require.Equal(t, exp2, v21)
		v20 := gtest.ReceiveSoon(t, nfx.Handlers[0].IncomingPrevoteProofs())
		require.Equal(t, exp2, v20)
	})

	t.Run("precommits", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		const nStrats = 3
		nfx := tmgossiptest.NewDaisyChainFixture(ctx, nStrats)
		defer nfx.Wait()
		defer cancel()

		fx := tmconsensustest.NewEd25519Fixture(nStrats)

		// Update from left side.
		gtest.SendSoon(t, nfx.UpdateChs[0], tmelink.NetworkViewUpdate{
			Voting: &tmconsensus.VersionedRoundView{
				RoundView: tmconsensus.RoundView{
					Height: 1,
					Round:  0,

					ValidatorSet: fx.ValSet(),

					PrecommitProofs: fx.PrecommitProofMap(
						ctx, 1, 0,
						map[string][]int{
							"": {0},
						},
					),
				},
				Version: 1,
			},
		})

		exp0 := tmconsensus.PrecommitSparseProof{
			Height:     1,
			Round:      0,
			PubKeyHash: string(fx.ValSet().PubKeyHash),
			Proofs: fx.SparsePrecommitProofMap(
				ctx, 1, 0,
				map[string][]int{"": {0}},
			),
		}
		v01 := gtest.ReceiveSoon(t, nfx.Handlers[1].IncomingPrecommitProofs())
		require.Equal(t, exp0, v01)
		v02 := gtest.ReceiveSoon(t, nfx.Handlers[2].IncomingPrecommitProofs())
		require.Equal(t, exp0, v02)

		// Now update from the right side.
		gtest.SendSoon(t, nfx.UpdateChs[2], tmelink.NetworkViewUpdate{
			Voting: &tmconsensus.VersionedRoundView{
				RoundView: tmconsensus.RoundView{
					Height: 1,
					Round:  0,

					ValidatorSet: fx.ValSet(),

					PrecommitProofs: fx.PrecommitProofMap(
						ctx, 1, 0,
						map[string][]int{
							"": {2},
						},
					),
				},
				Version: 1,
			},
		})

		exp2 := tmconsensus.PrecommitSparseProof{
			Height:     1,
			Round:      0,
			PubKeyHash: string(fx.ValSet().PubKeyHash),
			Proofs: fx.SparsePrecommitProofMap(
				ctx, 1, 0,
				map[string][]int{"": {2}},
			),
		}
		v21 := gtest.ReceiveSoon(t, nfx.Handlers[1].IncomingPrecommitProofs())
		require.Equal(t, exp2, v21)
		v20 := gtest.ReceiveSoon(t, nfx.Handlers[0].IncomingPrecommitProofs())
		require.Equal(t, exp2, v20)
	})
}
