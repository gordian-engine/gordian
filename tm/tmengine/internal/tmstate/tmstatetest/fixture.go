package tmstatetest

import (
	"context"
	"log/slog"
	"testing"

	"github.com/gordian-engine/gordian/gassert/gasserttest"
	"github.com/gordian-engine/gordian/gcrypto"
	"github.com/gordian-engine/gordian/gwatchdog"
	"github.com/gordian-engine/gordian/internal/gtest"
	"github.com/gordian-engine/gordian/tm/tmconsensus"
	"github.com/gordian-engine/gordian/tm/tmconsensus/tmconsensustest"
	"github.com/gordian-engine/gordian/tm/tmdriver"
	"github.com/gordian-engine/gordian/tm/tmengine/internal/tmeil"
	"github.com/gordian-engine/gordian/tm/tmengine/internal/tmstate"
	"github.com/gordian-engine/gordian/tm/tmengine/tmelink"
	"github.com/gordian-engine/gordian/tm/tmstore/tmmemstore"
)

// Fixture is a helper type to create a [tmstate.StateMachine] and its required inputs
// for tests involving a StateMachine.
type Fixture struct {
	Log *slog.Logger

	Fx *tmconsensustest.Fixture

	// Exposed on the fixture explicitly as the MockConsensusStrategy type.
	CStrat *tmconsensustest.MockConsensusStrategy

	RoundTimer *MockRoundTimer

	RoundViewInCh         chan tmeil.StateMachineRoundView
	RoundEntranceOutCh    chan tmeil.StateMachineRoundEntrance
	FinalizeBlockRequests chan tmdriver.FinalizeBlockRequest
	BlockDataArrivalCh    chan tmelink.BlockDataArrival

	Cfg tmstate.StateMachineConfig

	WatchdogCtx context.Context
}

func NewFixture(ctx context.Context, t *testing.T, nVals int) *Fixture {
	fx := tmconsensustest.NewEd25519Fixture(nVals)

	cStrat := tmconsensustest.NewMockConsensusStrategy()

	rt := new(MockRoundTimer)

	roundViewInCh := make(chan tmeil.StateMachineRoundView)
	roundEntranceOutCh := make(chan tmeil.StateMachineRoundEntrance)
	finReq := make(chan tmdriver.FinalizeBlockRequest)

	// Normally this channel would be buffered,
	// but for test we prefer a synchronous channel.
	blockDataArrivalCh := make(chan tmelink.BlockDataArrival)

	log := gtest.NewLogger(t)
	wd, wCtx := gwatchdog.NewNopWatchdog(ctx, log.With("sys", "watchdog"))

	// Ensure the watchdog doesn't log after test completion.
	// There ought to be a defer cancel before the call to NewFixture anyway.
	t.Cleanup(wd.Wait)

	return &Fixture{
		Log: log,

		Fx: fx,

		CStrat: cStrat,

		RoundTimer: rt,

		RoundViewInCh:         roundViewInCh,
		RoundEntranceOutCh:    roundEntranceOutCh,
		FinalizeBlockRequests: finReq,
		BlockDataArrivalCh:    blockDataArrivalCh,

		Cfg: tmstate.StateMachineConfig{
			// Default to the first signer.
			// Caller can set to nil or a different signer if desired.
			Signer: tmconsensus.PassthroughSigner{
				Signer:          fx.PrivVals[0].Signer,
				SignatureScheme: fx.SignatureScheme,
			},

			HashScheme:                        fx.HashScheme,
			SignatureScheme:                   fx.SignatureScheme,
			CommonMessageSignatureProofScheme: fx.CommonMessageSignatureProofScheme,

			Genesis: fx.DefaultGenesis(),

			ActionStore:       tmmemstore.NewActionStore(),
			FinalizationStore: tmmemstore.NewFinalizationStore(),
			StateMachineStore: tmmemstore.NewStateMachineStore(),

			RoundTimer: rt,

			ConsensusStrategy: cStrat,

			RoundViewInCh:          roundViewInCh,
			RoundEntranceOutCh:     roundEntranceOutCh,
			FinalizeBlockRequestCh: finReq,
			BlockDataArrivalCh:     blockDataArrivalCh,

			Watchdog: wd,

			AssertEnv: gasserttest.DefaultEnv(),
		},

		WatchdogCtx: wCtx,
	}
}

func (f *Fixture) NewStateMachine() *tmstate.StateMachine {
	sm, err := tmstate.NewStateMachine(f.WatchdogCtx, f.Log, f.Cfg)
	if err != nil {
		panic(err)
	}
	return sm
}

func (f *Fixture) EmptyVRV(h uint64, r uint32) tmconsensus.VersionedRoundView {
	valSet := f.Fx.ValSet()
	vs := tmconsensus.NewVoteSummary()
	vs.SetAvailablePower(valSet.Validators)
	return tmconsensus.VersionedRoundView{
		RoundView: tmconsensus.RoundView{
			Height:       h,
			Round:        r,
			ValidatorSet: valSet,

			PrevCommitProof: tmconsensus.CommitProof{
				// Everything assumes an initial empty VRV has a non-nil Proofs.
				Proofs: map[string][]gcrypto.SparseSignature{},
			},

			VoteSummary: vs,
		},
	}
}
