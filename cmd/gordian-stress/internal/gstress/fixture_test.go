package gstress_test

import (
	"context"
	"log/slog"
	"os"
	"testing"

	"github.com/libp2p/go-libp2p"
	libp2phost "github.com/libp2p/go-libp2p/core/host"
	libp2ppeer "github.com/libp2p/go-libp2p/core/peer"
	"github.com/rollchains/gordian/cmd/gordian-stress/internal/gstress"
	"github.com/rollchains/gordian/internal/gtest"
	"github.com/rollchains/gordian/tm/tmp2p/tmlibp2p"
	"github.com/stretchr/testify/require"
)

type fixture struct {
	Log *slog.Logger

	Host *gstress.BootstrapHost

	serverSocketPath string
}

func newFixture(ctx context.Context, t *testing.T, cfg fixtureConfig) *fixture {
	t.Helper()

	// Normally we would use t.TempDir() to create a temporary diretory for the socket file.
	// However, that will use a longer-than-necessary path on macOS, e.g.
	// /var/folders/_m/h25_32y958gbgk67m97141400000gq/T/TestBootstrap_registerVali532062248/001/bootstrap.sock
	// And it turns out that there is a hardcoded limit to the file paths associated with sockets,
	// at least on macOS, but probably also on other Unix variants.
	// So, instead of the longer t.TempDir(), we just use os.CreateTemp()
	// with the default location of os.TempDir(), resulting in a full socket path like this:
	// /var/folders/_m/h25_32y958gbgk67m97141400000gq/T/510191397.sock
	//
	// We could be a little more considerate by injecting a specific directory for these tests,
	// but if that is strictly necessary, the user can set the TEMPDIR environment variable
	// to override the default location where the temporary socket files go.
	f, err := os.CreateTemp("", "*.sock")
	if err != nil {
		panic(err)
	}

	serverSocketPath := f.Name()

	if err := f.Close(); err != nil {
		panic(err)
	}
	if err := os.Remove(f.Name()); err != nil {
		panic(err)
	}

	log := gtest.NewLogger(t)

	h, err := gstress.NewBootstrapHost(ctx, log.With("sys", "bootstraphost"), serverSocketPath, cfg.SeedAddrs)
	if err != nil {
		t.Fatalf("failed to create bootstrap host: %v", err)
		return nil
	}

	t.Cleanup(h.Wait)

	return &fixture{
		Log:  log,
		Host: h,

		serverSocketPath: serverSocketPath,
	}
}

func newFixtureWithSeedHost(
	ctx context.Context, t *testing.T, cfg fixtureConfig,
) (*fixture, *tmlibp2p.Host, *gstress.SeedService) {
	t.Helper()

	if cfg.SeedAddrs != nil {
		panic("BUG: cannot set SeedAddrs when calling newFixtureWithSeedHost")
	}

	seedHost, err := tmlibp2p.NewHost(ctx, tmlibp2p.HostOptions{
		Options: []libp2p.Option{
			// Same options as gordian-stress/main.go.
			libp2p.ListenAddrStrings("/ip4/0.0.0.0/tcp/0"),
			libp2p.ForceReachabilityPublic(),
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = seedHost.Close() })

	seedHostInfo := libp2phost.InfoFromHost(seedHost.Libp2pHost())
	p2pAddrs, err := libp2ppeer.AddrInfoToP2pAddrs(seedHostInfo)
	require.NoError(t, err)

	var seedHostAddrs []string
	for _, a := range p2pAddrs {
		seedHostAddrs = append(seedHostAddrs, a.String())
	}

	bfx := newFixture(ctx, t, fixtureConfig{SeedAddrs: seedHostAddrs})
	seedSvc := gstress.NewSeedService(bfx.Log.With("svc", "seed"), seedHost.Libp2pHost(), bfx.Host)

	return bfx, seedHost, seedSvc
}

func (f *fixture) NewBootstrapClient() *gstress.BootstrapClient {
	c, err := gstress.NewBootstrapClient(f.Log.With("type", "bootstrapclient"), f.serverSocketPath)
	if err != nil {
		panic(err)
	}

	return c
}

func (f *fixture) NewSeedClient(ctx context.Context, t *testing.T) *tmlibp2p.Host {
	t.Helper()

	bc := f.NewBootstrapClient()
	seedAddrs, err := bc.SeedAddrs()
	require.NoError(t, err)

	clientHost, err := tmlibp2p.NewHost(ctx, tmlibp2p.HostOptions{
		Options: []libp2p.Option{
			// Same options as gordian-stress/main.go.
			libp2p.ListenAddrStrings("/ip4/0.0.0.0/tcp/0"),
			libp2p.ForceReachabilityPublic(),
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = clientHost.Close() })

	connected := false
	for _, sa := range seedAddrs {
		ai, err := libp2ppeer.AddrInfoFromString(sa)
		require.NoError(t, err)

		if err := clientHost.Libp2pHost().Connect(ctx, *ai); err != nil {
			t.Logf("error connecting to address %q: %v", sa, err)
			continue
		}

		connected = true
		break
	}

	require.True(t, connected, "failed to connect client to seed host")

	return clientHost
}

type fixtureConfig struct {
	SeedAddrs []string
}
