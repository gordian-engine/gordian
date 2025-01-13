package main

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"net/rpc"
	"os"
	"os/signal"
	"slices"
	"strconv"
	"strings"
	"time"

	petname "github.com/dustinkirkland/golang-petname"
	"github.com/gordian-engine/gordian/cmd/gordian-stress/internal/gstress"
	"github.com/gordian-engine/gordian/cmd/internal/gcmd"
	"github.com/gordian-engine/gordian/gcrypto"
	"github.com/gordian-engine/gordian/gwatchdog"
	"github.com/gordian-engine/gordian/tm/tmcodec/tmjson"
	"github.com/gordian-engine/gordian/tm/tmconsensus"
	"github.com/gordian-engine/gordian/tm/tmconsensus/tmconsensustest"
	"github.com/gordian-engine/gordian/tm/tmdriver"
	"github.com/gordian-engine/gordian/tm/tmengine"
	"github.com/gordian-engine/gordian/tm/tmgossip"
	"github.com/gordian-engine/gordian/tm/tmp2p/tmlibp2p"
	"github.com/gordian-engine/gordian/tm/tmstore/tmmemstore"
	"github.com/libp2p/go-libp2p"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	libp2phost "github.com/libp2p/go-libp2p/core/host"
	libp2ppeer "github.com/libp2p/go-libp2p/core/peer"
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
		logger.Error("Failure", "err", err)
		os.Stderr.Sync()
		return err
	}

	return nil
}

func NewRootCmd(log *slog.Logger) *cobra.Command {
	rootCmd := &cobra.Command{
		Use: "gordian-stress SUBCOMMAND",

		CompletionOptions: cobra.CompletionOptions{HiddenDefaultCmd: true},

		Long: `gordian-stress is used for orchestration of fine-grained local gordian test networks.

Orchestration as in: the user provides a single configuration file describing the network,
and the tooling ensures all the described participants are started as independent processes.

Fine-grained as in: fine control of the network participants' configuration.
`,
	}

	rootCmd.AddCommand(
		newSeedCmd(log),
		newWaitForSeedCmd(log),
		newSeedAddrsCmd(log),
		newValidatorCmd(log),
		newRegisterValidatorCmd(log),
		newStartCmd(log),
		newHaltCmd(log),

		// Hidden command to generate the petname out of band.
		newPetnameCmd(),
	)

	return rootCmd
}

