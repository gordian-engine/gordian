package tmi_test

import (
	"context"
	"log/slog"
	"testing"

	"github.com/rollchains/gordian/gwatchdog"
	"github.com/rollchains/gordian/internal/gtest"
	"github.com/rollchains/gordian/tm/tmconsensus"
	"github.com/rollchains/gordian/tm/tmconsensus/tmconsensustest"
	"github.com/rollchains/gordian/tm/tmengine/internal/tmeil"
	"github.com/rollchains/gordian/tm/tmengine/internal/tmmirror/internal/tmi"
	"github.com/rollchains/gordian/tm/tmengine/tmelink"
	"github.com/rollchains/gordian/tm/tmstore/tmmemstore"
)

// KernelFixture is a fixture to simplify Kernel construction.
//
// This type may move to a tmitest package if scope grows enough.
// But for now it is just an exported declaration in the _test package.
type KernelFixture struct {
	Log *slog.Logger

	Fx *tmconsensustest.StandardFixture

	// These channels are bidirectional in the fixture,
	// because they are write-only in the config.

	GossipStrategyOut chan tmelink.NetworkViewUpdate

	NHRRequests        chan chan tmi.NetworkHeightRound
	SnapshotRequests   chan tmi.SnapshotRequest
	ViewLookupRequests chan tmi.ViewLookupRequest

	AddPBRequests        chan tmconsensus.ProposedBlock
	AddPrevoteRequests   chan tmi.AddPrevoteRequest
	AddPrecommitRequests chan tmi.AddPrecommitRequest

	StateMachineRoundEntranceIn chan tmeil.StateMachineRoundEntrance
	StateMachineRoundViewOut    chan tmeil.StateMachineRoundView

	Cfg tmi.KernelConfig

	WatchdogCtx context.Context
}

func NewKernelFixture(ctx context.Context, t *testing.T, nVals int) *KernelFixture {
	fx := tmconsensustest.NewStandardFixture(nVals)

	// Unbuffered because the kernel needs to know exactly what was received.
	gso := make(chan tmelink.NetworkViewUpdate)

	// 1-buffered like production:
	// "because it is possible that the caller
	// may initiate the request and do work before reading the response."
	nhrRequests := make(chan chan tmi.NetworkHeightRound, 1)
	snapshotRequests := make(chan tmi.SnapshotRequest, 1)
	viewLookupRequests := make(chan tmi.ViewLookupRequest, 1)

	// "Arbitrarily sized to allow some concurrent requests,
	// with low likelihood of blocking."
	addPBRequests := make(chan tmconsensus.ProposedBlock, 8)

	// "The calling method blocks on the response regardless,
	// so no point in buffering these."
	addPrevoteRequests := make(chan tmi.AddPrevoteRequest)
	addPrecommitRequests := make(chan tmi.AddPrecommitRequest)

	// Okay to be unbuffered, this request would block reading from the response regardless.
	smRoundEntranceIn := make(chan tmeil.StateMachineRoundEntrance)

	// Must be unbuffered so kernel knows exactly what was sent to state machine.
	smViewOut := make(chan tmeil.StateMachineRoundView)

	log := gtest.NewLogger(t)
	wd, wCtx := gwatchdog.NewNopWatchdog(ctx, log.With("sys", "watchdog"))

	// Ensure the watchdog doesn't log after test completion.
	// There ought to be a defer cancel before the call to NewFixture anyway.
	t.Cleanup(wd.Wait)

	return &KernelFixture{
		Log: log,

		Fx: fx,

		GossipStrategyOut: gso,

		NHRRequests:        nhrRequests,
		SnapshotRequests:   snapshotRequests,
		ViewLookupRequests: viewLookupRequests,

		AddPBRequests:        addPBRequests,
		AddPrevoteRequests:   addPrevoteRequests,
		AddPrecommitRequests: addPrecommitRequests,

		StateMachineRoundEntranceIn: smRoundEntranceIn,
		StateMachineRoundViewOut:    smViewOut,

		Cfg: tmi.KernelConfig{
			Store:          tmmemstore.NewMirrorStore(),
			BlockStore:     tmmemstore.NewBlockStore(),
			RoundStore:     tmmemstore.NewRoundStore(),
			ValidatorStore: tmmemstore.NewValidatorStore(fx.HashScheme),

			InitialHeight:     1,
			InitialValidators: fx.Vals(),

			HashScheme:                        fx.HashScheme,
			SignatureScheme:                   fx.SignatureScheme,
			CommonMessageSignatureProofScheme: fx.CommonMessageSignatureProofScheme,

			GossipStrategyOut:           gso,
			StateMachineRoundEntranceIn: smRoundEntranceIn,
			StateMachineRoundViewOut:    smViewOut,

			NHRRequests:        nhrRequests,
			SnapshotRequests:   snapshotRequests,
			ViewLookupRequests: viewLookupRequests,

			AddPBRequests:        addPBRequests,
			AddPrevoteRequests:   addPrevoteRequests,
			AddPrecommitRequests: addPrecommitRequests,

			Watchdog: wd,
		},

		WatchdogCtx: wCtx,
	}
}

func (f *KernelFixture) NewKernel() *tmi.Kernel {
	k, err := tmi.NewKernel(f.WatchdogCtx, f.Log, f.Cfg)
	if err != nil {
		panic(err)
	}

	return k
}
