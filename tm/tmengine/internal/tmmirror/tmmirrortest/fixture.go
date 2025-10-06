package tmmirrortest

import (
	"context"
	"fmt"
	"log/slog"
	"testing"

	"github.com/gordian-engine/gordian/gassert/gasserttest"
	"github.com/gordian-engine/gordian/gcrypto"
	"github.com/gordian-engine/gordian/gwatchdog"
	"github.com/gordian-engine/gordian/internal/gtest"
	"github.com/gordian-engine/gordian/tm/tmconsensus"
	"github.com/gordian-engine/gordian/tm/tmconsensus/tmconsensustest"
	"github.com/gordian-engine/gordian/tm/tmengine/internal/tmeil"
	"github.com/gordian-engine/gordian/tm/tmengine/internal/tmemetrics"
	"github.com/gordian-engine/gordian/tm/tmengine/internal/tmmirror"
	"github.com/gordian-engine/gordian/tm/tmengine/tmelink"
	"github.com/gordian-engine/gordian/tm/tmengine/tmelink/tmelinktest"
	"github.com/gordian-engine/gordian/tm/tmstore"
	"github.com/gordian-engine/gordian/tm/tmstore/tmmemstore"
)

// Fixture is a helper type to create a [tmmirror.Mirror] and its required inputs
// for tests involving a Mirror.
type Fixture struct {
	Log *slog.Logger

	Fx *tmconsensustest.Fixture

	// These channels are bidirectional in the fixture,
	// because they are write-only in the config.
	StateMachineRoundViewOut chan tmeil.StateMachineRoundView

	GossipStrategyOut chan tmelink.NetworkViewUpdate
	LagStateOut       chan tmelink.LagState

	StateMachineRoundEntranceIn chan tmeil.StateMachineRoundEntrance
	ReplayedHeadersIn           chan tmelink.ReplayedHeaderRequest

	Cfg tmmirror.MirrorConfig

	WatchdogCtx context.Context
}

func NewFixture(ctx context.Context, t *testing.T, nVals int) *Fixture {
	fx := tmconsensustest.NewEd25519Fixture(nVals)
	gso := make(chan tmelink.NetworkViewUpdate)
	lso := make(chan tmelink.LagState)
	smIn := make(chan tmeil.StateMachineRoundEntrance, 1)
	smViewOut := make(chan tmeil.StateMachineRoundView) // Unbuffered.

	rhrIn := make(chan tmelink.ReplayedHeaderRequest)

	log := gtest.NewLogger(t)
	wd, wCtx := gwatchdog.NewNopWatchdog(ctx, log.With("sys", "watchdog"))

	// Ensure the watchdog doesn't log after test completion.
	// There ought to be a defer cancel before the call to NewFixture anyway.
	t.Cleanup(wd.Wait)

	return &Fixture{
		Log: log,

		Fx: fx,

		StateMachineRoundViewOut: smViewOut,

		GossipStrategyOut: gso,
		LagStateOut:       lso,

		StateMachineRoundEntranceIn: smIn,
		ReplayedHeadersIn:           rhrIn,

		Cfg: tmmirror.MirrorConfig{
			Store:                tmmemstore.NewMirrorStore(),
			CommittedHeaderStore: tmmemstore.NewCommittedHeaderStore(),
			RoundStore:           tmmemstore.NewRoundStore(),
			ValidatorStore:       tmmemstore.NewValidatorStore(fx.HashScheme),

			InitialHeight:       1,
			InitialValidatorSet: fx.ValSet(),

			HashScheme:                        fx.HashScheme,
			SignatureScheme:                   fx.SignatureScheme,
			CommonMessageSignatureProofScheme: fx.CommonMessageSignatureProofScheme,

			// Default the fetcher to a pair of blocking channels.
			// The caller can override f.Cfg.ProposedBlockFetcher
			// in tests that need control over, or inspection of, these channels.
			ProposedHeaderFetcher: tmelinktest.NewPHFetcher(0, 0).ProposedHeaderFetcher(),

			GossipStrategyOut: gso,
			LagStateOut:       lso,

			StateMachineRoundViewOut: smViewOut,

			StateMachineRoundEntranceIn: smIn,
			ReplayedHeadersIn:           rhrIn,

			Watchdog: wd,

			AssertEnv: gasserttest.DefaultEnv(),
		},

		WatchdogCtx: wCtx,
	}
}

