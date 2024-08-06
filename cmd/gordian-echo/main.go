package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"

	"github.com/libp2p/go-libp2p"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	libp2pevent "github.com/libp2p/go-libp2p/core/event"
	libp2phost "github.com/libp2p/go-libp2p/core/host"
	libp2ppeer "github.com/libp2p/go-libp2p/core/peer"
	"github.com/rollchains/gordian/cmd/internal/gcmd"
	"github.com/rollchains/gordian/gcrypto"
	"github.com/rollchains/gordian/gwatchdog"
	"github.com/rollchains/gordian/tm/tmcodec/tmjson"
	"github.com/rollchains/gordian/tm/tmconsensus"
	"github.com/rollchains/gordian/tm/tmconsensus/tmconsensustest"
	"github.com/rollchains/gordian/tm/tmdebug"
	"github.com/rollchains/gordian/tm/tmdriver"
	"github.com/rollchains/gordian/tm/tmengine"
	"github.com/rollchains/gordian/tm/tmgossip"
	"github.com/rollchains/gordian/tm/tmp2p/tmlibp2p"
	"github.com/rollchains/gordian/tm/tmstore/tmmemstore"
	"github.com/spf13/cobra"
)

func main() {
	if err := mainE(); err != nil {
		os.Exit(1)
	}
}

func mainE() error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	root := NewRootCmd(logger)
	if err := root.ExecuteContext(ctx); err != nil {
		logger.Info("Failure", "err", err)
		os.Stderr.Sync()
		return err
	}

	return nil
}

func NewRootCmd(log *slog.Logger) *cobra.Command {
	rootCmd := &cobra.Command{
		Use: "gordian-demo SUBCOMMAND",

		CompletionOptions: cobra.CompletionOptions{HiddenDefaultCmd: true},

		Long: `gordian-echo is used for running an "echo server" with the gordian consensus engine.

Initial setup involves:

1. Pick your insecure passphrase. These are insecure because
   we are not persisting anything to disk currently,
   so choose a very simple password that won't bother you if/when it gets leaked.
2. Discover your resulting validator public key with:
     $ gordian-echo validator-pubkey 'my-passphrase'
     b1a138599af82401286ddfbe06ac4c8d20c34d20ad27a6df2bc7f498c48c60d0
3. Once all the validators' public keys are known, create a config file like:
		 {"ValidatorPubKeys": ["id1", "id2", "id3"], "RemoteAddrs": ["/ip4/127.0.0.1/tcp/9999/p2p/$LIBP2P_ID"]}
   where the libp2p ID is derived from the libp2p-id subcommand.
4. Run the echo app:
     $ gordian-echo run-echo-validator 'my-passphrase' path/to/config.json
`,
	}

	rootCmd.AddCommand(
		NewValidatorPublicKeyCmd(log),
		NewLibp2pIDCmd(log),

		NewRunEchoValidatorCmd(log),

		NewRunP2PRelayerCmd(log),

		NewStandaloneMirrorCmd(log),

		NewFollowerCmd(log),
	)

	return rootCmd
}

func NewValidatorPublicKeyCmd(log *slog.Logger) *cobra.Command {
	return &cobra.Command{
		Use: "validator-pubkey INSECURE_PASSPHRASE",

		Aliases: []string{"validator-pub-key"},

		Short: "Print the validator public key derived from the given insecure passphrase",

		Args: cobra.ExactArgs(1),

		RunE: func(cmd *cobra.Command, args []string) error {
			signer, err := gcmd.SignerFromInsecurePassphrase("gordian-echo|", args[0])
			if err != nil {
				return err
			}

			fmt.Fprintf(cmd.OutOrStdout(), "%x\n", signer.PubKey())

			return nil
		},
	}
}

