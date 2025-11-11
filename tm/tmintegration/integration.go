package tmintegration

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
	"time"

	"github.com/gordian-engine/gordian/gassert/gasserttest"
	"github.com/gordian-engine/gordian/gwatchdog"
	"github.com/gordian-engine/gordian/internal/gtest"
	"github.com/gordian-engine/gordian/tm/tmconsensus"
	"github.com/gordian-engine/gordian/tm/tmdebug"
	"github.com/gordian-engine/gordian/tm/tmdriver"
	"github.com/gordian-engine/gordian/tm/tmengine"
	"github.com/gordian-engine/gordian/tm/tmgossip"
	"github.com/gordian-engine/gordian/tm/tmp2p"
	"github.com/stretchr/testify/require"
)

func RunIntegrationTest(t *testing.T, ff FactoryFunc) {
	t.Run("basic flow with identity app", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		const netSize = 2

		stores := make([]BlockDataStore, netSize)
		for i := range stores {
			stores[i] = IdentityBlockDataStore{}
		}

		n, sf := ff(t, ctx, stores)
		defer n.Wait()
		defer cancel()

		fx := n.Fixture()

		genesis := fx.DefaultGenesis()

		n.Stabilize(ctx)

		apps := make([]*identityApp, netSize)
		gStrats := make([]tmgossip.Strategy, netSize)

		log := gtest.NewLogger(t)

		for i := range netSize {
			wd, wCtx := gwatchdog.NewWatchdog(ctx, log.With("sys", "watchdog", "idx", i))
			t.Cleanup(wd.Wait)
			t.Cleanup(cancel)

			gs := n.GetGossipStrategy(wCtx, i)
			gStrats[i] = gs
			t.Cleanup(gs.Wait)
			t.Cleanup(cancel)

			cStrat := &identityConsensusStrategy{
				Log:    log.With("sys", "consensusstrategy", "idx", i),
				PubKey: fx.PrivVals[i].Val.PubKey,
			}

			blockFinCh := make(chan tmdriver.FinalizeBlockRequest)
			initChainCh := make(chan tmdriver.InitChainRequest)

			app := newIdentityApp(
				ctx, log.With("sys", "app", "idx", i), i,
				initChainCh, blockFinCh,
			)
			t.Cleanup(app.Wait)
			t.Cleanup(cancel)

			apps[i] = app

			e, err := tmengine.New(
				wCtx,
				log.With("sys", "engine", "idx", i),

				tmengine.WithActionStore(sf.NewActionStore(wCtx, i)),
				tmengine.WithCommittedHeaderStore(sf.NewCommittedHeaderStore(wCtx, i)),
				tmengine.WithFinalizationStore(sf.NewFinalizationStore(wCtx, i)),
				tmengine.WithMirrorStore(sf.NewMirrorStore(wCtx, i)),
				tmengine.WithRoundStore(sf.NewRoundStore(wCtx, i)),
				tmengine.WithStateMachineStore(sf.NewStateMachineStore(wCtx, i)),
				tmengine.WithValidatorStore(sf.NewValidatorStore(wCtx, i, fx.HashScheme)),

				tmengine.WithHashScheme(fx.HashScheme),
				tmengine.WithSignatureScheme(fx.SignatureScheme),
				tmengine.WithCommonMessageSignatureProofScheme(fx.CommonMessageSignatureProofScheme),

				tmengine.WithGossipStrategy(gs),
				tmengine.WithConsensusStrategy(cStrat),
				tmengine.WithProposedHeaderInterceptor(n.GetProposedHeaderInterceptor(ctx, i)),

				tmengine.WithGenesis(&tmconsensus.ExternalGenesis{
					ChainID:             genesis.ChainID,
					InitialHeight:       genesis.InitialHeight,
					InitialAppState:     strings.NewReader(""), // No initial app state for identity app.
					GenesisValidatorSet: fx.ValSet(),
				}),

				// TODO: this might need scaled up to run on a slower machine.
				// Plus we really don't want to trigger any timeouts during these tests anyway.
				tmengine.WithTimeoutStrategy(ctx, tmengine.LinearTimeoutStrategy{
					ProposalBase: 250 * time.Millisecond,

					PrevoteDelayBase:   100 * time.Millisecond,
					PrecommitDelayBase: 100 * time.Millisecond,

					CommitWaitBase: 15 * time.Millisecond,
				}),

				tmengine.WithBlockDataArrivalChannel(n.GetBlockDataArrivalChannel(wCtx, i)),
				tmengine.WithBlockFinalizationChannel(blockFinCh),
				tmengine.WithInitChainChannel(initChainCh),

				tmengine.WithSigner(tmconsensus.PassthroughSigner{
					Signer:          fx.PrivVals[i].Signer,
					SignatureScheme: fx.SignatureScheme,
				}),

				tmengine.WithWatchdog(wd),

				tmengine.WithAssertEnv(gasserttest.DefaultEnv()),
			)
			require.NoError(t, err)
			t.Cleanup(e.Wait)
			t.Cleanup(cancel)

			n.SetConsensusHandler(wCtx, i, tmconsensus.AcceptAllValidFeedbackMapper{
				Handler: e,
			})
		}

		for i := uint64(1); i < 6; i++ {
			t.Logf("Beginning finalization sync for height %d", i)
			for appIdx := range apps {
				finResp := gtest.ReceiveOrTimeout(t, apps[appIdx].FinalizeResponses, gtest.ScaleMs(1200))
				require.Equal(t, i, finResp.Height)

				round := finResp.Round

				expData := fmt.Sprintf("Height: %d; Round: %d", finResp.Height, round)
				expDataHash := sha256.Sum256([]byte(expData))
				require.Equal(t, expDataHash[:], finResp.AppStateHash)
			}
		}
	})

	t.Run("basic flow with validator shuffle app", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		const netSize = 6
		const pickN = 4

		stores := make([]BlockDataStore, netSize)
		for i := range stores {
			stores[i] = IdentityBlockDataStore{}
		}

		n, sf := ff(t, ctx, stores)
		defer n.Wait()
		defer cancel()

		fx := n.Fixture()

		genesis := fx.DefaultGenesis()

		n.Stabilize(ctx)

		apps := make([]*valShuffleApp, netSize)
		gStrats := make([]tmgossip.Strategy, netSize)

		log := gtest.NewLogger(t)

		for i := range netSize {
			wd, wCtx := gwatchdog.NewWatchdog(ctx, log.With("sys", "watchdog", "idx", i))
			t.Cleanup(wd.Wait)
			t.Cleanup(cancel)

			gs := n.GetGossipStrategy(wCtx, i)
			gStrats[i] = gs
			t.Cleanup(gs.Wait)
			t.Cleanup(cancel)

			cStrat := &valShuffleConsensusStrategy{
				Log:        log.With("sys", "consensusstrategy", "idx", i),
				PubKey:     fx.PrivVals[i].Val.PubKey,
				HashScheme: fx.HashScheme,
			}

			blockFinCh := make(chan tmdriver.FinalizeBlockRequest)
			initChainCh := make(chan tmdriver.InitChainRequest)

			app := newValShuffleApp(
				ctx, log.With("sys", "app", "idx", i), i,
				fx.HashScheme, pickN, initChainCh, blockFinCh,
			)
			t.Cleanup(app.Wait)
			t.Cleanup(cancel)

			apps[i] = app

			e, err := tmengine.New(
				wCtx,
				log.With("sys", "engine", "idx", i),

				tmengine.WithActionStore(sf.NewActionStore(wCtx, i)),
				tmengine.WithCommittedHeaderStore(sf.NewCommittedHeaderStore(wCtx, i)),
				tmengine.WithFinalizationStore(sf.NewFinalizationStore(wCtx, i)),
				tmengine.WithMirrorStore(sf.NewMirrorStore(wCtx, i)),
				tmengine.WithRoundStore(sf.NewRoundStore(wCtx, i)),
				tmengine.WithStateMachineStore(sf.NewStateMachineStore(wCtx, i)),
				tmengine.WithValidatorStore(sf.NewValidatorStore(wCtx, i, fx.HashScheme)),

				tmengine.WithHashScheme(fx.HashScheme),
				tmengine.WithSignatureScheme(fx.SignatureScheme),
				tmengine.WithCommonMessageSignatureProofScheme(fx.CommonMessageSignatureProofScheme),

				tmengine.WithGossipStrategy(gs),
				tmengine.WithConsensusStrategy(cStrat),
				tmengine.WithProposedHeaderInterceptor(n.GetProposedHeaderInterceptor(ctx, i)),

				tmengine.WithGenesis(&tmconsensus.ExternalGenesis{
					ChainID:             genesis.ChainID,
					InitialHeight:       genesis.InitialHeight,
					InitialAppState:     strings.NewReader(""), // No initial app state for identity app.
					GenesisValidatorSet: fx.ValSet(),
				}),

				// TODO: this might need scaled up to run on a slower machine.
				// Plus we really don't want to trigger any timeouts during these tests anyway.
				tmengine.WithTimeoutStrategy(ctx, tmengine.LinearTimeoutStrategy{
					ProposalBase: 250 * time.Millisecond,

					PrevoteDelayBase:   100 * time.Millisecond,
					PrecommitDelayBase: 100 * time.Millisecond,

					CommitWaitBase: 15 * time.Millisecond,
				}),

				tmengine.WithBlockDataArrivalChannel(n.GetBlockDataArrivalChannel(wCtx, i)),
				tmengine.WithBlockFinalizationChannel(blockFinCh),
				tmengine.WithInitChainChannel(initChainCh),

				tmengine.WithSigner(tmconsensus.PassthroughSigner{
					Signer:          fx.PrivVals[i].Signer,
					SignatureScheme: fx.SignatureScheme,
				}),

				tmengine.WithWatchdog(wd),

				tmengine.WithAssertEnv(gasserttest.DefaultEnv()),
			)
			require.NoError(t, err)
			t.Cleanup(e.Wait)
			t.Cleanup(cancel)

			const debugging = false
			var handler tmconsensus.FineGrainedConsensusHandler = e
			if debugging {
				handler = tmdebug.LoggingFineGrainedConsensusHandler{
					Log:     log.With("debug", "consensus", "idx", i),
					Handler: e,
				}
			}

			n.SetConsensusHandler(wCtx, i, tmconsensus.DropDuplicateFeedbackMapper{
				Handler: handler,
			})
		}

		for height := uint64(1); height < 6; height++ {
			t.Logf("Beginning finalization sync for height %d", height)
			for appIdx := range apps {
				finResp := gtest.ReceiveOrTimeout(t, apps[appIdx].FinalizeResponses, gtest.ScaleMs(500))
				require.Equal(t, height, finResp.Height)

				require.Len(t, finResp.Validators, pickN)

				// TODO: There should be more assertions around the specific validators here.
			}
		}
	})

	t.Run("basic flow with random data app", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		const netSize = 4

		stores := make([]BlockDataStore, netSize)
		for i := range stores {
			stores[i] = NewBlockDataMap()
		}

		n, sf := ff(t, ctx, stores)
		defer n.Wait()
		defer cancel()

		fx := n.Fixture()

		genesis := fx.DefaultGenesis()

		n.Stabilize(ctx)

		apps := make([]*randomDataApp, netSize)
		gStrats := make([]tmgossip.Strategy, netSize)

		log := gtest.NewLogger(t)

		for i := range netSize {
			wd, wCtx := gwatchdog.NewWatchdog(ctx, log.With("sys", "watchdog", "idx", i))
			t.Cleanup(wd.Wait)
			t.Cleanup(cancel)

			gs := n.GetGossipStrategy(wCtx, i)
			gStrats[i] = gs
			t.Cleanup(gs.Wait)
			t.Cleanup(cancel)

			seed := [32]byte{}
			binary.BigEndian.PutUint64(seed[:], uint64(i))
			rng := rand.NewChaCha8(seed)

			cStrat := &randomDataConsensusStrategy{
				Log:    log.With("sys", "consensusstrategy", "idx", i),
				PubKey: fx.PrivVals[i].Val.PubKey,
				Store:  stores[i],

				RNG: rng,
			}

			blockFinCh := make(chan tmdriver.FinalizeBlockRequest)
			initChainCh := make(chan tmdriver.InitChainRequest)

			app := newRandomDataApp(
				ctx, log.With("sys", "app", "idx", i), i,
				initChainCh, blockFinCh,
			)
			t.Cleanup(app.Wait)
			t.Cleanup(cancel)

			apps[i] = app

			e, err := tmengine.New(
				wCtx,
				log.With("sys", "engine", "idx", i),

				tmengine.WithActionStore(sf.NewActionStore(wCtx, i)),
				tmengine.WithCommittedHeaderStore(sf.NewCommittedHeaderStore(wCtx, i)),
				tmengine.WithFinalizationStore(sf.NewFinalizationStore(wCtx, i)),
				tmengine.WithMirrorStore(sf.NewMirrorStore(wCtx, i)),
				tmengine.WithRoundStore(sf.NewRoundStore(wCtx, i)),
				tmengine.WithStateMachineStore(sf.NewStateMachineStore(wCtx, i)),
				tmengine.WithValidatorStore(sf.NewValidatorStore(wCtx, i, fx.HashScheme)),

				tmengine.WithHashScheme(fx.HashScheme),
				tmengine.WithSignatureScheme(fx.SignatureScheme),
				tmengine.WithCommonMessageSignatureProofScheme(fx.CommonMessageSignatureProofScheme),

				tmengine.WithGossipStrategy(gs),
				tmengine.WithConsensusStrategy(cStrat),
				tmengine.WithProposedHeaderInterceptor(n.GetProposedHeaderInterceptor(ctx, i)),

				tmengine.WithGenesis(&tmconsensus.ExternalGenesis{
					ChainID:             genesis.ChainID,
					InitialHeight:       genesis.InitialHeight,
					InitialAppState:     strings.NewReader(""), // No initial app state for identity app.
					GenesisValidatorSet: fx.ValSet(),
				}),

				// TODO: this might need scaled up to run on a slower machine.
				// Plus we really don't want to trigger any timeouts during these tests anyway.
				tmengine.WithTimeoutStrategy(ctx, tmengine.LinearTimeoutStrategy{
					ProposalBase: 250 * time.Millisecond,

					PrevoteDelayBase:   100 * time.Millisecond,
					PrecommitDelayBase: 100 * time.Millisecond,

					CommitWaitBase: 15 * time.Millisecond,
				}),

				tmengine.WithBlockDataArrivalChannel(n.GetBlockDataArrivalChannel(wCtx, i)),
				tmengine.WithBlockFinalizationChannel(blockFinCh),
				tmengine.WithInitChainChannel(initChainCh),

				tmengine.WithSigner(tmconsensus.PassthroughSigner{
					Signer:          fx.PrivVals[i].Signer,
					SignatureScheme: fx.SignatureScheme,
				}),

				tmengine.WithWatchdog(wd),

				tmengine.WithAssertEnv(gasserttest.DefaultEnv()),
			)
			require.NoError(t, err)
			t.Cleanup(e.Wait)
			t.Cleanup(cancel)

			const debugging = false
			var handler tmconsensus.FineGrainedConsensusHandler = e
			if debugging {
				handler = tmdebug.LoggingFineGrainedConsensusHandler{
					Log:     log.With("debug", "consensus", "idx", i),
					Handler: e,
				}
			}

			n.SetConsensusHandler(wCtx, i, tmconsensus.DropDuplicateFeedbackMapper{
				Handler: handler,
			})
		}

		for height := uint64(1); height < 6; height++ {
			t.Logf("Beginning finalization sync for height %d", height)
			for appIdx := range apps {
				finResp := gtest.ReceiveOrTimeout(t, apps[appIdx].FinalizeResponses, gtest.ScaleMs(500))
				require.Equal(t, height, finResp.Height)
			}
		}
	})
}

