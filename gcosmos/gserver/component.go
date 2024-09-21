package gserver

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
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
	cometconfig "github.com/cometbft/cometbft/config"
	"github.com/cometbft/cometbft/privval"
	"github.com/cosmos/cosmos-sdk/codec"
	"github.com/libp2p/go-libp2p"
	libp2pevent "github.com/libp2p/go-libp2p/core/event"
	libp2pnetwork "github.com/libp2p/go-libp2p/core/network"
	libp2ppeer "github.com/libp2p/go-libp2p/core/peer"
	"github.com/rollchains/gordian/gcosmos/gcstore"
	"github.com/rollchains/gordian/gcosmos/gcstore/gcmemstore"
	"github.com/rollchains/gordian/gcosmos/gserver/internal/ggrpc"
	"github.com/rollchains/gordian/gcosmos/gserver/internal/gp2papi"
	"github.com/rollchains/gordian/gcosmos/gserver/internal/gsbd"
	"github.com/rollchains/gordian/gcosmos/gserver/internal/gsi"
	"github.com/rollchains/gordian/gcrypto"
	"github.com/rollchains/gordian/gdriver/gtxbuf"
	"github.com/rollchains/gordian/gexternalsigner"
	"github.com/rollchains/gordian/gwatchdog"
	"github.com/rollchains/gordian/tm/tmcodec/tmjson"
	"github.com/rollchains/gordian/tm/tmconsensus"
	"github.com/rollchains/gordian/tm/tmconsensus/tmconsensustest"
	"github.com/rollchains/gordian/tm/tmdriver"
	"github.com/rollchains/gordian/tm/tmengine"
	"github.com/rollchains/gordian/tm/tmengine/tmelink"
	"github.com/rollchains/gordian/tm/tmgossip"
	"github.com/rollchains/gordian/tm/tmp2p/tmlibp2p"
	"github.com/rollchains/gordian/tm/tmstore"
	"github.com/rollchains/gordian/tm/tmstore/tmmemstore"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

//go:generate go run github.com/rollchains/gordian/gassert/cmd/generate-nodebug component_debug.go

// The various interfaces we expect a Component to satisfy.
var (
	_ serverv2.ServerComponent[transaction.Tx] = (*Component)(nil)
	_ serverv2.HasStartFlags                   = (*Component)(nil)
)

// Component is a server component to be injected into the Cosmos SDK server module.
type Component struct {
	rootCtx context.Context
	cancel  context.CancelCauseFunc

	log *slog.Logger

	chainID string

	app   serverv2.AppI[transaction.Tx]
	txc   transaction.Codec[transaction.Tx]
	codec codec.Codec

	signer tmconsensus.Signer

	// Partially set up during Init,
	// then used during Start.
	opts []tmengine.Opt

	// Configured during Start, and needs a clean shutdown during Stop.
	h      *tmlibp2p.Host
	conn   *tmlibp2p.Connection
	e      *tmengine.Engine
	driver *gsi.Driver
	cStrat *gsi.ConsensusStrategy
	dh     *gp2papi.DataHost

	seedAddrs string

	httpLn net.Listener
	grpcLn net.Listener

	bds        gcstore.BlockDataStore
	hs         tmstore.HeaderStore
	fs         tmstore.FinalizationStore
	ms         tmstore.MirrorStore
	httpServer *gsi.HTTPServer
	grpcServer *ggrpc.GordianGRPC
}

// NewComponent returns a new server component
// ready to be supplied to the Cosmos SDK server module.
//
// It accepts a *slog.Logger directly to avoid dealing with SDK loggers.
func NewComponent(
	rootCtx context.Context,
	log *slog.Logger,
	txc transaction.Codec[transaction.Tx],
	codec codec.Codec,
) (*Component, error) {
	var c Component
	c.rootCtx, c.cancel = context.WithCancelCause(rootCtx)
	c.log = log.With("sys", "engine")
	c.txc = txc
	c.codec = codec

	return &c, nil
}

func (c *Component) Name() string {
	return "gordian"
}