func NewLibp2pIDCmd(log *slog.Logger) *cobra.Command {
	return &cobra.Command{
		Use: "libp2p-id INSECURE_PASSPHRASE",

		Short: "Print the libp2p ID derived from the given insecure passphrase",

		Args: cobra.ExactArgs(1),

		RunE: func(cmd *cobra.Command, args []string) error {
			privKey, err := gcmd.Libp2pKeyFromInsecurePassphrase("gordian-echo:network|", args[0])
			if err != nil {
				return fmt.Errorf("failed to generate libp2p network key: %w", err)
			}

			id, err := libp2ppeer.IDFromPrivateKey(privKey)
			if err != nil {
				return fmt.Errorf("failed to generate ID from libp2p private key: %w", err)
			}

			fmt.Fprintln(cmd.OutOrStdout(), id)

			return nil
		},
	}
}

func NewRunP2PRelayerCmd(log *slog.Logger) *cobra.Command {
	listenAddrs := []string{"/ip4/0.0.0.0/tcp/9999"}

	cmd := &cobra.Command{
		Use: "run-p2p-relayer INSECURE_PASSPHRASE",

		Short: "Run a p2p relayer with a fixed ID on a fixed address, to connect firewalled participants",

		Args: cobra.ExactArgs(1),

		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			netPrivKey, err := gcmd.Libp2pKeyFromInsecurePassphrase("gordian-echo:network|", args[0])
			if err != nil {
				return fmt.Errorf("failed to generate libp2p network key: %w", err)
			}

			h, err := tmlibp2p.NewHost(
				ctx,
				tmlibp2p.HostOptions{
					Options: []libp2p.Option{
						libp2p.Identity(netPrivKey),
						libp2p.ListenAddrStrings(listenAddrs...),

						// TODO: enable the relay service, and let the nodes remain "reachability private"?
						// libp2p.EnableRelayService(libp2prelayv2.WithInfiniteLimits()),

						// Unsure if this is something we always want.
						// Can be controlled by a flag later if undesirable by default.
						libp2p.ForceReachabilityPublic(),
					},
				},
			)
			if err != nil {
				return fmt.Errorf("failed to create libp2p host: %w", err)
			}

			defer func() {
				if err := h.Close(); err != nil {
					log.Warn("Error closing libp2p host", "err", err)
				}
			}()

			host := h.Libp2pHost()

			// We have to connect the relayer to the DHT,
			// so that peers can self-discover through the DHT.
			if _, err := dht.New(ctx, host,
				dht.ProtocolPrefix("/gordian"), // This must match the protocol prefix set in tmlibp2p.Connection.
			); err != nil {
				return fmt.Errorf("failed to create DHT peer for p2p-relayer: %w", err)
			}

			sub, err := host.EventBus().Subscribe(new(libp2pevent.EvtPeerConnectednessChanged))
			if err != nil {
				return err
			}
			defer sub.Close()

			loggingDone := make(chan struct{})
			go logPeerChanges(ctx, log, host, sub, loggingDone)

			log.Info("Listening for p2p connections", "id", host.ID(), "addrs", host.Addrs())
			log.Info("Press ^c to stop")

			<-ctx.Done()
			<-loggingDone

			return nil
		},
	}

	cmd.PersistentFlags().StringArrayVarP(&listenAddrs, "listen-multiaddr", "l", listenAddrs, "multiaddr to listen on")

	return cmd
}

