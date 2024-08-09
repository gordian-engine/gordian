package gci

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"

	coreappmgr "cosmossdk.io/core/app"
	"cosmossdk.io/core/transaction"
	serverv2 "cosmossdk.io/server/v2"
	"cosmossdk.io/server/v2/appmanager"
	"cosmossdk.io/simapp/v2"
	simdcmd "cosmossdk.io/simapp/v2/simdv2/cmd"
	stakingtypes "cosmossdk.io/x/staking/types"
	"github.com/cosmos/cosmos-sdk/client"
	ced25519 "github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	"github.com/cosmos/cosmos-sdk/server"
	genutiltypes "github.com/cosmos/cosmos-sdk/x/genutil/types"
	"github.com/libp2p/go-libp2p"
	"github.com/rollchains/gordian/gcosmos/gccodec"
	"github.com/rollchains/gordian/gcosmos/gserver"
	"github.com/rollchains/gordian/gcrypto"
	"github.com/rollchains/gordian/gwatchdog"
	"github.com/rollchains/gordian/internal/gchan"
	"github.com/rollchains/gordian/tm/tmcodec/tmjson"
	"github.com/rollchains/gordian/tm/tmconsensus"
	"github.com/rollchains/gordian/tm/tmconsensus/tmconsensustest"
	"github.com/rollchains/gordian/tm/tmdriver"
	"github.com/rollchains/gordian/tm/tmengine"
	"github.com/rollchains/gordian/tm/tmgossip"
	"github.com/rollchains/gordian/tm/tmp2p/tmlibp2p"
	"github.com/rollchains/gordian/tm/tmstore/tmmemstore"
	"github.com/spf13/cobra"
)

// NewSimdRootCmdWithGordian calls a simdcmd function we have added
// in order to get simd start to use Gordian instead of Comet.
func NewSimdRootCmdWithGordian(lifeCtx context.Context, log *slog.Logger) *cobra.Command {
	return simdcmd.NewRootCmdWithServer(func(cc client.Context) serverv2.ServerComponent[transaction.Tx] {
		codec := gccodec.NewTxDecoder(cc.TxConfig)
		c, err := gserver.NewComponent(lifeCtx, log, codec, cc.Codec)
		if err != nil {
			panic(err)
		}
		return c
	})
}

func StartGordianCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "gstart",
		Short:  "Start the gordian application",
		PreRun: setServerContextLogger,
		RunE: func(cmd *cobra.Command, args []string) error {
			serverCtx := server.GetServerContextFromCmd(cmd)

			// The simapp and app manager are the core SDK pieces required
			// to integrate with a consensus engine.

			// It seems like this BindPFlags call would happen automatically somewhere,
			// but it is currently necessary to preconfigure the Viper value passed to simapp.NewSimApp.
			serverCtx.Viper.BindPFlags(cmd.Flags())

			sa := simapp.NewSimApp[transaction.Tx](serverCtx.Logger, serverCtx.Viper)
			am := sa.App.AppManager

			// We should have set the logger to the slog implementation
			// in this command's PreRun.
			// For now a panic on failure to convert is fine.
			log := serverCtx.Logger.Impl().(*slog.Logger)

			ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt)
			defer cancel()

			// Right now, only start a tmlibp2p host, just to prove that gordian and the SDK
			// can both be imported into the same program.
			h, err := tmlibp2p.NewHost(
				ctx,
				tmlibp2p.HostOptions{
					Options: []libp2p.Option{
						// No explicit listen address.

						// Unsure if this is something we always want.
						// Can be controlled by a flag later if undesirable by default.
						libp2p.ForceReachabilityPublic(),
					},
				},
			)
			if err != nil {
				return fmt.Errorf("failed to create libp2p host: %w", err)
			}

			log.Info("Started libp2p host", "id", h.Libp2pHost().ID().String())

			defer func() {
				if err := h.Close(); err != nil {
					log.Warn("Error closing libp2p host", "err", err)
				}
			}()

			cc := client.GetClientContextFromCmd(cmd)
			initChainCh := make(chan tmdriver.InitChainRequest)
			go runDriver(
				ctx,
				log.With("sys", "driver"),
				// This used to be serverCtx.Config.RootDir,
				// but for some reason that value doesn't get set correctly anymore.
				filepath.Join(cc.HomeDir, serverCtx.Config.Genesis),
				am,
				gccodec.NewTxDecoder(cc.TxConfig),
				initChainCh,
			)

			// TODO: how to set up signer?
			var signer gcrypto.Signer

			return runStateMachine(ctx, log, h, signer, initChainCh)
		},
	}
	return cmd
}

