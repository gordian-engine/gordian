package tmstoretest

import (
	"bytes"
	"context"
	"fmt"
	"slices"
	"testing"

	"github.com/gordian-engine/gordian/tm/tmconsensus"
	"github.com/gordian-engine/gordian/tm/tmconsensus/tmconsensustest"
	"github.com/gordian-engine/gordian/tm/tmstore"
	"github.com/stretchr/testify/require"
)

type RoundStoreFactory func(cleanup func(func())) (tmstore.RoundStore, error)

func TestRoundStoreCompliance(t *testing.T, f RoundStoreFactory) {
	t.Run("nothing stored at height", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		s, err := f(t.Cleanup)
		require.NoError(t, err)

		_, _, _, err = s.LoadRoundState(ctx, 1, 0)
		require.ErrorIs(t, err, tmconsensus.RoundUnknownError{WantHeight: 1, WantRound: 0})

		_, _, _, err = s.LoadRoundState(ctx, 3, 6)
		require.ErrorIs(t, err, tmconsensus.RoundUnknownError{WantHeight: 3, WantRound: 6})
	})

	t.Run("proposed blocks", func(t *testing.T) {
		t.Run("happy path", func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			s, err := f(t.Cleanup)
			require.NoError(t, err)

			fx := tmconsensustest.NewEd25519Fixture(2)

			ph10 := fx.NextProposedHeader([]byte("val0"), 0)
			ph10.Round = 1
			fx.RecalculateHash(&ph10.Header)
			fx.SignProposal(ctx, &ph10, 0)
			require.Empty(t, ph10.Header.PrevCommitProof.Proofs)
			ph10.Header.PrevCommitProof.Proofs = nil

			ph11 := fx.NextProposedHeader([]byte("val1"), 1)
			ph11.Round = 1
			fx.RecalculateHash(&ph11.Header)
			fx.SignProposal(ctx, &ph11, 1)
			require.Empty(t, ph11.Header.PrevCommitProof.Proofs)
			ph11.Header.PrevCommitProof.Proofs = nil

			require.NoError(t, s.SaveRoundProposedHeader(ctx, ph10))
			require.NoError(t, s.SaveRoundProposedHeader(ctx, ph11))

			phs, _, _, err := s.LoadRoundState(ctx, 1, 1)
			require.NoError(t, err)

			// Using require.ElementsMatch gives poor output on mismatched lists with huge elements.
			// Ensure the two slices are sorted the same.
			want := []tmconsensus.ProposedHeader{ph10, ph11}
			slices.SortFunc(want, func(a, b tmconsensus.ProposedHeader) int {
				return bytes.Compare(a.Header.Hash, b.Header.Hash)
			})
			slices.SortFunc(phs, func(a, b tmconsensus.ProposedHeader) int {
				return bytes.Compare(a.Header.Hash, b.Header.Hash)
			})
			require.Equal(t, want, phs)

			// Now that we've done the header at initial height, let's say ph10 was committed,
			// and the next proposed header contains an actual PrevCommitProof.

			voteMap := map[string][]int{
				string(ph10.Header.Hash): {0, 1}, // Only two validators in this test.
			}
			fx.CommitBlock(ph10.Header, []byte("app_state_10"), 1, fx.PrecommitProofMap(ctx, 1, 1, voteMap))

			ph20 := fx.NextProposedHeader([]byte("val0_2"), 0)
			fx.SignProposal(ctx, &ph20, 0)

			ph21 := fx.NextProposedHeader([]byte("val1_2"), 1)
			fx.SignProposal(ctx, &ph21, 1)

			require.NoError(t, s.SaveRoundProposedHeader(ctx, ph20))
			require.NoError(t, s.SaveRoundProposedHeader(ctx, ph21))

			phs, _, _, err = s.LoadRoundState(ctx, 2, 0)
			require.NoError(t, err)

			// Using require.ElementsMatch gives poor output on mismatched lists with huge elements.
			// Ensure the two slices are sorted the same.
			want = []tmconsensus.ProposedHeader{ph20, ph21}
			slices.SortFunc(want, func(a, b tmconsensus.ProposedHeader) int {
				return bytes.Compare(a.Header.Hash, b.Header.Hash)
			})
			slices.SortFunc(phs, func(a, b tmconsensus.ProposedHeader) int {
				return bytes.Compare(a.Header.Hash, b.Header.Hash)
			})
			require.Equal(t, want, phs)
		})

		t.Run("overwriting an existing proposed block is disallowed", func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			s, err := f(t.Cleanup)
			require.NoError(t, err)

			fx := tmconsensustest.NewEd25519Fixture(2)

			ph0 := fx.NextProposedHeader([]byte("val0"), 0)
			fx.SignProposal(ctx, &ph0, 0)
			require.Empty(t, ph0.Header.PrevCommitProof.Proofs)
			ph0.Header.PrevCommitProof.Proofs = nil

			// First write succeeds.
			require.NoError(t, s.SaveRoundProposedHeader(ctx, ph0))

			// And a second write is an error.
			// It is possible for two independent proposed blocks to have the same block hash,
			// but one proposer is not allowed to propose two different blocks in one round.
			err = s.SaveRoundProposedHeader(ctx, ph0)
			require.ErrorIs(t, err, tmstore.OverwriteError{
				Field: "pubkey",
				Value: fmt.Sprintf("%x", ph0.ProposerPubKey.PubKeyBytes()),
			})
		})
	})

	t.Run("replayed headers", func(t *testing.T) {
		t.Run("returned in LoadRoundState", func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			s, err := f(t.Cleanup)
			require.NoError(t, err)

			fx := tmconsensustest.NewEd25519Fixture(2)

			ph1 := fx.NextProposedHeader([]byte("val0"), 0)
			require.Empty(t, ph1.Header.PrevCommitProof.Proofs)
			ph1.Header.PrevCommitProof.Proofs = nil

			require.NoError(t, s.SaveRoundReplayedHeader(ctx, ph1.Header))

			// In normal flow when the mirror is handling a replayed header,
			// it saves the replayed header and then writes the precommit proof.
			// If that gets interrupted after saving the header but before writing the precommit proof,
			// there isn't really a way for us to know it's part of this particular round,
			// so we don't assert about the header until the precommit proofs are saved.
			voteMap := map[string][]int{
				string(ph1.Header.Hash): {0, 1},
			}

			precommits := fx.SparsePrecommitSignatureCollection(ctx, 1, 0, voteMap)
			require.NoError(t, s.OverwriteRoundPrecommitProofs(ctx, 1, 0, precommits))

			phs, _, _, err := s.LoadRoundState(ctx, 1, 0)
			require.NoError(t, err)
			require.Equal(t, []tmconsensus.ProposedHeader{
				{
					Header: ph1.Header,
				},
			}, phs)
		})

		t.Run("returns tmstore.OverwriteError when proposed header already exists", func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			s, err := f(t.Cleanup)
			require.NoError(t, err)

			fx := tmconsensustest.NewEd25519Fixture(2)

			ph1 := fx.NextProposedHeader([]byte("val0"), 0)
			fx.SignProposal(ctx, &ph1, 0)
			require.NoError(t, s.SaveRoundProposedHeader(ctx, ph1))

			err = s.SaveRoundReplayedHeader(ctx, ph1.Header)
			require.Error(t, err)
			require.ErrorIs(t, err, tmstore.OverwriteError{
				Field: "hash",
				Value: fmt.Sprintf("%x", ph1.Header.Hash),
			})
		})
	})

	for _, tc := range []struct {
		typ string

		proofMapFn func(f *tmconsensustest.Fixture) func(
			ctx context.Context,
			height uint64,
			round uint32,
			voteMap map[string][]int, // Map of block hash to vote, to validator indices.
		) tmconsensus.SparseSignatureCollection

		overwriteFn func(s tmstore.RoundStore) func(
			ctx context.Context,
			height uint64,
			round uint32,
			proofs tmconsensus.SparseSignatureCollection,
		) error

		choiceFn func(prevotes, precommits tmconsensus.SparseSignatureCollection) (
			want tmconsensus.SparseSignatureCollection,
		)
	}{
		{
			typ: "prevote",
			proofMapFn: func(f *tmconsensustest.Fixture) func(
				ctx context.Context,
				height uint64,
				round uint32,
				voteMap map[string][]int, // Map of block hash to prevote, to validator indices.
			) tmconsensus.SparseSignatureCollection {
				return f.SparsePrevoteSignatureCollection
			},
			overwriteFn: func(s tmstore.RoundStore) func(
				ctx context.Context,
				height uint64,
				round uint32,
				proofs tmconsensus.SparseSignatureCollection,
			) error {
				return s.OverwriteRoundPrevoteProofs
			},
			choiceFn: func(prevotes, precommits tmconsensus.SparseSignatureCollection) (
				want tmconsensus.SparseSignatureCollection,
			) {
				return prevotes
			},
		},
		{
			typ: "precommit",
			proofMapFn: func(f *tmconsensustest.Fixture) func(
				ctx context.Context,
				height uint64,
				round uint32,
				voteMap map[string][]int, // Map of block hash to precommit, to validator indices.
			) tmconsensus.SparseSignatureCollection {
				return f.SparsePrecommitSignatureCollection
			},
			overwriteFn: func(s tmstore.RoundStore) func(
				ctx context.Context,
				height uint64,
				round uint32,
				proofs tmconsensus.SparseSignatureCollection,
			) error {
				return s.OverwriteRoundPrecommitProofs
			},
			choiceFn: func(prevotes, precommits tmconsensus.SparseSignatureCollection) (
				want tmconsensus.SparseSignatureCollection,
			) {
				return precommits
			},
		},
	} {
		tc := tc
		t.Run(tc.typ+" proofs", func(t *testing.T) {
			t.Run("happy path", func(t *testing.T) {
				t.Parallel()

				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()

				s, err := f(t.Cleanup)
				require.NoError(t, err)

				fx := tmconsensustest.NewEd25519Fixture(2)
				attemptToSavePubKeys(t, ctx, s, fx.Vals())

				ph := fx.NextProposedHeader([]byte("app_data"), 0)

				// First validator votes for proposed block,
				// other validator votes for nil.
				voteMap := map[string][]int{
					string(ph.Header.Hash): {0},
					"":                     {1},
				}
				proofMap := tc.proofMapFn(fx)(ctx, 1, 0, voteMap)

				err = tc.overwriteFn(s)(ctx, 1, 0, proofMap)
				require.NoError(t, err)

				_, prevotes, precommits, err := s.LoadRoundState(ctx, 1, 0)
				require.NoError(t, err)
				want := tc.choiceFn(prevotes, precommits)
				require.Equal(t, proofMap, want)
			})
		})
	}

	t.Run("full set of round values", func(t *testing.T) {
		t.Run("all present when all set", func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			s, err := f(t.Cleanup)
			require.NoError(t, err)

			fx := tmconsensustest.NewEd25519Fixture(2)
			attemptToSavePubKeys(t, ctx, s, fx.Vals())

			ph := fx.NextProposedHeader([]byte("app_data"), 0)
			fx.SignProposal(ctx, &ph, 0)
			require.Empty(t, ph.Header.PrevCommitProof.Proofs)
			ph.Header.PrevCommitProof.Proofs = nil
			require.NoError(t, s.SaveRoundProposedHeader(ctx, ph))

			// First validator votes for proposed block,
			// other validator votes for nil.
			voteMap := map[string][]int{
				string(ph.Header.Hash): {0},
				"":                     {1},
			}
			prevoteSigs := fx.SparsePrevoteSignatureCollection(ctx, 1, 0, voteMap)
			require.NoError(t, s.OverwriteRoundPrevoteProofs(ctx, 1, 0, prevoteSigs))

			precommitSigs := fx.SparsePrecommitSignatureCollection(ctx, 1, 0, voteMap)
			require.NoError(t, s.OverwriteRoundPrecommitProofs(ctx, 1, 0, precommitSigs))

			phs, prevotes, precommits, err := s.LoadRoundState(ctx, 1, 0)
			require.NoError(t, err)
			require.Equal(t, []tmconsensus.ProposedHeader{ph}, phs)
			require.Equal(t, prevoteSigs, prevotes)
			require.Equal(t, precommitSigs, precommits)
		})

		t.Run("only proposed blocks set", func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			s, err := f(t.Cleanup)
			require.NoError(t, err)

			fx := tmconsensustest.NewEd25519Fixture(2)
			attemptToSavePubKeys(t, ctx, s, fx.Vals())

			ph := fx.NextProposedHeader([]byte("app_data"), 0)
			fx.SignProposal(ctx, &ph, 0)
			require.Empty(t, ph.Header.PrevCommitProof.Proofs)
			ph.Header.PrevCommitProof.Proofs = nil
			require.NoError(t, s.SaveRoundProposedHeader(ctx, ph))

			pbs, prevotes, precommits, err := s.LoadRoundState(ctx, 1, 0)
			require.NoError(t, err)
			require.Equal(t, []tmconsensus.ProposedHeader{ph}, pbs)
			require.Nil(t, prevotes.PubKeyHash)
			require.Nil(t, prevotes.BlockSignatures)
			require.Nil(t, precommits.PubKeyHash)
			require.Nil(t, precommits.BlockSignatures)
		})

		t.Run("only prevotes set", func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			s, err := f(t.Cleanup)
			require.NoError(t, err)

			fx := tmconsensustest.NewEd25519Fixture(2)
			attemptToSavePubKeys(t, ctx, s, fx.Vals())

			ph := fx.NextProposedHeader([]byte("app_data"), 0)
			fx.SignProposal(ctx, &ph, 0)
			require.Empty(t, ph.Header.PrevCommitProof.Proofs)
			ph.Header.PrevCommitProof.Proofs = nil

			// First validator votes for proposed block,
			// other validator votes for nil.
			voteMap := map[string][]int{
				string(ph.Header.Hash): {0},
				"":                     {1},
			}
			prevoteSigs := fx.SparsePrevoteSignatureCollection(ctx, 1, 0, voteMap)
			require.NoError(t, s.OverwriteRoundPrevoteProofs(ctx, 1, 0, prevoteSigs))

			pbs, prevotes, precommits, err := s.LoadRoundState(ctx, 1, 0)
			require.NoError(t, err)
			require.Nil(t, pbs)
			require.Equal(t, prevoteSigs, prevotes)
			require.Nil(t, precommits.PubKeyHash)
			require.Nil(t, precommits.BlockSignatures)
		})

		t.Run("only precommits set", func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			s, err := f(t.Cleanup)
			require.NoError(t, err)

			fx := tmconsensustest.NewEd25519Fixture(2)
			attemptToSavePubKeys(t, ctx, s, fx.Vals())

			ph := fx.NextProposedHeader([]byte("app_data"), 0)
			fx.SignProposal(ctx, &ph, 0)
			require.Empty(t, ph.Header.PrevCommitProof.Proofs)
			ph.Header.PrevCommitProof.Proofs = nil

			// First validator votes for proposed block,
			// other validator votes for nil.
			voteMap := map[string][]int{
				string(ph.Header.Hash): {0},
				"":                     {1},
			}

			precommitSigs := fx.SparsePrecommitSignatureCollection(ctx, 1, 0, voteMap)
			require.NoError(t, s.OverwriteRoundPrecommitProofs(ctx, 1, 0, precommitSigs))

			pbs, prevotes, precommits, err := s.LoadRoundState(ctx, 1, 0)
			require.NoError(t, err)
			require.Nil(t, pbs)
			require.Nil(t, prevotes.PubKeyHash)
			require.Nil(t, prevotes.BlockSignatures)
			require.Equal(t, precommitSigs, precommits)
		})
	})
}