func NewStandaloneMirrorCmd(log *slog.Logger) *cobra.Command {
	cmd := &cobra.Command{
		Use: "run-standalone-mirror PATH_TO_CONFIG_FILE",

		Short: "Run a standalone mirror to track the network state",

		Args: cobra.ExactArgs(1),

		RunE: func(cmd *cobra.Command, args []string) error {
			// We need a cancelable context if we fail partway through setup.
			// Be sure to defer cancel() after other deferred
			// close and cleanup calls, for types dependent on
			// a parent context cancellation.
			ctx, cancel := context.WithCancel(cmd.Context())
			defer cancel()

			jConfig, err := os.ReadFile(args[0])
			if err != nil {
				return err
			}

			var cfg echoConfig
			if err := json.Unmarshal(jConfig, &cfg); err != nil {
				return fmt.Errorf("failed to parse config file: %w", err)
			}

			if len(cfg.RemoteAddrs) == 0 {
				log.Warn("Config had no remote addresses set; relying on incoming connections to discover peers")
			}

			h, err := tmlibp2p.NewHost(
				ctx,
				tmlibp2p.HostOptions{
					Options: []libp2p.Option{
						// Not signing, so not setting libp2p.Identity -- allow system to generate a random ID.

						libp2p.ListenAddrStrings("/ip4/0.0.0.0/tcp/0"), // Hardcoded to an anonymous port for now.

						// Ideally we would provide a way to prefer using a relayer circuit,
						// but for demo and prototyping this will be fine.
						libp2p.ForceReachabilityPublic(),
					},
				},
			)
			if err != nil {
				return fmt.Errorf("failed to create libp2p host: %w", err)
			}

			defer func() {
				if err := h.Close(); err != nil {
					log.Warn("Error closing libp2p host", "err", err)
				}
			}()
			defer cancel()

			host := h.Libp2pHost()

			sub, err := host.EventBus().Subscribe(new(libp2pevent.EvtPeerConnectednessChanged))
			if err != nil {
				return err
			}
			defer sub.Close()

			loggingDone := make(chan struct{})
			go logPeerChanges(ctx, log, host, sub, loggingDone)
			defer func() {
				cancel()
				<-loggingDone
			}()

			log.Info("Listening", "id", host.ID(), "addrs", host.Addrs())

			for _, ra := range cfg.RemoteAddrs {
				ai, err := libp2ppeer.AddrInfoFromString(ra)
				if err != nil {
					return fmt.Errorf("failed to parse %q: %w", ra, err)
				}

				log.Info("Attempting connection", "remote_addr", ra)
				if err := host.Connect(ctx, *ai); err != nil {
					return fmt.Errorf("failed to connect to %v: %w", ai, err)
				}
			}

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

			bs := tmmemstore.NewBlockStore()
			ms := tmmemstore.NewMirrorStore()
			rs := tmmemstore.NewRoundStore()
			vs := tmmemstore.NewValidatorStore(tmconsensustest.SimpleHashScheme{})

			vals := make([]tmconsensus.Validator, len(cfg.ValidatorPubKeys))
			for i, pubKeyHex := range cfg.ValidatorPubKeys {
				pubKeyBytes, err := hex.DecodeString(pubKeyHex)
				if err != nil {
					return fmt.Errorf("failed to parse validator pubkey at index %d: %w", i, err)
				}

				pubKey, err := gcrypto.NewEd25519PubKey(pubKeyBytes)
				if err != nil {
					return fmt.Errorf("failed to build ed25519 public key from bytes: %w", err)
				}

				vals[i] = tmconsensus.Validator{
					PubKey: pubKey,
					Power:  1,
				}
			}

			m, err := tmengine.NewMirror(
				ctx,
				log.With("sys", "mirror"),
				tmengine.WithBlockStore(bs),
				tmengine.WithMirrorStore(ms),
				tmengine.WithRoundStore(rs),
				tmengine.WithValidatorStore(vs),

				tmengine.WithHashScheme(tmconsensustest.SimpleHashScheme{}),
				tmengine.WithSignatureScheme(tmconsensustest.SimpleSignatureScheme{}),
				tmengine.WithCommonMessageSignatureProofScheme(gcrypto.SimpleCommonMessageSignatureProofScheme),

				tmengine.WithGenesis(&tmconsensus.ExternalGenesis{
					ChainID:           "gordiandemo-echo",
					InitialHeight:     1,
					InitialAppState:   strings.NewReader(""), // No initial app state for identity app.
					GenesisValidators: vals,
				}),
			)
			if err != nil {
				return fmt.Errorf("failed to build mirror: %w", err)
			}
			defer m.Wait()

			const debugging = false
			if debugging {
				lh := tmdebug.LoggingConsensusHandler{
					Log: log.With("debug", "consensushandler"),
					Handler: tmconsensus.AcceptAllValidFeedbackMapper{
						Handler: tmdebug.LoggingFineGrainedConsensusHandler{
							Log:     log.With("debug", "fgchandler"),
							Handler: m,
						},
					},
				}
				conn.SetConsensusHandler(ctx, lh)
			} else {
				conn.SetConsensusHandler(ctx, tmconsensus.AcceptAllValidFeedbackMapper{
					Handler: m,
				})
			}

			log.Info("Starting mirror...")
			<-ctx.Done()
			log.Info("Shutting down...")

			return nil
		},
	}

	// TODO: will we set the listener addresses as a flag like in the echo validator command?

	return cmd
}