func newSeedCmd(log *slog.Logger) *cobra.Command {
	cmd := &cobra.Command{
		Use: "seed [PATH_TO_SOCKET_FILE=/var/run/gstress.$RANDOM_WORDS.$PID.sock]",

		Short: "Run a \"seed node\" that provides a central location for node discovery and coordination",

		Long: `seed runs a "seed node" that acts as a central location for:
- network participants to discover each other
- operator to discover dynamic addresses of each node
- operator to send various signals, such as a watchdog termination, to each node
`,

		Args: cobra.RangeArgs(0, 1),

		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithCancel(cmd.Context())
			defer cancel()

			h, err := tmlibp2p.NewHost(
				ctx,
				tmlibp2p.HostOptions{
					Options: []libp2p.Option{
						// Since unspecified, use a dynamic identity and a random listening address.
						// Specify only tcp for the protocol, just for simplicity in stack traces.
						libp2p.ListenAddrStrings("/ip4/0.0.0.0/tcp/0"),

						// Unsure if this is something we always want.
						// Can be controlled by a flag later if undesirable by default.
						libp2p.ForceReachabilityPublic(),
					},
				},
			)
			if err != nil {
				return fmt.Errorf("failed to create libp2p host: %w", err)
			}

			host := h.Libp2pHost()

			// We currently rely on the libp2p DHT for peer discovery,
			// so the seed node needs to run the DHT as well.
			// The validators connect to the DHT through the tmlibp2p.Connection type.
			if _, err := dht.New(ctx, host, dht.ProtocolPrefix("/gordian")); err != nil {
				return fmt.Errorf("failed to create DHT peer for seed: %w", err)
			}

			var socketPath string
			if len(args) == 0 {
				// petname package is still relying on old math/rand,
				// and requires an explicit seed.
				randName := petname.Generate(2, "-")

				// Unix sockets don't work on Windows anyway (right?)
				// so just directly make the path with forward slashes.
				socketPath = fmt.Sprintf("/var/run/gstress.%s.%d.sock", randName, os.Getpid())
			} else {
				socketPath = args[0]
			}

			hostInfo := libp2phost.InfoFromHost(host)
			p2pAddrs, err := libp2ppeer.AddrInfoToP2pAddrs(hostInfo)
			if err != nil {
				return fmt.Errorf("failed to get host p2p addrs: %w", err)
			}

			var hostAddrs []string
			for _, a := range p2pAddrs {
				hostAddrs = append(hostAddrs, a.String())
			}
			bh, err := gstress.NewBootstrapHost(ctx, log.With("sys", "bootstrap"), socketPath, hostAddrs)
			if err != nil {
				return fmt.Errorf("failed to initialize discovery host: %w", err)
			}
			defer bh.Wait()
			defer cancel()

			seedSvc := gstress.NewSeedService(log.With("svc", "seed"), host, bh)

			log.Info("Seed host ready", "id", host.ID().String(), "addrs", hostAddrs)

			go func() {
				const pollDur = 10 * time.Second
				timer := time.NewTimer(pollDur)
				defer timer.Stop()

				mm := make(map[string]tmengine.Metrics)
				for {
					select {
					case <-ctx.Done():
						return
					case <-timer.C:
						seedSvc.CopyMetrics(mm)
						if len(mm) < 2 {
							clear(mm)
							timer.Reset(pollDur)
							continue
						}

						// Slice of any, but the values are all attrs, to call log.Info.
						attrs := make([]any, 0, len(mm))
						for k, m := range mm {
							attrs = append(attrs, slog.Attr{Key: k, Value: m.LogValue()})
						}
						log.Info("Current metrics", attrs...)

						ms := make([]tmengine.Metrics, 0, len(mm))
						for _, m := range mm {
							ms = append(ms, m)
						}
						clear(mm)

						// Sort by the mirror voting values, ascending, first.
						slices.SortFunc(ms, func(a, b tmengine.Metrics) int {
							z := cmp.Compare(a.MirrorVotingHeight, b.MirrorVotingHeight)
							if z == 0 {
								z = cmp.Compare(a.MirrorVotingRound, b.MirrorVotingRound)
							}
							return z
						})

						const tolerance = 5

						// Now, if there is an arbitrary >5 difference in mirror voting height,
						// halt the network.
						vhDelta := ms[len(ms)-1].MirrorVotingHeight - ms[0].MirrorVotingHeight
						if vhDelta > tolerance {
							log.Warn("Halting due to mirror voting height difference", "delta", vhDelta)
							bh.Halt()
							go func() { time.Sleep(2 * time.Second); cancel() }()
							return
						}

						// If they are on the same height but there is a >5 round difference somehow,
						// that is also a halt.
						if vhDelta == 0 {
							vrDelta := ms[len(ms)-1].MirrorVotingRound - ms[0].MirrorVotingRound
							if vrDelta > tolerance {
								log.Warn(
									"Halting due to mirror voting round difference",
									"height", ms[0].MirrorVotingHeight,
									"round_delta", vrDelta,
								)
								bh.Halt()
								go func() { time.Sleep(2 * time.Second); cancel() }()
								return
							}
						}

						// One last halt check: has an individual validator
						// diverged on its state machine and round heights?
						for _, m := range ms {
							if delta := m.MirrorVotingHeight - m.StateMachineHeight; delta > tolerance {
								log.Warn(
									"Halting due to state machine and mirror voting height difference",
									"delta", delta,
								)
								bh.Halt()
								go func() { time.Sleep(2 * time.Second); cancel() }()
								return
							}
						}

					}

					timer.Reset(pollDur)
				}
			}()

			select {
			case <-ctx.Done():
				log.Info("Received ^c")
			case <-bh.Halted():
				log.Info("Received halt signal")
			}

			return nil
		},
	}

	return cmd
}