func RunIntegrationTest_p2p(t *testing.T, nf NewFactoryFunc) {
	t.Run("basic flow with identity app", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		log := gtest.NewLogger(t)
		f := nf(&Env{
			RootLogger: log,

			tb: t,
		})

		const netSize = 2

		n, fx, err := f.NewNetwork(t, ctx, netSize)
		require.NoError(t, err)
		defer n.Wait()
		defer cancel()

		genesis := fx.DefaultGenesis()

		// Make just the connections first, so we can stabilize the network,
		// before we begin instantiating the engines.
		conns := make([]tmp2p.Connection, len(fx.PrivVals))
		for i := range fx.PrivVals {
			conn, err := n.Connect(ctx)
			require.NoError(t, err)
			conns[i] = conn
		}

		require.NoError(t, n.Stabilize(ctx))

		apps := make([]*identityApp, len(fx.PrivVals))

		for i, v := range fx.PrivVals {
			as, err := f.NewActionStore(ctx, i)
			require.NoError(t, err)

			chs, err := f.NewCommittedHeaderStore(ctx, i)
			require.NoError(t, err)

			fs, err := f.NewFinalizationStore(ctx, i)
			require.NoError(t, err)

			ms, err := f.NewMirrorStore(ctx, i)
			require.NoError(t, err)

			rs, err := f.NewRoundStore(ctx, i)
			require.NoError(t, err)

			sms, err := f.NewStateMachineStore(ctx, i)
			require.NoError(t, err)

			vs, err := f.NewValidatorStore(ctx, i, fx.HashScheme)
			require.NoError(t, err)

			gStrat, err := f.NewGossipStrategy(ctx, i, conns[i])
			require.NoError(t, err)

			cStrat := &identityConsensusStrategy{
				Log:    log.With("sys", "consensusstrategy", "idx", i),
				PubKey: v.Val.PubKey,
			}

			blockFinCh := make(chan tmdriver.FinalizeBlockRequest)
			initChainCh := make(chan tmdriver.InitChainRequest)

			app := newIdentityApp(
				ctx, log.With("sys", "app", "idx", i), i,
				initChainCh, blockFinCh,
			)
			t.Cleanup(app.Wait)
			t.Cleanup(cancel)

			apps[i] = app

			wd, wCtx := gwatchdog.NewWatchdog(ctx, log.With("sys", "watchdog", "idx", i))
			t.Cleanup(wd.Wait)
			t.Cleanup(cancel)

			e, err := tmengine.New(
				wCtx,
				log.With("sys", "engine", "idx", i),
				tmengine.WithActionStore(as),
				tmengine.WithCommittedHeaderStore(chs),
				tmengine.WithFinalizationStore(fs),
				tmengine.WithMirrorStore(ms),
				tmengine.WithRoundStore(rs),
				tmengine.WithStateMachineStore(sms),
				tmengine.WithValidatorStore(vs),

				tmengine.WithHashScheme(fx.HashScheme),
				tmengine.WithSignatureScheme(fx.SignatureScheme),
				tmengine.WithCommonMessageSignatureProofScheme(fx.CommonMessageSignatureProofScheme),

				tmengine.WithGossipStrategy(gStrat),
				tmengine.WithConsensusStrategy(cStrat),

				tmengine.WithGenesis(&tmconsensus.ExternalGenesis{
					ChainID:             genesis.ChainID,
					InitialHeight:       genesis.InitialHeight,
					InitialAppState:     strings.NewReader(""), // No initial app state for identity app.
					GenesisValidatorSet: fx.ValSet(),
				}),

				// TODO: this might need scaled up to run on a slower machine.
				// Plus we really don't want to trigger any timeouts during these tests anyway.
				tmengine.WithTimeoutStrategy(ctx, tmengine.LinearTimeoutStrategy{
					ProposalBase: 250 * time.Millisecond,

					PrevoteDelayBase:   100 * time.Millisecond,
					PrecommitDelayBase: 100 * time.Millisecond,

					CommitWaitBase: 15 * time.Millisecond,
				}),

				tmengine.WithBlockFinalizationChannel(blockFinCh),
				tmengine.WithInitChainChannel(initChainCh),

				tmengine.WithSigner(tmconsensus.PassthroughSigner{
					Signer:          v.Signer,
					SignatureScheme: fx.SignatureScheme,
				}),

				tmengine.WithWatchdog(wd),

				tmengine.WithAssertEnv(gasserttest.DefaultEnv()),
			)
			require.NoError(t, err)
			t.Cleanup(e.Wait)
			t.Cleanup(cancel)

			conns[i].SetConsensusHandler(ctx, tmconsensus.AcceptAllValidFeedbackMapper{
				Handler: e,
			})
		}

		for i := uint64(1); i < 6; i++ {
			t.Logf("Beginning finalization sync for height %d", i)
			for appIdx := range apps {
				finResp := gtest.ReceiveOrTimeout(t, apps[appIdx].FinalizeResponses, gtest.ScaleMs(1200))
				require.Equal(t, i, finResp.Height)

				round := finResp.Round

				expData := fmt.Sprintf("Height: %d; Round: %d", finResp.Height, round)
				expDataHash := sha256.Sum256([]byte(expData))
				require.Equal(t, expDataHash[:], finResp.AppStateHash)
			}
		}
	})

	t.Run("basic flow with validator shuffle app", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		log := gtest.NewLogger(t)
		f := nf(&Env{
			RootLogger: log,

			tb: t,
		})

		const netSize = 6 // Total number of validators in the network.
		const pickN = 4   // How many validators participate in rounds beyond initial height.

		n, fx, err := f.NewNetwork(t, ctx, netSize)
		require.NoError(t, err)
		defer n.Wait()
		defer cancel()

		genesis := fx.DefaultGenesis()

		// Make just the connections first, so we can stabilize the network,
		// before we begin instantiating the engines.
		conns := make([]tmp2p.Connection, len(fx.PrivVals))
		for i := range fx.PrivVals {
			conn, err := n.Connect(ctx)
			require.NoError(t, err)
			conns[i] = conn
		}

		require.NoError(t, n.Stabilize(ctx))

		apps := make([]*valShuffleApp, len(fx.PrivVals))

		for i, v := range fx.PrivVals {
			as, err := f.NewActionStore(ctx, i)
			require.NoError(t, err)

			chs, err := f.NewCommittedHeaderStore(ctx, i)
			require.NoError(t, err)

			fs, err := f.NewFinalizationStore(ctx, i)
			require.NoError(t, err)

			ms, err := f.NewMirrorStore(ctx, i)
			require.NoError(t, err)

			rs, err := f.NewRoundStore(ctx, i)
			require.NoError(t, err)

			sms, err := f.NewStateMachineStore(ctx, i)
			require.NoError(t, err)

			vs, err := f.NewValidatorStore(ctx, i, fx.HashScheme)
			require.NoError(t, err)

			gStrat, err := f.NewGossipStrategy(ctx, i, conns[i])
			require.NoError(t, err)

			cStrat := &valShuffleConsensusStrategy{
				Log:        log.With("sys", "consensusstrategy", "idx", i),
				PubKey:     v.Val.PubKey,
				HashScheme: fx.HashScheme,
			}

			blockFinCh := make(chan tmdriver.FinalizeBlockRequest)
			initChainCh := make(chan tmdriver.InitChainRequest)

			app := newValShuffleApp(
				ctx, log.With("sys", "app", "idx", i), i,
				fx.HashScheme, pickN, initChainCh, blockFinCh,
			)
			t.Cleanup(app.Wait)
			t.Cleanup(cancel)

			apps[i] = app

			wd, wCtx := gwatchdog.NewWatchdog(ctx, log.With("sys", "watchdog", "idx", i))
			t.Cleanup(wd.Wait)
			t.Cleanup(cancel)

			e, err := tmengine.New(
				wCtx,
				log.With("sys", "engine", "idx", i),
				tmengine.WithActionStore(as),
				tmengine.WithCommittedHeaderStore(chs),
				tmengine.WithFinalizationStore(fs),
				tmengine.WithMirrorStore(ms),
				tmengine.WithRoundStore(rs),
				tmengine.WithStateMachineStore(sms),
				tmengine.WithValidatorStore(vs),

				tmengine.WithHashScheme(fx.HashScheme),
				tmengine.WithSignatureScheme(fx.SignatureScheme),
				tmengine.WithCommonMessageSignatureProofScheme(fx.CommonMessageSignatureProofScheme),

				tmengine.WithGossipStrategy(gStrat),
				tmengine.WithConsensusStrategy(cStrat),

				tmengine.WithGenesis(&tmconsensus.ExternalGenesis{
					ChainID:             genesis.ChainID,
					InitialHeight:       genesis.InitialHeight,
					InitialAppState:     strings.NewReader(""), // No initial app state for identity app.
					GenesisValidatorSet: fx.ValSet(),
				}),

				// TODO: this might need scaled up to run on a slower machine.
				// Plus we really don't want to trigger any timeouts during these tests anyway.
				tmengine.WithTimeoutStrategy(ctx, tmengine.LinearTimeoutStrategy{
					ProposalBase: 250 * time.Millisecond,

					PrevoteDelayBase:   100 * time.Millisecond,
					PrecommitDelayBase: 100 * time.Millisecond,

					CommitWaitBase: 15 * time.Millisecond,
				}),

				tmengine.WithBlockFinalizationChannel(blockFinCh),
				tmengine.WithInitChainChannel(initChainCh),

				tmengine.WithSigner(tmconsensus.PassthroughSigner{
					Signer:          v.Signer,
					SignatureScheme: fx.SignatureScheme,
				}),

				tmengine.WithWatchdog(wd),

				tmengine.WithAssertEnv(gasserttest.DefaultEnv()),
			)
			require.NoError(t, err)
			t.Cleanup(e.Wait)
			t.Cleanup(cancel)

			const debugging = false
			var handler tmconsensus.FineGrainedConsensusHandler = e
			if debugging {
				handler = tmdebug.LoggingFineGrainedConsensusHandler{
					Log:     log.With("debug", "consensus", "idx", i),
					Handler: e,
				}
			}
			conns[i].SetConsensusHandler(ctx, tmconsensus.DropDuplicateFeedbackMapper{
				Handler: handler,
			})
		}

		for height := uint64(1); height < 6; height++ {
			t.Logf("Beginning finalization sync for height %d", height)
			for appIdx := range apps {
				finResp := gtest.ReceiveOrTimeout(t, apps[appIdx].FinalizeResponses, gtest.ScaleMs(500))
				require.Equal(t, height, finResp.Height)

				require.Len(t, finResp.Validators, pickN)

				// TODO: There should be more assertions around the specific validators here.
			}
		}
	})
}
