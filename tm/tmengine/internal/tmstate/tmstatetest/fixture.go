package tmstatetest

import (
	"context"
	"log/slog"
	"testing"

	"github.com/rollchains/gordian/gwatchdog"
	"github.com/rollchains/gordian/internal/gtest"
	"github.com/rollchains/gordian/tm/tmconsensus"
	"github.com/rollchains/gordian/tm/tmconsensus/tmconsensustest"
	"github.com/rollchains/gordian/tm/tmdriver"
	"github.com/rollchains/gordian/tm/tmengine/internal/tmeil"
	"github.com/rollchains/gordian/tm/tmengine/internal/tmstate"
	"github.com/rollchains/gordian/tm/tmengine/tmelink"
	"github.com/rollchains/gordian/tm/tmstore/tmmemstore"
)

// Fixture is a helper type to create a [tmstate.StateMachine] and its required inputs
// for tests involving a StateMachine.
type Fixture struct {
	Log *slog.Logger

	Fx *tmconsensustest.StandardFixture

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
	fx := tmconsensustest.NewStandardFixture(nVals)

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
			Signer: fx.PrivVals[0].Signer,

			SignatureScheme: fx.SignatureScheme,

			HashScheme: fx.HashScheme,

			Genesis: fx.DefaultGenesis(),

			ActionStore:       tmmemstore.NewActionStore(),
			FinalizationStore: tmmemstore.NewFinalizationStore(),

			RoundTimer: rt,

			ConsensusStrategy: cStrat,

			RoundViewInCh:          roundViewInCh,
			RoundEntranceOutCh:     roundEntranceOutCh,
			FinalizeBlockRequestCh: finReq,
			BlockDataArrivalCh:     blockDataArrivalCh,

			Watchdog: wd,
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
	vals := f.Fx.Vals()
	vs := tmconsensus.NewVoteSummary()
	vs.SetAvailablePower(vals)
	keyHash, powHash := f.Fx.ValidatorHashes()
	return tmconsensus.VersionedRoundView{
		RoundView: tmconsensus.RoundView{
			Height:                 h,
			Round:                  r,
			Validators:             vals,
			ValidatorPubKeyHash:    keyHash,
			ValidatorVotePowerHash: powHash,

			VoteSummary: vs,
		},
	}
}