func newWaitForSeedCmd(log *slog.Logger) *cobra.Command {
	return &cobra.Command{
		Use: "wait-for-seed PATH_TO_SOCKET_FILE",

		Short: "Wait for the seed service to be listening at the given socket path",

		Args: cobra.ExactArgs(1),

		RunE: func(cmd *cobra.Command, args []string) error {
			socketPath := args[0]
			socketExists := false
			for range 200 {
				if _, err := os.Stat(socketPath); err != nil {
					// File wasn't ready.
					// 5ms sleep * 200 = checking for 1 total second.
					time.Sleep(5 * time.Millisecond)
					continue
				}
				socketExists = true
				break
			}

			if !socketExists {
				return fmt.Errorf("socket file at %s not ready within one second", socketPath)
			}

			return nil
		},
	}
}

func newSeedAddrsCmd(log *slog.Logger) *cobra.Command {
	cmd := &cobra.Command{
		Use: "seed-addrs PATH_TO_SOCKET_FILE",

		Short: "Read the libp2p seed addresses from the seed node's Unix socket",

		Args: cobra.ExactArgs(1),

		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := gstress.NewBootstrapClient(log, args[0])
			if err != nil {
				return fmt.Errorf("failed to create bootstrap client: %w", err)
			}

			addrs, err := c.SeedAddrs()
			if err != nil {
				return fmt.Errorf("failed to retrieve seed addresses: %w", err)
			}

			for _, addr := range addrs {
				// Logs go to stderr, but the read addresses go to stdout.
				fmt.Fprintln(cmd.OutOrStdout(), addr)
			}

			return nil
		},
	}

	return cmd
}

