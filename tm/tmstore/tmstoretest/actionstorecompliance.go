package tmstoretest

import (
	"context"
	"testing"

	"github.com/gordian-engine/gordian/tm/tmconsensus"
	"github.com/gordian-engine/gordian/tm/tmstore"
	"github.com/stretchr/testify/require"
)

type ActionStoreFactory func(cleanup func(func())) (tmstore.ActionStore, error)

func TestActionStoreCompliance(t *testing.T, f ActionStoreFactory, fxf FixtureFactory) {
	t.Run("proposed headers", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		s, err := f(t.Cleanup)
		require.NoError(t, err)

		fx := fxf(2)
		ph1 := fx.NextProposedHeader([]byte("app_data_1"), 0)
		ph1.Round = 2 // Arbitrary, test nonzero round.

		// The returned data should be nil anyway.
		ph1.Header.PrevCommitProof.Proofs = nil

		fx.RecalculateHash(&ph1.Header)
		fx.SignProposal(ctx, &ph1, 0)

		attemptToSavePubKeys(t, ctx, s, ph1.Header.ValidatorSet.Validators)

		require.NoError(t, s.SaveProposedHeaderAction(ctx, ph1))

		t.Run("round trip", func(t *testing.T) {
			ra, err := s.LoadActions(ctx, 1, 2)
			require.NoError(t, err)

			require.Equal(t, uint64(1), ra.Height)
			require.Equal(t, uint32(2), ra.Round)
			require.Equal(t, ra.ProposedHeader, ph1)
		})

		t.Run("double save rejected with same input", func(t *testing.T) {
			err := s.SaveProposedHeaderAction(ctx, ph1)
			require.ErrorIs(t, err, tmstore.DoubleActionError{Type: "proposed block"})
		})

		t.Run("double save rejected with different input", func(t *testing.T) {
			otherPH := fx.NextProposedHeader([]byte("different_app_data"), 0)

			otherPH.Round = 2
			fx.RecalculateHash(&otherPH.Header)
			fx.SignProposal(ctx, &otherPH, 0)

			err := s.SaveProposedHeaderAction(ctx, otherPH)
			require.ErrorIs(t, err, tmstore.DoubleActionError{Type: "proposed block"})
		})
	})

	t.Run("prevotes", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		s, err := f(t.Cleanup)
		require.NoError(t, err)

		fx := fxf(2)
		ph1 := fx.NextProposedHeader([]byte("app_data_1"), 0)
		ph1.Round = 2

		// The returned data should be nil anyway.
		ph1.Header.PrevCommitProof.Proofs = nil

		fx.RecalculateHash(&ph1.Header)
		fx.SignProposal(ctx, &ph1, 0)

		vt := tmconsensus.VoteTarget{
			Height:    1,
			Round:     2,
			BlockHash: string(ph1.Header.Hash),
		}
		sig := fx.PrevoteSignature(ctx, vt, 0)

		attemptToSavePubKeys(t, ctx, s, ph1.Header.ValidatorSet.Validators)

		pubKey := fx.ValidatorPubKey(0)
		require.NoError(t, s.SavePrevoteAction(ctx, pubKey, vt, sig))

		t.Run("round trip", func(t *testing.T) {
			ra, err := s.LoadActions(ctx, 1, 2)
			require.NoError(t, err)

			require.Equal(t, uint64(1), ra.Height)
			require.Equal(t, uint32(2), ra.Round)

			require.True(t, pubKey.Equal(ra.PubKey))
			require.Equal(t, vt.BlockHash, ra.PrevoteTarget)
			require.Equal(t, string(sig), ra.PrevoteSignature)
		})

		t.Run("double save rejected with same signature", func(t *testing.T) {
			err := s.SavePrevoteAction(ctx, pubKey, vt, sig)
			require.ErrorIs(t, err, tmstore.DoubleActionError{Type: "prevote"})
		})

		t.Run("double save rejected with different signature", func(t *testing.T) {
			nilVT := tmconsensus.VoteTarget{Height: 1, Round: 2}
			nilSig := fx.PrevoteSignature(ctx, nilVT, 0)
			err := s.SavePrevoteAction(ctx, pubKey, nilVT, nilSig)
			require.ErrorIs(t, err, tmstore.DoubleActionError{Type: "prevote"})
		})
	})

	t.Run("precommits", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		s, err := f(t.Cleanup)
		require.NoError(t, err)

		fx := fxf(2)
		ph1 := fx.NextProposedHeader([]byte("app_data_1"), 0)
		ph1.Round = 2

		fx.RecalculateHash(&ph1.Header)
		fx.SignProposal(ctx, &ph1, 0)

		vt := tmconsensus.VoteTarget{
			Height:    1,
			Round:     2,
			BlockHash: string(ph1.Header.Hash),
		}
		sig := fx.PrecommitSignature(ctx, vt, 0)

		attemptToSavePubKeys(t, ctx, s, ph1.Header.ValidatorSet.Validators)

		pubKey := fx.ValidatorPubKey(0)
		require.NoError(t, s.SavePrecommitAction(ctx, pubKey, vt, sig))

		t.Run("round trip", func(t *testing.T) {
			ra, err := s.LoadActions(ctx, 1, 2)
			require.NoError(t, err)

			require.Equal(t, uint64(1), ra.Height)
			require.Equal(t, uint32(2), ra.Round)

			require.True(t, pubKey.Equal(ra.PubKey))
			require.Equal(t, vt.BlockHash, ra.PrecommitTarget)
			require.Equal(t, string(sig), ra.PrecommitSignature)
		})

		t.Run("double save rejected with same signature", func(t *testing.T) {
			err := s.SavePrecommitAction(ctx, pubKey, vt, sig)
			require.ErrorIs(t, err, tmstore.DoubleActionError{Type: "precommit"})
		})

		t.Run("double save rejected with different signature", func(t *testing.T) {
			nilVT := tmconsensus.VoteTarget{Height: 1, Round: 2}
			nilSig := fx.PrecommitSignature(ctx, nilVT, 0)
			err := s.SavePrecommitAction(ctx, pubKey, nilVT, nilSig)
			require.ErrorIs(t, err, tmstore.DoubleActionError{Type: "precommit"})
		})
	})

	t.Run("pub key change", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		fx := fxf(2)
		ph1 := fx.NextProposedHeader([]byte("app_data_1"), 0)
		ph1.Round = 2

		fx.RecalculateHash(&ph1.Header)
		fx.SignProposal(ctx, &ph1, 0)

		vt := tmconsensus.VoteTarget{
			Height:    1,
			Round:     2,
			BlockHash: string(ph1.Header.Hash),
		}
		prevoteSig := fx.PrevoteSignature(ctx, vt, 0)
		prevotePubKey := fx.ValidatorPubKey(0)

		precommitSig := fx.PrecommitSignature(ctx, vt, 1)
		precommitPubKey := fx.ValidatorPubKey(1)

		t.Run("rejected when adding precommit", func(t *testing.T) {
			s, err := f(t.Cleanup)
			require.NoError(t, err)

			attemptToSavePubKeys(t, ctx, s, ph1.Header.ValidatorSet.Validators)

			require.NoError(t, s.SavePrevoteAction(ctx, prevotePubKey, vt, prevoteSig))
			err = s.SavePrecommitAction(ctx, precommitPubKey, vt, precommitSig)
			require.ErrorIs(t, err, tmstore.PubKeyChangedError{
				ActionType: "precommit",
				Want:       string(prevotePubKey.PubKeyBytes()),
				Got:        string(precommitPubKey.PubKeyBytes()),
			})
		})

		t.Run("rejected when adding prevote", func(t *testing.T) {
			// Unclear if there is any use case for recording precommit before prevote,
			// but we can still test it anyway.

			s, err := f(t.Cleanup)
			require.NoError(t, err)

			attemptToSavePubKeys(t, ctx, s, ph1.Header.ValidatorSet.Validators)

			require.NoError(t, s.SavePrecommitAction(ctx, precommitPubKey, vt, precommitSig))
			err = s.SavePrevoteAction(ctx, prevotePubKey, vt, prevoteSig)
			require.ErrorIs(t, err, tmstore.PubKeyChangedError{
				ActionType: "prevote",
				Want:       string(precommitPubKey.PubKeyBytes()),
				Got:        string(prevotePubKey.PubKeyBytes()),
			})
		})
	})

	t.Run("LoadActions", func(t *testing.T) {
		t.Run("happy path", func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			s, err := f(t.Cleanup)
			require.NoError(t, err)

			fx := fxf(2)
			ph1 := fx.NextProposedHeader([]byte("app_data_1"), 0)
			ph1.Round = 2 // Arbitrary, test nonzero round.

			// The returned data should be nil anyway.
			ph1.Header.PrevCommitProof.Proofs = nil

			fx.RecalculateHash(&ph1.Header)
			fx.SignProposal(ctx, &ph1, 0)

			attemptToSavePubKeys(t, ctx, s, ph1.Header.ValidatorSet.Validators)

			require.NoError(t, s.SaveProposedHeaderAction(ctx, ph1))

			vt := tmconsensus.VoteTarget{
				Height:    1,
				Round:     2,
				BlockHash: string(ph1.Header.Hash),
			}

			pubKey := fx.ValidatorPubKey(0)
			prevoteSig := fx.PrevoteSignature(ctx, vt, 0)
			precommitSig := fx.PrecommitSignature(ctx, vt, 0)

			require.NoError(t, s.SavePrevoteAction(ctx, pubKey, vt, prevoteSig))
			require.NoError(t, s.SavePrecommitAction(ctx, pubKey, vt, precommitSig))

			ra, err := s.LoadActions(ctx, 1, 2)
			require.NoError(t, err)

			require.Equal(t, uint64(1), ra.Height)
			require.Equal(t, uint32(2), ra.Round)
			require.Equal(t, ra.ProposedHeader, ph1)
			require.True(t, pubKey.Equal(ra.PubKey))

			require.Equal(t, vt.BlockHash, ra.PrevoteTarget)
			require.Equal(t, string(prevoteSig), ra.PrevoteSignature)

			require.Equal(t, vt.BlockHash, ra.PrecommitTarget)
			require.Equal(t, string(precommitSig), ra.PrecommitSignature)
		})

		// TODO: tests for missing just one out of three actions.

		t.Run("returns RoundUnknownError on invalid round", func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			s, err := f(t.Cleanup)
			require.NoError(t, err)

			_, err = s.LoadActions(ctx, 1, 2)
			require.ErrorIs(t, err, tmconsensus.RoundUnknownError{
				WantHeight: 1,
				WantRound:  2,
			})
		})
	})
}

// attemptToSavePubKeys saves the validators' public keys to the given store,
// if the store satisfies ValidatorStore.
// Some store implementations may assume that the validators have already been saved.
func attemptToSavePubKeys(t *testing.T, ctx context.Context, s any, vals []tmconsensus.Validator) {
	t.Helper()

	if vs, ok := s.(tmstore.ValidatorStore); ok {
		pubKeys := tmconsensus.ValidatorsToPubKeys(vals)
		_, err := vs.SavePubKeys(ctx, pubKeys)
		require.NoError(t, err)
	}
}