func NewFollowerCmd(log *slog.Logger) *cobra.Command {
	cmd := &cobra.Command{
		Use: "run-follower PATH_TO_CONFIG_FILE",

		Short: "Run a v0.3 follower (non-signing state machine)",

		Args: cobra.ExactArgs(1),

		RunE: func(cmd *cobra.Command, args []string) error {
			return runStateMachineV3(log, cmd, nil, nil, args[0])
		},
	}

	// TODO: will we set the listener addresses as a flag like in the echo validator command?

	return cmd
}

func NewRunEchoValidatorCmd(log *slog.Logger) *cobra.Command {
	listenAddrs := []string{"/ip4/0.0.0.0/tcp/8888"}

	cmd := &cobra.Command{
		Use: "run-echo-validator INSECURE_PASSPHRASE PATH_TO_CONFIG_FILE",

		Short: "Run a v0.3 validator with the echo application",

		Args: cobra.ExactArgs(2),

		RunE: func(cmd *cobra.Command, args []string) error {
			signer, err := gcmd.SignerFromInsecurePassphrase("gordian-echo|", args[0])
			if err != nil {
				return err
			}

			return runStateMachineV3(log, cmd, &signer, listenAddrs, args[1])
		},
	}

	cmd.PersistentFlags().StringArrayVarP(&listenAddrs, "listen-multiaddr", "l", listenAddrs, "multiaddr to listen on")

	return cmd
}

