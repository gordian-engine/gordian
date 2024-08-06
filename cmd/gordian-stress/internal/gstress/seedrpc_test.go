package gstress_test

import (
	"context"
	"net/rpc"
	"testing"

	"github.com/rollchains/gordian/cmd/gordian-stress/internal/gstress"
	"github.com/rollchains/gordian/internal/gtest"
	"github.com/rollchains/gordian/tm/tmconsensus/tmconsensustest"
	"github.com/rollchains/gordian/tm/tmengine"
	"github.com/stretchr/testify/require"
)

func TestSeedRPC_Genesis_startFirst(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	bfx, seedHost, _ := newFixtureWithSeedHost(ctx, t, fixtureConfig{})

	c := bfx.NewBootstrapClient()

	vals := tmconsensustest.DeterministicValidatorsEd25519(2).Vals()
	for _, v := range vals {
		require.NoError(t, c.RegisterValidator(v))
	}

	require.NoError(t, c.Start())

	require.NoError(t, c.SetChainID("my-chain-id"))

	clientHost := bfx.NewSeedClient(ctx, t)

	seedRPCStream, err := clientHost.Libp2pHost().NewStream(
		ctx, seedHost.Libp2pHost().ID(), gstress.SeedServiceProtocolID,
	)
	require.NoError(t, err)

	rpcClient := rpc.NewClient(seedRPCStream)

	resp := new(gstress.RPCGenesisResponse)
	require.NoError(t, rpcClient.Call("SeedRPC.Genesis", gstress.RPCGenesisRequest{}, resp))
	require.Equal(t, "echo", resp.App)
	require.Equal(t, "my-chain-id", resp.ChainID)
	require.Equal(t, vals, resp.Validators)
}

func TestSeedRPC_Genesis_blocksUntilStart(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	bfx, seedHost, _ := newFixtureWithSeedHost(ctx, t, fixtureConfig{})

	c := bfx.NewBootstrapClient()

	vals := tmconsensustest.DeterministicValidatorsEd25519(2).Vals()
	for _, v := range vals {
		require.NoError(t, c.RegisterValidator(v))
	}

	require.NoError(t, c.SetChainID("my-chain-id"))

	// Open two client streams without calling Start.
	clientHost1 := bfx.NewSeedClient(ctx, t)
	clientHost2 := bfx.NewSeedClient(ctx, t)

	seedRPCStream1, err := clientHost1.Libp2pHost().NewStream(
		ctx, seedHost.Libp2pHost().ID(), gstress.SeedServiceProtocolID,
	)
	require.NoError(t, err)

	seedRPCStream2, err := clientHost2.Libp2pHost().NewStream(
		ctx, seedHost.Libp2pHost().ID(), gstress.SeedServiceProtocolID,
	)
	require.NoError(t, err)

	rpcClient1 := rpc.NewClient(seedRPCStream1)
	resp1 := new(gstress.RPCGenesisResponse)
	call1 := rpcClient1.Go("SeedRPC.Genesis", gstress.RPCGenesisRequest{}, resp1, nil)

	rpcClient2 := rpc.NewClient(seedRPCStream2)
	resp2 := new(gstress.RPCGenesisResponse)
	call2 := rpcClient2.Go("SeedRPC.Genesis", gstress.RPCGenesisRequest{}, resp2, nil)

	// The async calls are outstanding.
	gtest.NotSending(t, call1.Done)
	gtest.NotSending(t, call2.Done)

	require.NoError(t, c.Start())

	// Now the calls complete rapidly.
	call1 = gtest.ReceiveSoon(t, call1.Done)
	call2 = gtest.ReceiveSoon(t, call2.Done)

	// And they completed without error.
	require.NoError(t, call1.Error)
	require.NoError(t, call2.Error)

	// They both have the correct values.
	require.Equal(t, "my-chain-id", resp1.ChainID)
	require.Equal(t, vals, resp1.Validators)
	require.Equal(t, "my-chain-id", resp2.ChainID)
	require.Equal(t, vals, resp2.Validators)
}

func TestSeedRPC_halt(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	bfx, seedHost, _ := newFixtureWithSeedHost(ctx, t, fixtureConfig{})

	c := bfx.NewBootstrapClient()

	clientHost := bfx.NewSeedClient(ctx, t)

	seedRPCStream, err := clientHost.Libp2pHost().NewStream(
		ctx, seedHost.Libp2pHost().ID(), gstress.SeedServiceProtocolID,
	)
	require.NoError(t, err)

	rpcClient := rpc.NewClient(seedRPCStream)
	resp := new(gstress.RPCHaltResponse)
	call := rpcClient.Go("SeedRPC.AwaitHalt", gstress.RPCHaltRequest{}, resp, nil)

	// Async call outstanding.
	gtest.NotSending(t, call.Done)

	// Make halt call.
	require.NoError(t, c.Halt())

	// RPC call completes instantly.
	call = gtest.ReceiveSoon(t, call.Done)
	require.NoError(t, call.Error)

	_ = gtest.ReceiveSoon(t, bfx.Host.Halted())
}

func TestSeed_metrics(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	bfx, seedHost, seedSvc := newFixtureWithSeedHost(ctx, t, fixtureConfig{})

	clientHost := bfx.NewSeedClient(ctx, t)

	seedRPCStream, err := clientHost.Libp2pHost().NewStream(
		ctx, seedHost.Libp2pHost().ID(), gstress.SeedServiceProtocolID,
	)
	require.NoError(t, err)

	m := tmengine.Metrics{
		StateMachineHeight: 1,
		StateMachineRound:  1,

		MirrorVotingRound:      2,
		MirrorVotingHeight:     2,
		MirrorCommittingRound:  1,
		MirrorCommittingHeight: 1,
	}
	rpcClient := rpc.NewClient(seedRPCStream)
	require.NoError(t, rpcClient.Call(
		"SeedRPC.PublishMetrics",
		gstress.RPCPublishMetricsRequest{Metrics: m},
		new(gstress.RPCPublishMetricsResponse),
	))

	gotM := make(map[string]tmengine.Metrics)
	seedSvc.CopyMetrics(gotM)
	require.Len(t, gotM, 1)

	var got tmengine.Metrics
	for _, v := range gotM {
		got = v
	}

	require.Equal(t, m, got)
}