func (f *Fixture) NewMirror() *tmmirror.Mirror {
	m, err := tmmirror.NewMirror(f.WatchdogCtx, f.Log, f.Cfg)
	if err != nil {
		panic(err)
	}
	return m
}

func (f *Fixture) Store() *tmmemstore.MirrorStore {
	return f.Cfg.Store.(*tmmemstore.MirrorStore)
}

func (f *Fixture) ValidatorStore() tmstore.ValidatorStore {
	return f.Cfg.ValidatorStore
}

func (f *Fixture) UseMetrics(t *testing.T, ctx context.Context) <-chan tmemetrics.Metrics {
	if f.Cfg.MetricsCollector != nil {
		panic("UseMetrics called when f.Cfg.MetricsCollector was not nil")
	}

	ch := make(chan tmemetrics.Metrics)
	mc := tmemetrics.NewCollector(ctx, 4, ch)
	f.Cfg.MetricsCollector = mc

	// The one tricky part: the collector will not report any metrics
	// before both the state machine and the mirror have reported once.
	// So, since this is a mirror fixture and we presumably will not
	// have any state machine involvement,
	// just report a zero state machine metric.
	mc.UpdateStateMachine(tmemetrics.StateMachineMetrics{})

	t.Cleanup(mc.Wait)
	return ch
}

// CommitInitialHeight updates the round store, the network store,
// and the consensus fixture to have a commit at the initial height at round zero.
//
// If the mirror is started after this call,
// / it is as though the mirror handled the expected sequence of messages
// to advance past the initial height and round.
func (f *Fixture) CommitInitialHeight(
	ctx context.Context,
	initialAppStateHash []byte,
	initialProposerIndex int,
	committerIdxs []int,
) {
	// First, store the proposed block.
	// Sign it so it is valid.
	pb := f.Fx.NextProposedHeader(initialAppStateHash, initialProposerIndex)
	f.Fx.SignProposal(ctx, &pb, initialProposerIndex)
	if err := f.Cfg.RoundStore.SaveRoundProposedHeader(ctx, pb); err != nil {
		panic(fmt.Errorf("failed to save proposed block: %w", err))
	}

	// Now build the precommit for that round.
	voteMap := map[string][]int{
		string(pb.Header.Hash): committerIdxs,
	}
	fullPrecommitProofs := f.Fx.PrecommitProofMap(ctx, f.Cfg.InitialHeight, 0, voteMap)
	sparsePrecommits := f.Fx.SparsePrecommitSignatureCollection(ctx, f.Cfg.InitialHeight, 0, voteMap)

	if err := f.Cfg.RoundStore.OverwriteRoundPrecommitProofs(ctx, f.Cfg.InitialHeight, 0, sparsePrecommits); err != nil {
		panic(fmt.Errorf("failed to overwrite precommit proofs: %w", err))
	}

	// The kernel saves committing blocks to the header store,
	// so do that here too.
	if err := f.Cfg.CommittedHeaderStore.SaveCommittedHeader(ctx, tmconsensus.CommittedHeader{
		Header: pb.Header,
		Proof: tmconsensus.CommitProof{
			Round:      0,
			PubKeyHash: string(pb.Header.ValidatorSet.PubKeyHash),
			Proofs:     f.Fx.SparsePrecommitProofMap(ctx, f.Cfg.InitialHeight, 0, voteMap),
		},
	}); err != nil {
		panic(fmt.Errorf("failed to save header: %w", err))
	}

	// And mark the mirror store's updated height/round.
	if err := f.Cfg.Store.SetNetworkHeightRound(tmmirror.NetworkHeightRound{
		CommittingHeight: f.Cfg.InitialHeight,
		CommittingRound:  0,

		VotingHeight: f.Cfg.InitialHeight + 1,
		VotingRound:  0,
	}.ForStore(ctx)); err != nil {
		panic(fmt.Errorf("failed to store network height/round: %w", err))
	}

	// Finally, update the fixture to reflect the committed block.
	f.Fx.CommitBlock(pb.Header, []byte("app_state_height_1"), 0, fullPrecommitProofs)
}