// Init is called early in the SDK server component lifecycle, before Start.
func (c *Component) Init(app serverv2.AppI[transaction.Tx], cfg map[string]any, log cosmoslog.Logger) error {
	if c.log == nil {
		l, ok := log.Impl().(*slog.Logger)
		if !ok {
			return errors.New("(*gserver.Component).Init: log must be set during gserver.NewServerModule, or Init log must be implemented by *slog.Logger")
		}
		c.log = l
	}

	// It's somewhat likely that a user could misconfigure the assertion rules in a debug build,
	// so check those before doing any other heavy lifting.
	assertOpt, err := getAssertEngineOpt(cfg)
	if err != nil {
		return fmt.Errorf("failed to build assertion environment: %w", err)
	}

	// Maybe set up the HTTP server.
	if httpAddr, ok := cfg[httpAddrFlag].(string); ok && httpAddr != "" {
		ln, err := net.Listen("tcp", httpAddr)
		if err != nil {
			return fmt.Errorf("failed to listen for HTTP on %q: %w", httpAddr, err)
		}

		if f, ok := cfg[httpAddrFileFlag].(string); ok && f != "" {
			// TODO: we should probably track this file and delete it on shutdown.
			addr := ln.Addr().String() + "\n"
			if err := os.WriteFile(f, []byte(addr), 0600); err != nil {
				return fmt.Errorf("failed to write HTTP address to file %q: %w", f, err)
			}
		}

		c.httpLn = ln
	}

	// Maybe set up the GRPC server.
	if grpcAddrFlag, ok := cfg[grpcAddrFlag].(string); ok && grpcAddrFlag != "" {
		ln, err := net.Listen("tcp", grpcAddrFlag)
		if err != nil {
			return fmt.Errorf("failed to listen for gRPC on %q: %w", grpcAddrFlag, err)
		}

		c.grpcLn = ln
	}

	if sa, ok := cfg[seedAddrsFlag].(string); ok {
		c.seedAddrs = sa
	}
	if c.seedAddrs == "" {
		c.log.Warn("No seed addresses provided; relying on incoming connections to discover peers")
	}

	c.app = app

	// Load the comet config, in order to read the privval key from disk.
	// We don't really care about the state file,
	// but we need to to call LoadFilePV,
	// to get to the FilePVKey,
	// which gives us the PrivKey.
	homeDir := cfg["home"].(string)
	cometConfig := cometconfig.DefaultConfig().SetRoot(homeDir)
	if err := serverv2.UnmarshalSubConfig(cfg, "", &cometConfig); err != nil {
		return fmt.Errorf("failed to unmarshal comet config (to get private key info): %w", err)
	}

	fpv := privval.LoadFilePV(cometConfig.PrivValidatorKeyFile(), cometConfig.PrivValidatorStateFile())
	privKey := fpv.Key.PrivKey
	if privKey.Type() != "ed25519" {
		panic(fmt.Errorf(
			"gcosmos only understands ed25519 signing keys; got %q",
			privKey.Type(),
		))
	}

	// TODO: we should allow a way to explicitly NOT provide a signer.
	if extSignerAddr, ok := cfg[extSignerAddrFlag].(string); ok && extSignerAddr != "" {
		c.signer, err = gexternalsigner.NewExternalSigner(extSignerAddr)
		if err != nil {
			panic(fmt.Errorf("failed to create external signer: %s: %w", extSignerAddr, err))
		}
	} else {
		c.signer = tmconsensus.PassthroughSigner{
			Signer:          gcrypto.NewEd25519Signer(ed25519.PrivateKey(privKey.Bytes())),
			SignatureScheme: tmconsensustest.SimpleSignatureScheme{},
		}
	}

	var as *tmmemstore.ActionStore
	if c.signer != nil {
		as = tmmemstore.NewActionStore()
	}

	bds := gcmemstore.NewBlockDataStore()
	fs := tmmemstore.NewFinalizationStore()
	hs := tmmemstore.NewHeaderStore()
	ms := tmmemstore.NewMirrorStore()
	rs := tmmemstore.NewRoundStore()
	vs := tmmemstore.NewValidatorStore(tmconsensustest.SimpleHashScheme{})

	c.bds = bds
	c.fs = fs
	c.hs = hs
	c.ms = ms

	// Is it possible for the genesis path to ever be rooted somewhere else?
	genesisPath := filepath.Join(homeDir, "config", "genesis.json")
	gf, err := os.Open(genesisPath)
	if err != nil {
		return fmt.Errorf("failed to open genesis file to extract chain ID: %w", err)
	}
	defer gf.Close()

	var cid struct {
		ChainID string `json:"chain_id"`
	}
	if err := json.NewDecoder(gf).Decode(&cid); err != nil {
		return fmt.Errorf("failed to parse JSON from genesis file at %s: %w", genesisPath, err)
	}
	// Even though we have a defer above, close it explicitly now that we are done parsing.
	_ = gf.Close()

	// Store the chain ID on the component, because the driver needs it during Start.
	c.chainID = cid.ChainID

	genesis := &tmconsensus.ExternalGenesis{
		ChainID:         cid.ChainID,
		InitialHeight:   1,
		InitialAppState: strings.NewReader(""), // No initial app state yet.
		// TODO: where will GenesisValidators come from?
	}

	c.opts = []tmengine.Opt{
		tmengine.WithSigner(c.signer),

		tmengine.WithActionStore(as),
		tmengine.WithFinalizationStore(fs),
		tmengine.WithHeaderStore(hs),
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
	if assertOpt != nil {
		// Will always be nil in non-debug builds.
		c.opts = append(c.opts, assertOpt)
	}

	return nil
}

// Start is called when the SDK is starting server components.
func (c *Component) Start(ctx context.Context) error {
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

	// TODO: allow c.dh to be conditionally set,
	// instead of unconditionally assigning it.
	c.dh = gp2papi.NewDataHost(
		c.rootCtx,
		c.log.With("sys", "datahost"),
		h.Libp2pHost(),
		c.hs,
		c.bds,
		codec,
	)

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

	am := *(c.app.GetAppManager())

	txm := gsi.TxManager{AppManager: am}
	txBuf := gtxbuf.New(
		ctx, c.log.With("d_sys", "tx_buffer"),
		txm.AddTx, txm.TxDeleterFunc,
	)

	bdrCache := gsbd.NewRequestCache()

	rhCh := make(chan tmelink.ReplayedHeaderRequest)
	catchupClient := gp2papi.NewCatchupClient(
		ctx,
		c.log.With("d_sys", "catchup_client"),
		gp2papi.CatchupClientConfig{
			Host:               h.Libp2pHost(),
			Unmarshaler:        codec,
			TxDecoder:          c.txc,
			RequestCache:       bdrCache,
			ReplayedHeadersOut: rhCh,
		},
	)

	sub, err := h.Libp2pHost().EventBus().Subscribe(new(libp2pevent.EvtPeerConnectednessChanged))
	if err != nil {
		return fmt.Errorf("failed to subscribe to libp2p host's peer connectedness events: %w", err)
	}
	go func() {
		defer sub.Close()

		for {
			select {
			case <-ctx.Done():
				return
			case e := <-sub.Out():
				switch e := e.(type) {
				case libp2pevent.EvtPeerConnectednessChanged:
					if e.Connectedness == libp2pnetwork.Connected {
						catchupClient.AddPeer(ctx, e.Peer)
					} else if e.Connectedness == libp2pnetwork.NotConnected {
						catchupClient.RemovePeer(ctx, e.Peer)
					}
				default:
					c.log.Warn("Unknown peer connectedness event type", "type", fmt.Sprintf("%T", e))
				}
			}
		}
	}()

	initChainCh := make(chan tmdriver.InitChainRequest)
	blockFinCh := make(chan tmdriver.FinalizeBlockRequest)
	lagStateCh := make(chan tmelink.LagState)
	d, err := gsi.NewDriver(
		c.rootCtx,
		ctx,
		c.log.With("serversys", "driver"),
		gsi.DriverConfig{
			ChainID: c.chainID,

			AppManager: c.app.GetAppManager(),

			Store: c.app.GetStore().(*root.Store),

			InitChainRequests:     initChainCh,
			FinalizeBlockRequests: blockFinCh,
			LagStateUpdates:       lagStateCh,

			TxBuffer: txBuf,

			CatchupClient: catchupClient,

			BlockDataRequestCache: bdrCache,
			BlockDataStore:        c.bds,
		},
	)
	if err != nil {
		return fmt.Errorf("failed to create driver: %w", err)
	}
	c.driver = d

	// We hold onto the options slice so that we can partially initialize it during Init.
	// But it doesn't need to live beyond the scope of Start,
	// so clear the c.opts field to allow the slice to be GCed once the value goes out of scope.
	opts := c.opts
	c.opts = nil

	// Extra options that we couldn't set earlier for whatever reason:
	opts = append(
		opts,
		tmengine.WithBlockFinalizationChannel(blockFinCh),
		tmengine.WithLagStateChannel(lagStateCh),
		tmengine.WithReplayedHeaderRequestChannel(rhCh),
	)

	// We needed the driver before we could make the consensus strategy.
	csCfg := gsi.ConsensusStrategyConfig{
		AppManager: *(c.app.GetAppManager()),
		TxBuf:      txBuf,
		BlockDataProvider: gsbd.NewLibp2pProviderHost(
			c.log.With("s_sys", "block_provider"), h.Libp2pHost(),
		),

		ProposedBlockDataRetriever: gsi.NewPBDRetriever(
			ctx,
			c.log.With("serversys", "pbd_retriever"),
			gsi.PBDRetrieverConfig{
				RequestCache: bdrCache,
				Decoder:      c.txc,

				Host: h.Libp2pHost(),

				NWorkers: 4, // TODO: don't hardcode this.
			},
		),

		BlockDataRequestCache: bdrCache,
	}
	if c.signer != nil {
		csCfg.SignerPubKey = c.signer.PubKey()
	}
	c.cStrat = gsi.NewConsensusStrategy(
		c.rootCtx,
		c.log.With("serversys", "cons_strat"),
		csCfg,
	)
	opts = append(opts, tmengine.WithConsensusStrategy(c.cStrat))

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

	if c.grpcLn != nil {
		// TODO; share this with the http server as a wrapper.
		// https://github.com/rollchains/gordian/pull/14
		c.grpcServer = ggrpc.NewGordianGRPCServer(ctx, c.log.With("sys", "grpc"), ggrpc.GRPCServerConfig{
			Listener: c.grpcLn,

			MirrorStore:       c.ms,
			FinalizationStore: c.fs,

			CryptoRegistry: reg,

			AppManager: am,
			TxCodec:    c.txc,
			Codec:      c.codec,

			TxBuffer: txBuf,
		})
	}

	if c.httpLn != nil {
		c.httpServer = gsi.NewHTTPServer(ctx, c.log.With("sys", "http"), gsi.HTTPServerConfig{
			Listener: c.httpLn,

			MirrorStore:       c.ms,
			FinalizationStore: c.fs,

			CryptoRegistry: reg,

			Libp2pHost: c.h,
			Libp2pconn: c.conn,

			AppManager: am,
			TxCodec:    c.txc,
			Codec:      c.codec,

			TxBuffer: txBuf,
		})
	}

	return nil
}

// Stop is called when the SDK is shutting down the server components.
func (c *Component) Stop(_ context.Context) error {
	c.cancel(errors.New("stopped via SDK server module"))

	// Stop serving client requests before anything else.
	if c.dh != nil {
		c.dh.Wait()
	}

	if c.e != nil {
		c.e.Wait()
	}
	if c.driver != nil {
		c.driver.Wait()
	}
	if c.cStrat != nil {
		c.cStrat.Wait()
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
			// If the HTTP server is closed directly,
			// it will close the underlying listener,
			// which will probably happen before our call to close the listener here.
			// Don't log if the error already indicated the network connection was closed.
			if !errors.Is(err, net.ErrClosed) {
				c.log.Warn("Error closing HTTP listener", "err", err)
			}
		}
		if c.httpServer != nil {
			c.httpServer.Wait()
		}
	}
	if c.grpcLn != nil {
		if err := c.grpcLn.Close(); err != nil {
			// If the GRPC server is closed directly,
			// it will close the underlying listener,
			// which will probably happen before our call to close the listener here.
			// Don't log if the error already indicated the network connection was closed.
			if !errors.Is(err, net.ErrClosed) {
				c.log.Warn("Error closing gRPC listener", "err", err)
			}
		}
		if c.grpcServer != nil {
			c.grpcServer.Wait()
		}
	}
	return nil
}