func runStateMachineV3(
	log *slog.Logger,
	cmd *cobra.Command,
	signer gcrypto.Signer,
	listenAddrs []string,
	configPath string,
) error {
	// We need a cancelable context if we fail partway through setup.
	// Be sure to defer cancel() after other deferred
	// close and cleanup calls, for types dependent on
	// a parent context cancellation.
	ctx, cancel := context.WithCancel(cmd.Context())
	defer cancel()

	// Just reassign ctx here because we will not have any further references to the root context,
	// other than explicit cancel calls to ensure clean shutdown.
	wd, ctx := gwatchdog.NewWatchdog(ctx, log.With("sys", "watchdog"))

	jConfig, err := os.ReadFile(configPath)
	if err != nil {
		return err
	}

	var cfg echoConfig
	if err := json.Unmarshal(jConfig, &cfg); err != nil {
		return fmt.Errorf("failed to parse config file: %w", err)
	}

	if len(cfg.RemoteAddrs) == 0 {
		log.Warn("Config had no remote addresses set; relying on incoming connections to discover peers")
	}

	h, err := tmlibp2p.NewHost(
		ctx,
		tmlibp2p.HostOptions{
			Options: []libp2p.Option{
				// Not signing, so not setting libp2p.Identity -- allow system to generate a random ID.

				libp2p.ListenAddrStrings(listenAddrs...),

				// Ideally we would provide a way to prefer using a relayer circuit,
				// but for demo and prototyping this will be fine.
				libp2p.ForceReachabilityPublic(),
			},
		},
	)
	if err != nil {
		return fmt.Errorf("failed to create libp2p host: %w", err)
	}

	defer func() {
		if err := h.Close(); err != nil {
			log.Warn("Error closing libp2p host", "err", err)
		}
	}()
	defer cancel()

	host := h.Libp2pHost()

	sub, err := host.EventBus().Subscribe(new(libp2pevent.EvtPeerConnectednessChanged))
	if err != nil {
		return err
	}
	defer sub.Close()

	loggingDone := make(chan struct{})
	go logPeerChanges(ctx, log, host, sub, loggingDone)
	defer func() {
		cancel()
		<-loggingDone
	}()

	log.Info("Listening", "id", host.ID(), "addrs", host.Addrs())

	for _, ra := range cfg.RemoteAddrs {
		ai, err := libp2ppeer.AddrInfoFromString(ra)
		if err != nil {
			return fmt.Errorf("failed to parse %q: %w", ra, err)
		}

		log.Info("Attempting connection", "remote_addr", ra)
		if err := host.Connect(ctx, *ai); err != nil {
			return fmt.Errorf("failed to connect to %v: %w", ai, err)
		}
	}

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

	vals := make([]tmconsensus.Validator, len(cfg.ValidatorPubKeys))
	for i, pubKeyHex := range cfg.ValidatorPubKeys {
		pubKeyBytes, err := hex.DecodeString(pubKeyHex)
		if err != nil {
			return fmt.Errorf("failed to parse validator pubkey at index %d: %w", i, err)
		}

		pubKey, err := gcrypto.NewEd25519PubKey(pubKeyBytes)
		if err != nil {
			return fmt.Errorf("failed to build ed25519 public key from bytes: %w", err)
		}

		vals[i] = tmconsensus.Validator{
			PubKey: pubKey,
			Power:  1,
		}
	}

	blockFinCh := make(chan tmdriver.FinalizeBlockRequest)
	initChainCh := make(chan tmdriver.InitChainRequest)

	app := newEchoApp(ctx, log.With("sys", "app_v0.3"), initChainCh, blockFinCh)
	defer app.Wait()
	defer cancel()

	cStrat := &echoConsensusStrategy{
		Log: log.With("sys", "cstrat"),
	}
	if signer != nil {
		// No pubkey set in follower mode.
		cStrat.PubKey = signer.PubKey()
	}

	gs := tmgossip.NewChattyStrategy(ctx, log.With("sys", "chattygossip"), conn)

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
			ChainID:           "gordiandemo-echo",
			InitialHeight:     1,
			InitialAppState:   strings.NewReader(""), // No initial app state for identity app.
			GenesisValidators: vals,
		}),

		tmengine.WithTimeoutStrategy(ctx, tmengine.LinearTimeoutStrategy{}),

		tmengine.WithBlockFinalizationChannel(blockFinCh),
		tmengine.WithInitChainChannel(initChainCh),

		tmengine.WithSigner(signer),

		tmengine.WithWatchdog(wd),
	)
	if err != nil {
		return fmt.Errorf("failed to build engine: %w", err)
	}
	defer e.Wait()

	const debugging = false
	if debugging {
		lh := tmdebug.LoggingConsensusHandler{
			Log: log.With("debug", "consensushandler"),
			Handler: tmconsensus.AcceptAllValidFeedbackMapper{
				Handler: tmdebug.LoggingFineGrainedConsensusHandler{
					Log:     log.With("debug", "fgchandler"),
					Handler: e,
				},
			},
		}
		conn.SetConsensusHandler(ctx, lh)
	} else {
		conn.SetConsensusHandler(ctx, tmconsensus.AcceptAllValidFeedbackMapper{
			Handler: e,
		})
	}

	if signer == nil {
		log.Info("Running follower v0.3 engine...")
	} else {
		log.Info("Running v0.3 engine...")
	}
	<-ctx.Done()
	log.Info("Shutting down...")

	return nil
}

func logPeerChanges(
	ctx context.Context,
	log *slog.Logger,
	_ libp2phost.Host,
	sub libp2pevent.Subscription,
	done chan<- struct{},
) {
	defer close(done)

	for {
		select {
		case <-ctx.Done():
			return

		case e := <-sub.Out():
			switch e := e.(type) {
			case libp2pevent.EvtPeerConnectednessChanged:
				log.Info(
					"Peer connectedness changed",
					"id", e.Peer,
					"connectedness", e.Connectedness,
				)
			default:
				log.Warn("Unknown event type", "type", fmt.Sprintf("%T", e))
			}
		}
	}
}
