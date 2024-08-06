package gstress_test

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	libp2pping "github.com/libp2p/go-libp2p/p2p/protocol/ping"
	"github.com/rollchains/gordian/internal/gtest"
	"github.com/rollchains/gordian/tm/tmconsensus/tmconsensustest"
	"github.com/stretchr/testify/require"
)

func TestBootstrap_seedAddrs(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	seedAddrs := []string{"a", "b", "c"}

	bfx := newFixture(ctx, t, fixtureConfig{SeedAddrs: seedAddrs})

	c := bfx.NewBootstrapClient()

	addrs, err := c.SeedAddrs()
	require.NoError(t, err)

	require.Equal(t, seedAddrs, addrs)
}

func TestBootstrap_chainID(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	beforeSec := time.Now().Unix()
	bfx := newFixture(ctx, t, fixtureConfig{SeedAddrs: []string{"a"}})
	afterSec := time.Now().Unix()

	c := bfx.NewBootstrapClient()

	// Confirm the default ID,
	// which should be formatted as "gstress%d" % time.Now().Unix().
	chainID, err := c.ChainID()
	require.NoError(t, err)

	sec, err := strconv.ParseInt(strings.TrimPrefix(chainID, "gstress"), 10, 64)
	require.NoError(t, err)

	require.LessOrEqual(t, beforeSec, sec)
	require.GreaterOrEqual(t, afterSec, sec)

	// Change the chain ID.
	require.NoError(t, c.SetChainID("foo"))

	chainID, err = c.ChainID()
	require.NoError(t, err)
	require.Equal(t, "foo", chainID)
}

func TestBootstrap_app(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	bfx := newFixture(ctx, t, fixtureConfig{SeedAddrs: []string{"a"}})

	c := bfx.NewBootstrapClient()

	// The default app is echo.
	app, err := c.App()
	require.NoError(t, err)
	require.Equal(t, "echo", app)

	// Setting the app to anything else currently fails,
	// because we don't yet support any other built-in app.
	// Change the chain ID.
	require.Error(t, c.SetApp("foo"))

	// Confirm it didn't change.
	app, err = c.App()
	require.NoError(t, err)
	require.Equal(t, "echo", app)
}

func TestBootstrap_registerValidator(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	bfx := newFixture(ctx, t, fixtureConfig{SeedAddrs: []string{"a"}})

	c := bfx.NewBootstrapClient()

	// Registering a validator once succeeds.
	v := tmconsensustest.DeterministicValidatorsEd25519(1)[0].CVal
	require.NoError(t, c.RegisterValidator(v))

	// Attempting again will cause a panic on one of the bootstrap host goroutines,
	// so we aren't going to do that in test.

	got, err := c.Validators()
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, v, got[0])
}

func TestBootstrap_start(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	bfx, seedHost, _ := newFixtureWithSeedHost(ctx, t, fixtureConfig{})

	clientHost := bfx.NewSeedClient(ctx, t)

	ch := libp2pping.Ping(ctx, clientHost.Libp2pHost(), seedHost.Libp2pHost().ID())

	res := gtest.ReceiveSoon(t, ch)
	require.NoError(t, res.Error)
	// Takes under 200 microseconds on my machine,
	// so the default ReceiveSoon timeout seems okay.
	t.Logf("Ping RTT: %s", res.RTT)
}
