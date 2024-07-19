package gserver

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"

	"cosmossdk.io/core/transaction"
	cosmoslog "cosmossdk.io/log"
	serverv2 "cosmossdk.io/server/v2"
	"cosmossdk.io/store/v2/root"
	"github.com/cometbft/cometbft/privval"
	"github.com/cosmos/cosmos-sdk/client"
	"github.com/libp2p/go-libp2p"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	libp2phost "github.com/libp2p/go-libp2p/core/host"
	libp2ppeer "github.com/libp2p/go-libp2p/core/peer"
	"github.com/rollchains/gordian/gcosmos/gserver/internal/gsi"
	"github.com/rollchains/gordian/gcrypto"
	"github.com/rollchains/gordian/gwatchdog"
	"github.com/rollchains/gordian/tm/tmcodec/tmjson"
	"github.com/rollchains/gordian/tm/tmconsensus"
	"github.com/rollchains/gordian/tm/tmconsensus/tmconsensustest"
	"github.com/rollchains/gordian/tm/tmdriver"
	"github.com/rollchains/gordian/tm/tmengine"
	"github.com/rollchains/gordian/tm/tmgossip"
	"github.com/rollchains/gordian/tm/tmp2p/tmlibp2p"
	"github.com/rollchains/gordian/tm/tmstore"
	"github.com/rollchains/gordian/tm/tmstore/tmmemstore"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

// The various interfaces we expect a Component to satisfy.
var (
	_ serverv2.ServerComponent[transaction.Tx] = (*Component[transaction.Tx])(nil)
)

// Component is a server component to be injected into the Cosmos SDK server module.
type Component[T transaction.Tx] struct {
	rootCtx context.Context
	cancel  context.CancelCauseFunc

	log *slog.Logger

	app serverv2.AppI[T]

	signer gcrypto.Signer

	// Partially set up during Init,
	// then used during Start.
	opts []tmengine.Opt

	// Configured during Start, and needs a clean shutdown during Stop.
	h      *tmlibp2p.Host
	conn   *tmlibp2p.Connection
	e      *tmengine.Engine
	driver *gsi.Driver[T]

	seedAddrs string

	httpLn     net.Listener
	ms         tmstore.MirrorStore
	vs         tmstore.ValidatorStore
	rs         tmstore.RoundStore
	fs         tmstore.FinalizationStore
	bs         tmstore.BlockStore
	httpServer *gsi.HTTPServer
}

// NewComponent returns a new server component
// ready to be supplied to the Cosmos SDK server module.
//
// It accepts a *slog.Logger directly to avoid dealing with SDK loggers.
func NewComponent[T transaction.Tx](
	rootCtx context.Context,
	log *slog.Logger,
) (*Component[T], error) {
	var c Component[T]
	c.rootCtx, c.cancel = context.WithCancelCause(rootCtx)
	c.log = log.With("sys", "engine")

	return &c, nil
}

func (c *Component[T]) Name() string {
	return "gordian"
}