func newValidatorCmd(log *slog.Logger) *cobra.Command {
	cmd := &cobra.Command{
		Use: "validator BOOTSTRAP_SOCKET_FILE INSECURE_PASSPHRASE",

		Short: "Run a validator whose key is derived from the given insecure passphrase",

		Args: cobra.ExactArgs(2),

		RunE: func(cmd *cobra.Command, args []string) error {
			socketPath := args[0]
			insecurePassphrase := args[1]

			c, err := gstress.NewBootstrapClient(log.With("sys", "bootstrapclient"), socketPath)
			if err != nil {
				return fmt.Errorf("failed to create bootstrap client (make sure you ran 'gordian-stress seed' first): %w", err)
			}

			// Need the seed addresses to connect to the seed RPC soon.
			seedAddrs, err := c.SeedAddrs()
			if err != nil {
				return fmt.Errorf("failed to retrieve seed addresses (make sure you ran 'gordian-stress seed' first): %w", err)
			}

			signer, err := gcmd.SignerFromInsecurePassphrase("gordian-stress|", insecurePassphrase)
			if err != nil {
				return fmt.Errorf("failed to build signer from insecure passphrase: %w", err)
			}

			// Log a warning if our key is not in the list.
			curVals, err := c.Validators()
			if err != nil {
				return fmt.Errorf("failed to get validators from seed: %w", err)
			}
			isRegistered := false
			for _, v := range curVals {
				if v.PubKey.Equal(signer.PubKey()) {
					isRegistered = true
					break
				}
			}
			if !isRegistered {
				log.Warn("This validator is not registered; if unintentional, run 'gordian-stress register-validator' to include this validator in genesis")
			}

			ctx := cmd.Context()

			netPrivKey, err := gcmd.Libp2pKeyFromInsecurePassphrase("gordian-stress:network|", insecurePassphrase)
			if err != nil {
				return fmt.Errorf("failed to generate libp2p network key: %w", err)
			}

			h, err := tmlibp2p.NewHost(
				ctx,
				tmlibp2p.HostOptions{
					Options: []libp2p.Option{
						libp2p.Identity(netPrivKey),

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

			// Connect to the first possible seed address.
			var connectedID libp2ppeer.ID
			for _, sa := range seedAddrs {
				ai, err := libp2ppeer.AddrInfoFromString(sa)
				if err != nil {
					log.Warn("Failed to parse seed address", "addr", sa, "err", err)
					continue
				}

				if err := h.Libp2pHost().Connect(ctx, *ai); err != nil {
					log.Warn("Failed to connect to seed address", "addr", sa, "err", err)
					continue
				}

				connectedID = ai.ID
				break
			}
			if connectedID == "" {
				return fmt.Errorf("failed to connect to any of %d seed address(es)", len(seedAddrs))
			}

			// Now that we are connected over libp2p, open the seed RPC.
			seedRPCStream, err := h.Libp2pHost().NewStream(
				ctx, connectedID, gstress.SeedServiceProtocolID,
			)
			if err != nil {
				return fmt.Errorf("failed to open seed stream: %w", err)
			}
			rpcClient := rpc.NewClient(seedRPCStream)

			log.Info("Awaiting genesis...")
			resp := new(gstress.RPCGenesisResponse)
			select {
			case <-ctx.Done():
				return context.Cause(ctx)

			case call := <-rpcClient.Go("SeedRPC.Genesis", gstress.RPCGenesisRequest{}, resp, nil).Done:
				if call.Error != nil {
					return fmt.Errorf("error during Genesis RPC: %w", err)
				}
				// Otherwise, resp has been populated.
			}

			// Ensure we are on a valid app.
			if resp.App != "echo" {
				return fmt.Errorf("unsupported app %q", resp.App)
			}

			return runStateMachine(log, cmd, h, signer, *resp, rpcClient)
		},
	}

	return cmd
}

func newRegisterValidatorCmd(log *slog.Logger) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "register-validator PATH_TO_SOCKET_FILE INSECURE_PASSPHRASE VOTE_POWER",
		Aliases: []string{"rv"},

		Short: "Register a validator with the seed node",

		Args: cobra.ExactArgs(3),

		RunE: func(cmd *cobra.Command, args []string) error {
			socketPath := args[0]
			insecurePassphrase := args[1]

			pow, err := strconv.ParseUint(args[2], 10, 64)
			if err != nil {
				return fmt.Errorf("failed to parse vote power: %w", err)
			}

			signer, err := gcmd.SignerFromInsecurePassphrase("gordian-stress|", insecurePassphrase)
			if err != nil {
				return fmt.Errorf("failed to build signer from insecure passphrase: %w", err)
			}

			c, err := gstress.NewBootstrapClient(log, socketPath)
			if err != nil {
				return fmt.Errorf("failed to create bootstrap client: %w", err)
			}

			return c.RegisterValidator(tmconsensus.Validator{
				PubKey: signer.PubKey(),
				Power:  pow,
			})
		},
	}

	return cmd
}

func newStartCmd(log *slog.Logger) *cobra.Command {
	cmd := &cobra.Command{
		Use: "start PATH_TO_SOCKET_FILE",

		Short: "Tell the seed node to start the chain with all connected validators",

		Args: cobra.ExactArgs(1),

		RunE: func(cmd *cobra.Command, args []string) error {
			socketPath := args[0]

			c, err := gstress.NewBootstrapClient(log, socketPath)
			if err != nil {
				return fmt.Errorf("failed to create bootstrap client: %w", err)
			}

			return c.Start()
		},
	}

	return cmd
}

func runStateMachine(
	log *slog.Logger,
	cmd *cobra.Command,
	h *tmlibp2p.Host,
	signer gcrypto.Signer,
	seedGenesis gstress.RPCGenesisResponse,
	rpcClient *rpc.Client,
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

	go func() {
		haltCall := rpcClient.Go("SeedRPC.AwaitHalt", gstress.RPCHaltRequest{}, new(gstress.RPCHaltResponse), nil)
		select {
		case <-ctx.Done():
			// Nothing to do.
			return
		case <-haltCall.Done:
			wd.Terminate("received halt signal from seed")
		}
	}()

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

	chs := tmmemstore.NewCommittedHeaderStore()
	fs := tmmemstore.NewFinalizationStore()
	ms := tmmemstore.NewMirrorStore()
	rs := tmmemstore.NewRoundStore()
	vs := tmmemstore.NewValidatorStore(tmconsensustest.SimpleHashScheme{})

	blockFinCh := make(chan tmdriver.FinalizeBlockRequest)
	initChainCh := make(chan tmdriver.InitChainRequest)

	app := gcmd.NewEchoApp(ctx, log.With("app", "echo"), initChainCh, blockFinCh)
	defer app.Wait()
	defer cancel()

	var signerPubKey gcrypto.PubKey
	if signer != nil {
		signerPubKey = signer.PubKey()
	}
	cStrat := gcmd.NewEchoConsensusStrategy(log.With("sys", "cstrat"), signerPubKey)

	gs := tmgossip.NewChattyStrategy(ctx, log.With("sys", "chattygossip"), conn)

	metricsCh := make(chan tmengine.Metrics)

	valSet, err := tmconsensus.NewValidatorSet(seedGenesis.Validators, tmconsensustest.SimpleHashScheme{})
	if err != nil {
		return fmt.Errorf("failed to build validator set for genesis: %w", err)
	}

	e, err := tmengine.New(
		ctx,
		log.With("sys", "engine"),
		tmengine.WithActionStore(as),
		tmengine.WithCommittedHeaderStore(chs),
		tmengine.WithFinalizationStore(fs),
		tmengine.WithMirrorStore(ms),
		tmengine.WithRoundStore(rs),
		tmengine.WithValidatorStore(vs),

		tmengine.WithHashScheme(tmconsensustest.SimpleHashScheme{}),
		tmengine.WithSignatureScheme(tmconsensustest.SimpleSignatureScheme{}),
		tmengine.WithCommonMessageSignatureProofScheme(gcrypto.SimpleCommonMessageSignatureProofScheme{}),

		tmengine.WithConsensusStrategy(cStrat),
		tmengine.WithGossipStrategy(gs),

		tmengine.WithGenesis(&tmconsensus.ExternalGenesis{
			ChainID:             seedGenesis.App,
			InitialHeight:       1,
			InitialAppState:     strings.NewReader(""), // No initial app state for echo app.
			GenesisValidatorSet: valSet,
		}),

		tmengine.WithTimeoutStrategy(ctx, tmengine.LinearTimeoutStrategy{}),

		tmengine.WithBlockFinalizationChannel(blockFinCh),
		tmengine.WithInitChainChannel(initChainCh),

		tmengine.WithSigner(tmconsensus.PassthroughSigner{
			Signer:          signer,
			SignatureScheme: tmconsensustest.SimpleSignatureScheme{},
		}),

		tmengine.WithMetricsChannel(metricsCh),

		tmengine.WithWatchdog(wd),
	)
	if err != nil {
		return fmt.Errorf("failed to build engine: %w", err)
	}

	// Periodically publish the metrics.
	go func() {
		timer := time.NewTimer(5 * time.Second)
		defer timer.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:

				// Quick check for metrics,
				// only publish if they can be immediately read.
				select {
				case m := <-metricsCh:
					// Attempt to synchronously publish, discard errors.
					_ = rpcClient.Call(
						"SeedRPC.PublishMetrics",
						gstress.RPCPublishMetricsRequest{Metrics: m},
						new(gstress.RPCPublishMetricsResponse),
					)
				default:
					// Nothing.
				}

			}
			timer.Reset(5 * time.Second)
		}
	}()

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

func newHaltCmd(log *slog.Logger) *cobra.Command {
	cmd := &cobra.Command{
		Use: "halt PATH_TO_SOCKET_FILE",

		Short: "Tell the seed node and its connected validators to halt",

		Args: cobra.ExactArgs(1),

		RunE: func(cmd *cobra.Command, args []string) error {
			socketPath := args[0]

			c, err := gstress.NewBootstrapClient(log, socketPath)
			if err != nil {
				return fmt.Errorf("failed to create bootstrap client: %w", err)
			}

			return c.Halt()
		},
	}

	return cmd
}

func newPetnameCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "petname",
		Hidden: true,
		Args:   cobra.NoArgs,
		Run: func(cmd *cobra.Command, _ []string) {
			fmt.Fprintln(cmd.OutOrStdout(), petname.Generate(2, "-"))
		},
	}
}