func runStateMachine(
	ctx context.Context,
	log *slog.Logger,
	h *tmlibp2p.Host,
	signer gcrypto.Signer,
	initChainCh chan<- tmdriver.InitChainRequest,
) error {
	const chainID = "gcosmos"
	// We need a cancelable context if we fail partway through setup.
	// Be sure to defer cancel() after other deferred
	// close and cleanup calls, for types dependent on
	// a parent context cancellation.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Just reassign ctx here because we will not have any further references to the root context,
	// other than explicit cancel calls to ensure clean shutdown.
	wd, ctx := gwatchdog.NewWatchdog(ctx, log.With("sys", "watchdog"))

	reg := new(gcrypto.Registry)
	gcrypto.RegisterEd25519(reg)
	codec := tmjson.MarshalCodec{
		CryptoRegistry: reg,
	}
	conn, err := tmlibp2p.NewConnection(
		ctx,
		log.With("sys", "libp2pconn"),
		h,
		codec,
	)
	if err != nil {
		return fmt.Errorf("failed to build libp2p connection: %w", err)
	}
	defer conn.Disconnect()
	defer cancel()

	var as *tmmemstore.ActionStore
	if signer != nil {
		as = tmmemstore.NewActionStore()
	}

	bs := tmmemstore.NewBlockStore()
	fs := tmmemstore.NewFinalizationStore()
	ms := tmmemstore.NewMirrorStore()
	rs := tmmemstore.NewRoundStore()
	vs := tmmemstore.NewValidatorStore(tmconsensustest.SimpleHashScheme{})

	blockFinCh := make(chan tmdriver.FinalizeBlockRequest)

	// TODO: driver instantiation, and consensus strategy, would usually go here.
	// Obviously the nop consensus strategy isn't very useful.
	var cStrat tmconsensus.ConsensusStrategy = tmconsensustest.NopConsensusStrategy{}

	var signerPubKey gcrypto.PubKey
	if signer != nil {
		signerPubKey = signer.PubKey()
		// TODO: probably pass signerPubKey to consensus strategy
		_ = signerPubKey
	}

	gs := tmgossip.NewChattyStrategy(ctx, log.With("sys", "chattygossip"), conn)

	// TODO: when should metrics be enabled?
	metricsCh := make(chan tmengine.Metrics)

	e, err := tmengine.New(
		ctx,
		log.With("sys", "engine"),
		tmengine.WithActionStore(as),
		tmengine.WithBlockStore(bs),
		tmengine.WithFinalizationStore(fs),
		tmengine.WithMirrorStore(ms),
		tmengine.WithRoundStore(rs),
		tmengine.WithValidatorStore(vs),

		tmengine.WithHashScheme(tmconsensustest.SimpleHashScheme{}),
		tmengine.WithSignatureScheme(tmconsensustest.SimpleSignatureScheme{}),
		tmengine.WithCommonMessageSignatureProofScheme(gcrypto.SimpleCommonMessageSignatureProofScheme),

		tmengine.WithConsensusStrategy(cStrat),
		tmengine.WithGossipStrategy(gs),

		tmengine.WithGenesis(&tmconsensus.ExternalGenesis{
			ChainID:           chainID,
			InitialHeight:     1,
			InitialAppState:   strings.NewReader(""), // No initial app state for echo app.
			GenesisValidators: nil,                   // TODO: where will the validators come from?
		}),

		tmengine.WithTimeoutStrategy(ctx, tmengine.LinearTimeoutStrategy{}),

		tmengine.WithBlockFinalizationChannel(blockFinCh),
		tmengine.WithInitChainChannel(initChainCh),

		tmengine.WithSigner(signer),

		tmengine.WithMetricsChannel(metricsCh),

		tmengine.WithWatchdog(wd),
	)
	if err != nil {
		return fmt.Errorf("failed to build engine: %w", err)
	}

	conn.SetConsensusHandler(ctx, tmconsensus.AcceptAllValidFeedbackMapper{
		Handler: e,
	})
	defer e.Wait()

	if signer == nil {
		log.Info("Running follower engine...")
	} else {
		log.Info("Running engine...")
	}
	<-ctx.Done()
	log.Info("Shutting down...")

	return nil
}