// Prevoter returns a [Voter] for prevotes.
func (f *Fixture) Prevoter(m *tmmirror.Mirror) Voter {
	keyHash, _ := f.Fx.ValidatorHashes()
	return prevoteVoter{mfx: f, m: m, keyHash: keyHash}
}

// Precommitter returns a [Voter] for precommits.
func (f *Fixture) Precommitter(m *tmmirror.Mirror) Voter {
	keyHash, _ := f.Fx.ValidatorHashes()
	return precommitVoter{mfx: f, m: m, keyHash: keyHash}
}

// Voter is the interface returned from [*Fixture.Prevoter] and [*Fixture.Precommitter]
// to offer a consistent interface to handle prevote and precommit proofs, respectively.
//
// This simplifies sets of mirror tests where the only difference
// is whether we are applying prevotes or precommits.
type Voter interface {
	HandleProofs(
		ctx context.Context,
		height uint64, round uint32,
		votes map[string][]int,
	) tmconsensus.HandleVoteProofsResult

	ProofsFromView(tmconsensus.RoundView) map[string]gcrypto.CommonMessageSignatureProof
	FullProofsFromRoundStateMaps(
		height uint64, round uint32,
		valSet tmconsensus.ValidatorSet,
		prevotes, precommits tmconsensus.SparseSignatureCollection,
	) map[string]gcrypto.CommonMessageSignatureProof
}

type prevoteVoter struct {
	mfx     *Fixture
	m       *tmmirror.Mirror
	keyHash string
}

func (v prevoteVoter) HandleProofs(
	ctx context.Context,
	height uint64, round uint32,
	votes map[string][]int,
) tmconsensus.HandleVoteProofsResult {
	return v.m.HandlePrevoteProofs(
		ctx, tmconsensus.PrevoteSparseProof{
			Height: height, Round: round,

			PubKeyHash: v.keyHash,

			Proofs: v.mfx.Fx.SparsePrevoteProofMap(ctx, height, round, votes),
		})
}

func (v prevoteVoter) ProofsFromView(rv tmconsensus.RoundView) map[string]gcrypto.CommonMessageSignatureProof {
	return rv.PrevoteProofs
}
func (v prevoteVoter) FullProofsFromRoundStateMaps(
	height uint64,
	round uint32,
	valSet tmconsensus.ValidatorSet,
	prevotes, _ tmconsensus.SparseSignatureCollection,
) map[string]gcrypto.CommonMessageSignatureProof {
	out, err := prevotes.ToFullPrevoteProofMap(
		height, round,
		valSet.PubKeys,
		v.mfx.Fx.SignatureScheme,
		v.mfx.Fx.CommonMessageSignatureProofScheme,
	)
	if err != nil {
		panic(err)
	}
	return out
}

type precommitVoter struct {
	mfx     *Fixture
	m       *tmmirror.Mirror
	keyHash string
}

func (v precommitVoter) HandleProofs(
	ctx context.Context,
	height uint64, round uint32,
	votes map[string][]int,
) tmconsensus.HandleVoteProofsResult {
	return v.m.HandlePrecommitProofs(
		ctx, tmconsensus.PrecommitSparseProof{
			Height: height, Round: round,

			PubKeyHash: v.keyHash,

			Proofs: v.mfx.Fx.SparsePrecommitProofMap(ctx, height, round, votes),
		})
}

func (v precommitVoter) ProofsFromView(rv tmconsensus.RoundView) map[string]gcrypto.CommonMessageSignatureProof {
	return rv.PrecommitProofs
}

func (v precommitVoter) FullProofsFromRoundStateMaps(
	height uint64,
	round uint32,
	valSet tmconsensus.ValidatorSet,
	_, precommits tmconsensus.SparseSignatureCollection,
) map[string]gcrypto.CommonMessageSignatureProof {
	out, err := precommits.ToFullPrecommitProofMap(
		height, round,
		valSet.PubKeys,
		v.mfx.Fx.SignatureScheme,
		v.mfx.Fx.CommonMessageSignatureProofScheme,
	)
	if err != nil {
		panic(err)
	}
	return out
}