const (
	httpAddrFlag      = "g-http-addr"
	grpcAddrFlag      = "g-grpc-addr"
	httpAddrFileFlag  = "g-http-addr-file"
	extSignerAddrFlag = "g-signer-addr"

	seedAddrsFlag = "g-seed-addrs"
)

// StartCmdFlags satisfies the optional [serverv2.HasStartFlags] interface,
// which adds the returned flagset to the flags for "$app start".
//
// The configured values are then available in the config map passed to [*Component.Init];
// the flag names are top level keys in the config map,
// with values corresponding to the command line flag values.
func (c *Component) StartCmdFlags() *pflag.FlagSet {
	flags := pflag.NewFlagSet("gserver", pflag.ExitOnError)

	flags.String(httpAddrFlag, "", "TCP address of Gordian's introspective HTTP server; if blank, server will not be started")
	flags.String(grpcAddrFlag, "", "TCP address of Gordian's introspective GRPC server; if blank, server will not be started")
	flags.String(httpAddrFileFlag, "", "Write the actual Gordian HTTP listen address to the given file (useful for tests when configured to listen on :0)")
	flags.String(extSignerAddrFlag, "", "Address of the external signer GRPC server")

	flags.String(seedAddrsFlag, "", "Newline-separated multiaddrs to connect to; if omitted, relies on incoming connections to discover peers")

	// Adds --g-assert-rules in debug builds, no-op otherwise.
	addAssertRuleFlag(flags)

	return flags
}

// WriteCustomConfigAt satisfies an undocumented interface,
// and here we emulate what Comet does in order to get past some error expecting this file to exist.
func (c *Component) WriteCustomConfigAt(configPath string) error {
	f, err := os.Create(filepath.Join(configPath, "config.toml"))
	if err != nil {
		return fmt.Errorf("could not create empty config file: %w", err)
	}
	return f.Close()
}

func (c *Component) CLICommands() serverv2.CLIConfig {
	return serverv2.CLIConfig{
		Commands: []*cobra.Command{
			// These commands are all declared in commands.go.
			newSeedCommand(),
			newPrintValPubKeyCommand(),
		},
	}
}