func runDriver(
	ctx context.Context,
	log *slog.Logger,
	genesisPath string,
	appManager *appmanager.AppManager[transaction.Tx],
	txCodec transaction.Codec[transaction.Tx],
	initChainCh <-chan tmdriver.InitChainRequest,
) {
	defer log.Info("Driver goroutine finished")
	log.Info("Driver starting...")

	ag, err := genutiltypes.AppGenesisFromFile(genesisPath)
	if err != nil {
		log.Warn("Failed to AppGenesisFromFile", "genesisPath", genesisPath, "err", err)
		return
	}

	req, ok := gchan.RecvC(ctx, log, initChainCh, "receiving init chain request")
	if !ok {
		log.Warn("Context cancelled before receiving init chain message")
		return
	}

	blockReq := &coreappmgr.BlockRequest[transaction.Tx]{
		Height: req.Genesis.InitialHeight, // Comet does genesis height - 1, do we need that too?

		ChainId:   req.Genesis.ChainID,
		IsGenesis: true,

		// Omitted vs comet server code: Time, Hash, AppHash, ConsensusMessages
	}

	appState := []byte(ag.AppState)

	// Now, init genesis in the SDK-layer application.
	blockResp, genesisState, err := appManager.InitGenesis(
		ctx, blockReq, appState, txCodec,
	)
	if err != nil {
		log.Warn("Failed to run appManager.InitGenesis", "genesisPath", genesisPath, "appState", fmt.Sprintf("%q", appState), "err", err)
		return
	}

	log.Info("Got init chain request", "val", req)
	log.Info("App response for init chain", "blockResp", blockResp)
	for i, res := range blockResp.TxResults {
		if res.Error != nil {
			log.Info("Error in blockResp", "i", i, "err", res.Error)
		}
	}
	log.Info("Genesis state from init chain", "genesisState", genesisState)
}

func setServerContextLogger(cmd *cobra.Command, args []string) {
	// Attempting to use the github.com/samber/slog-zerolog-v2 repository
	// to convert the presumed zerolog logger to slog
	// was causing all gordian log messages to be dropped.
	// So just use our own slog-backed cosmos Logger.
	//
	// Doing this at the top level before executing the command failed.
	// Presumably somewhere in the command sequence,
	// the logger was overwritten back to zerolog.
	//
	// It is possible this will cause other logging details in the SDK to break.
	// But interleaved output from running two independent loggers also sounds bad.
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	overwriteServerContextLogger(cmd, logger)
}

func CheckGenesisValidatorsCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "check-genesis-validators",
		Aliases: []string{"cgv"},
		Short:   "Print the gentx/genesis validators in the associated genesis file",
		PreRun:  setServerContextLogger,
		RunE: func(cmd *cobra.Command, args []string) error {
			serverCtx := server.GetServerContextFromCmd(cmd)

			// The simapp and app manager are the core SDK pieces required
			// to integrate with a consensus engine.
			sa := simapp.NewSimApp[transaction.Tx](serverCtx.Logger, serverCtx.Viper)
			am := sa.App.AppManager
			_ = am // Not actually integrated yet.

			ag, err := genutiltypes.AppGenesisFromFile(filepath.Join(
				serverCtx.Config.RootDir, serverCtx.Config.Genesis,
			))
			if err != nil {
				return fmt.Errorf("failed to AppGenesisFromFile: %w", err)
			}

			m, err := genutiltypes.GenesisStateFromAppGenesis(ag)
			if err != nil {
				return fmt.Errorf("failed to GenesisStateFromAppGenesis: %w", err)
			}

			clientCtx := client.GetClientContextFromCmd(cmd)
			gs := genutiltypes.GetGenesisStateFromAppState(clientCtx.Codec, m)

			for i, gentx := range gs.GenTxs {
				tx, err := genutiltypes.ValidateAndGetGenTx(
					gentx,
					clientCtx.TxConfig.TxJSONDecoder(),
					genutiltypes.DefaultMessageValidator,
				)
				if err != nil {
					return fmt.Errorf("failed to decode gentx at index %d: %w", i, err)
				}

				msgs, err := tx.GetMessages()
				if err != nil {
					return fmt.Errorf("failed to get tx messages at index %d: %w", i, err)
				}
				for j, msg := range msgs {
					cvm, ok := msg.(*stakingtypes.MsgCreateValidator)
					if !ok {
						continue
					}

					var pubkey cryptotypes.PubKey
					if err := clientCtx.InterfaceRegistry.UnpackAny(cvm.Pubkey, &pubkey); err != nil {
						return fmt.Errorf("failed to unpack pubkey in tx messages %d.%d: %w", i, j, err)
					}

					cPubKey, ok := pubkey.(*ced25519.PubKey)
					if !ok {
						return fmt.Errorf("failed to convert create validator pubkey of type %T to ed25519", pubkey)
					}

					gPubKey := gcrypto.Ed25519PubKey(cPubKey.Key)

					fmt.Fprintf(cmd.OutOrStdout(), "tx %d.%d: pubkey=%x\n", i, j, gPubKey.PubKeyBytes())
				}
			}

			return nil
		},
	}
	return cmd
}