func (c *Component[T]) Start(ctx context.Context) error {
	h, err := tmlibp2p.NewHost(
		c.rootCtx,
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
	c.h = h

	c.log.Info("Started libp2p host", "id", h.Libp2pHost().ID().String())

	for _, seedAddr := range strings.Split(c.seedAddrs, "\n") {
		if seedAddr == "" {
			// If c.seedAddrs was empty, skip so we don't log a misleading warning.
			continue
		}

		ai, err := libp2ppeer.AddrInfoFromString(seedAddr)
		if err != nil {
			c.log.Warn("Failed to parse seed address", "addr", seedAddr, "err", err)
			continue
		}

		if err := h.Libp2pHost().Connect(ctx, *ai); err != nil {
			c.log.Warn("Failed to connect to seed address", "addr", seedAddr, "err", err)
			continue
		}
	}

	reg := new(gcrypto.Registry)
	gcrypto.RegisterEd25519(reg)
	codec := tmjson.MarshalCodec{
		CryptoRegistry: reg,
	}
	conn, err := tmlibp2p.NewConnection(
		c.rootCtx,
		c.log.With("sys", "libp2pconn"),
		h,
		codec,
	)
	if err != nil {
		return fmt.Errorf("failed to build libp2p connection: %w", err)
	}
	c.conn = conn

	initChainCh := make(chan tmdriver.InitChainRequest)
	blockFinCh := make(chan tmdriver.FinalizeBlockRequest)
	d, err := gsi.NewDriver(
		c.rootCtx,
		ctx,
		c.log.With("serversys", "driver"),
		gsi.DriverConfig[T]{
			ConsensusAuthority: c.app.GetConsensusAuthority(),

			AppManager: c.app.GetAppManager(),

			Store: c.app.GetStore().(*root.Store),

			InitChainRequests:     initChainCh,
			FinalizeBlockRequests: blockFinCh,
		},
	)
	if err != nil {
		return fmt.Errorf("failed to create driver: %w", err)
	}
	c.driver = d

	opts := c.opts
	c.opts = nil // Don't need to reference the slice after this, so allow it to be GCed.

	// Extra options that we couldn't set earlier for whatever reason:

	opts = append(opts, tmengine.WithBlockFinalizationChannel(blockFinCh))

	// We needed the driver before we could make the consensus strategy.
	opts = append(opts, tmengine.WithConsensusStrategy(
		gsi.NewConsensusStrategy(c.log.With("serversys", "cons_strat"), d, c.signer),
	))

	// Depends on conn.
	gs := tmgossip.NewChattyStrategy(ctx, c.log.With("sys", "chattygossip"), conn)
	opts = append(opts, tmengine.WithGossipStrategy(gs))

	// No point in creating this channel before a call to Start.
	opts = append(opts, tmengine.WithInitChainChannel(initChainCh))

	// Could be sooner but it's easier to just take this context late here.
	wd, wdCtx := gwatchdog.NewWatchdog(c.rootCtx, c.log.With("sys", "watchdog"))
	opts = append(opts, tmengine.WithWatchdog(wd))

	// The timeout strategy pairs with a context,
	// so it makes sense to delay this until we have a watchdog context available.
	opts = append(opts, tmengine.WithTimeoutStrategy(wdCtx, tmengine.LinearTimeoutStrategy{}))

	e, err := tmengine.New(wdCtx, c.log.With("sys", "engine"), opts...)
	if err != nil {
		return fmt.Errorf("failed to start engine: %w", err)
	}
	c.e = e

	// Plain context here; if canceled, this will fail, which is fine.
	conn.SetConsensusHandler(ctx, tmconsensus.AcceptAllValidFeedbackMapper{
		Handler: e,
	})

	if c.httpLn != nil {
		c.httpServer = gsi.NewHTTPServer(ctx, c.log.With("sys", "http"), gsi.HTTPServerConfig{
			Listener:          c.httpLn,
			MirrorStore:       c.ms,
			Libp2pHost:        c.h,
			Libp2pconn:        c.conn,
			ValidatorStore:    c.vs,
			RoundStore:        c.rs,
			FinalizationStore: c.fs,
			BlockStore:        c.bs,
		})
	}

	return nil
}

func (c *Component[T]) Stop(_ context.Context) error {
	c.cancel(errors.New("stopped via SDK server module"))
	if c.e != nil {
		c.e.Wait()
	}
	if c.driver != nil {
		c.driver.Wait()
	}
	if c.conn != nil {
		c.conn.Disconnect()
	}
	if c.h != nil {
		if err := c.h.Close(); err != nil {
			c.log.Warn("Error closing tmp2p host", "err", err)
		}
	}
	if c.httpLn != nil {
		if err := c.httpLn.Close(); err != nil {
			c.log.Warn("Error closing HTTP listener", "err", err)
		}
		if c.httpServer != nil {
			c.httpServer.Wait()
		}
	}
	return nil
}

func (c *Component[T]) Init(app serverv2.AppI[T], v *viper.Viper, log cosmoslog.Logger) error {
	if c.log == nil {
		l, ok := log.Impl().(*slog.Logger)
		if !ok {
			return errors.New("(*gserver.Component).Init: log must be set during gserver.NewServerModule, or Init log must be implemented by *slog.Logger")
		}
		c.log = l
	}

	// Maybe set up the HTTP server.
	if httpAddr := v.GetString(httpAddrFlag); httpAddr != "" {
		ln, err := net.Listen("tcp", httpAddr)
		if err != nil {
			return fmt.Errorf("failed to listen for HTTP on %q: %w", httpAddr, err)
		}

		if f := v.GetString(httpAddrFileFlag); f != "" {
			// TODO: we should probably track this file and delete it on shutdown.
			addr := ln.Addr().String() + "\n"
			if err := os.WriteFile(f, []byte(addr), 0600); err != nil {
				return fmt.Errorf("failed to write HTTP address to file %q: %w", f, err)
			}
		}

		c.httpLn = ln
	}

	c.seedAddrs = v.GetString(seedAddrsFlag)
	if c.seedAddrs == "" {
		c.log.Warn("No seed addresses provided; relying on incoming connections to discover peers")
	}

	c.app = app

	// Load the comet config, in order to read the privval key from disk.
	// We don't really care about the state file,
	// but we need to to call LoadFilePV,
	// to get to the FilePVKey,
	// which gives us the PrivKey.
	cometConfig := client.GetConfigFromViper(v)
	fpv := privval.LoadFilePV(cometConfig.PrivValidatorKeyFile(), cometConfig.PrivValidatorStateFile())
	privKey := fpv.Key.PrivKey
	if privKey.Type() != "ed25519" {
		panic(fmt.Errorf(
			"gcosmos only understands ed25519 signing keys; got %q",
			privKey.Type(),
		))
	}

	// TODO: we should allow a way to explicitly NOT provide a signer.
	c.signer = gcrypto.NewEd25519Signer(ed25519.PrivateKey(privKey.Bytes()))

	var as *tmmemstore.ActionStore
	if c.signer != nil {
		as = tmmemstore.NewActionStore()
	}

	bs := tmmemstore.NewBlockStore()
	fs := tmmemstore.NewFinalizationStore()
	ms := tmmemstore.NewMirrorStore()
	rs := tmmemstore.NewRoundStore()
	vs := tmmemstore.NewValidatorStore(tmconsensustest.SimpleHashScheme{})

	c.bs = bs
	c.fs = fs
	c.ms = ms
	c.rs = rs
	c.vs = vs

	const chainID = "TODO:TEMPORARY_CHAIN_ID" // TODO: need to get this from the SDK.

	c.opts = []tmengine.Opt{
		tmengine.WithSigner(c.signer),

		tmengine.WithActionStore(as),
		tmengine.WithBlockStore(bs),
		tmengine.WithFinalizationStore(fs),
		tmengine.WithMirrorStore(ms),
		tmengine.WithRoundStore(rs),
		tmengine.WithValidatorStore(vs),

		tmengine.WithHashScheme(tmconsensustest.SimpleHashScheme{}),
		tmengine.WithSignatureScheme(tmconsensustest.SimpleSignatureScheme{}),
		tmengine.WithCommonMessageSignatureProofScheme(gcrypto.SimpleCommonMessageSignatureProofScheme),

		tmengine.WithGenesis(genesis),

		// NOTE: there are remaining required options that we shouldn't initialize here,
		// but instead they will be added during the Start call.
	}

	return nil
}

const (
	httpAddrFlag     = "g-http-addr"
	httpAddrFileFlag = "g-http-addr-file"

	seedAddrsFlag = "g-seed-addrs"
)

func (c *Component[T]) StartCmdFlags() *pflag.FlagSet {
	flags := pflag.NewFlagSet("gserver", pflag.ExitOnError)

	flags.String(httpAddrFlag, "", "TCP address of Gordian's introspective HTTP server; if blank, server will not be started")
	flags.String(httpAddrFileFlag, "", "Write the actual Gordian HTTP listen address to the given file (useful for tests when configured to listen on :0)")

	flags.String(seedAddrsFlag, "", "Newline-separated multiaddrs to connect to; if omitted, relies on incoming connections to discover peers")

	return flags
}

// WriteCustomConfigAt satisfies an undocumented interface,
// and here we emulate what Comet does in order to get past some error expecting this file to exist.
func (c *Component[T]) WriteCustomConfigAt(configPath string) error {
	f, err := os.Create(filepath.Join(configPath, "config.toml"))
	if err != nil {
		return fmt.Errorf("could not create empty config file: %w", err)
	}
	return f.Close()
}

func (c *Component[T]) CLICommands() serverv2.CLIConfig {
	return serverv2.CLIConfig{
		Commands: []*cobra.Command{newSeedCommand()},
	}
}

func newSeedCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "seed",
		Short: `Run a "seed node" as a central discovery point for other libp2p nodes`,
		Args:  cobra.ExactArgs(1),

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

			hostInfo := libp2phost.InfoFromHost(host)
			p2pAddrs, err := libp2ppeer.AddrInfoToP2pAddrs(hostInfo)
			if err != nil {
				return fmt.Errorf("failed to get host p2p addrs: %w", err)
			}

			var hostAddrs []string
			for _, a := range p2pAddrs {
				hostAddrs = append(hostAddrs, a.String())
			}

			joinedAddrs := strings.Join(hostAddrs, "\n") + "\n" // Trailing newline indicates proper end of input.

			if err := os.WriteFile(args[0], []byte(joinedAddrs), 0600); err != nil {
				return fmt.Errorf("failed to write seed address output file %q: %w", args[0], err)
			}

			fmt.Fprintf(cmd.ErrOrStderr(), "Seed running at multiaddrs: %s\n", joinedAddrs)

			return nil
		},
	}
}
