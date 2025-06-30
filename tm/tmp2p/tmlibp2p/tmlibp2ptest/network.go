package tmlibp2ptest

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/gordian-engine/gordian/tm/tmcodec"
	"github.com/gordian-engine/gordian/tm/tmconsensus"
	"github.com/gordian-engine/gordian/tm/tmp2p"
	"github.com/gordian-engine/gordian/tm/tmp2p/tmlibp2p"
	"github.com/libp2p/go-libp2p"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	pubsub "github.com/libp2p/go-libp2p-pubsub"
	p2phost "github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/routing"
	"github.com/libp2p/go-libp2p/p2p/transport/tcp"
)

type Network struct {
	log *slog.Logger

	codec tmcodec.MarshalCodec

	seed *tmlibp2p.Host

	connWatchWg sync.WaitGroup

	mu    sync.Mutex
	peers []*tmlibp2p.Connection
}

func NewNetwork(ctx context.Context, log *slog.Logger, codec tmcodec.MarshalCodec) (*Network, error) {
	seed, err := tmlibp2p.NewHost(ctx, newHostOptions(ctx))
	if err != nil {
		return nil, err
	}

	n := &Network{
		log: log,

		codec: codec,

		seed: seed,
	}

	n.connWatchWg.Add(1)
	go n.disconnectAllOnContextClose(ctx)

	return n, nil
}

func newHostOptions(ctx context.Context) tmlibp2p.HostOptions {
	gossipSubParams := pubsub.DefaultGossipSubParams()

	// These low values were arbitrarily chosen, coprime to hopefully avoid CPU spiking,
	// and small enough to be unlikely to cause slowdown in tests.
	// Without these lower values, there is a relatively high rate of integration test failures
	// when running under the race detector, because the gossip sub hasn't been fully set up,
	// likely due to CPU contention with other concurrent tests.
	gossipSubParams.HeartbeatInitialDelay = 8 * time.Millisecond
	gossipSubParams.HeartbeatInterval = 45 * time.Millisecond
	gossipSubParams.DirectConnectInitialDelay = 11 * time.Millisecond

	return tmlibp2p.HostOptions{
		Options: []libp2p.Option{
			// Only use localhost and TCP for test:
			// this simplifies stack traces quite a bit
			// when libp2p doesn't have to consider QUIC connections.
			libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"),
			libp2p.Transport(tcp.NewTCPTransport),

			// Allow localhost connections for test.
			libp2p.ForceReachabilityPublic(),

			// TODO: we don't use libp2p.Routing in main.go
			// because we have separate work that uses a DHT.
			// So it would be better if this wasn't hardcoded to use DHT routing,
			// but rather tests that don't otherwise use a DHT could opt in to it.
			libp2p.Routing(func(h p2phost.Host) (routing.PeerRouting, error) {
				idht, err := dht.New(ctx, h)
				return idht, err
			}),
		},

		PubSubOptions: []pubsub.Option{
			pubsub.WithGossipSubParams(gossipSubParams),
		},
	}
}

func (n *Network) disconnectAllOnContextClose(ctx context.Context) {
	defer n.connWatchWg.Done()

	<-ctx.Done()

	n.mu.Lock()
	defer n.mu.Unlock()
	for _, c := range n.peers {
		c.Disconnect()
	}

	if err := n.seed.Close(); err != nil {
		n.log.Info("Error closing network's seed node", "err", err)
	}
}

func (n *Network) Connect(ctx context.Context) (*tmlibp2p.Connection, error) {
	h, err := tmlibp2p.NewHost(ctx, newHostOptions(ctx))
	if err != nil {
		return nil, err
	}

	seedHost := n.seed.Libp2pHost()
	ai := peer.AddrInfo{
		ID:    seedHost.ID(),
		Addrs: seedHost.Addrs(),
	}
	if err := h.Libp2pHost().Connect(ctx, ai); err != nil {
		return nil, err
	}

	connLog := n.log.With("conn_id", h.Libp2pHost().ID().ShortString())
	conn, err := tmlibp2p.NewConnection(ctx, connLog, h, n.codec)
	if err != nil {
		return nil, err
	}

	n.connWatchWg.Add(1)
	go n.watchConnDisconnect(conn.Disconnected())

	n.mu.Lock()
	defer n.mu.Unlock()
	n.peers = append(n.peers, conn)
	return conn, nil
}

// watchConnDisconnect runs in its own goroutine and blocks
// until ch is closed.
//
// This goroutine is started in n.Connect.
func (n *Network) watchConnDisconnect(ch <-chan struct{}) {
	defer n.connWatchWg.Done()

	<-ch
}

// Wait blocks until both the network's context have been canceled
// and all peers have shut down.
//
// Once Wait is called, it is an error to continue calling other methods on n.
func (n *Network) Wait() {
	n.connWatchWg.Wait()
	if err := n.seed.Close(); err != nil {
		n.log.Info("Error closing seed host", "err", err)
	}
}

// AddDriverAnnotations is a no-op.
func (n *Network) AddDriverAnnotations(context.Context, tmp2p.Connection, *tmconsensus.ProposedHeader) error {
	return nil
}

// Stabilize blocks until all peers in the network
// are aware of each other.
//
// This is useful for tests, to ensure that the network is stable
// before interactions begin.
func (n *Network) Stabilize(ctx context.Context) error {
	n.mu.Lock()
	defer n.mu.Unlock()

	// Every peer should be aware of the seed, the other peers, AND itself.
	want := len(n.peers) + 1

	for ctx.Err() == nil {
		// Seed is handled as its own case, separate from main peers.
		if n.seed.Libp2pHost().Peerstore().Peers().Len() != want {
			time.Sleep(5 * time.Millisecond)
			continue
		}

		allVisible := true
		for _, p := range n.peers {
			if p.Host().Libp2pHost().Peerstore().Peers().Len() != want {
				allVisible = false
				break
			}
		}

		if !allVisible {
			time.Sleep(5 * time.Millisecond)
			continue
		}

		// It's not enough that all the peers see each other.
		// There are some other background routines running.
		// It isn't clear what else to synchronize on,
		// but a 100ms sleep seems to consistently work for now.
		time.Sleep(100 * time.Millisecond)
		return nil
	}

	return ctx.Err()
}
