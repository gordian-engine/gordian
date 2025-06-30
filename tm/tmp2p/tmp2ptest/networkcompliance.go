package tmp2ptest

import (
	"context"
	"testing"
	"time"

	"github.com/gordian-engine/gordian/gcrypto"
	"github.com/gordian-engine/gordian/internal/gtest"
	"github.com/gordian-engine/gordian/tm/tmconsensus"
	"github.com/gordian-engine/gordian/tm/tmconsensus/tmconsensustest"
	"github.com/gordian-engine/gordian/tm/tmp2p"
	"github.com/stretchr/testify/require"
)

// Network is a generalized interface for an in-process network for testing.
//
// Some p2p implementations, such as [LoopbackNetwork] are a first-class network implementation.
// Others may require extra code, such as libp2p requiring a "seed node"
// for other peers to join for discovery purposes.
type Network interface {
	// Open a connection.
	Connect(context.Context) (tmp2p.Connection, error)

	// The network compliance tests do not directly touch the annotations
	// on the ProposedHeader or its Header.
	// This method gives the network a chance to modify those fields
	// so that recipients have sufficient information to decode the value.
	//
	// In production code, the driver would directly set those fields initially.
	AddDriverAnnotations(context.Context, tmp2p.Connection, *tmconsensus.ProposedHeader) error

	// Block until the network has cleaned up.
	// Typically the Network has a lifecycle associated with a context,
	// so cancel that context to stop the network.
	Wait()

	// Stabilize blocks until the current set of connections are
	// aware of other live connections in this Network.
	//
	// Some Network implementations may take time to fully set up connections,
	// so this should be called after a batch of Connect or Disconnect calls.
	Stabilize(context.Context) error
}

// NetworkConstructor is used within [TestNetworkCompliance] to create a Network.
// The testing.T parameter is available for tests to register cleanup.
type NetworkConstructor func(*testing.T, context.Context) (Network, error)

// GenericNetwork is a convenience wrapper type that allows
// a concrete network implementation to have a Connect method
// returning the appropriate concrete connection type.
//
// That is to say, you may define:
//
//	type MyNetwork struct { /* ... */ }
//
//	func (n *MyNetwork) Connect() (*MyConn, error) { /* ... */ }
//
// and then use the GenericNetwork wrapper type,
// instead of rewriting your own wrapper
// or instead of defining your Connect() method to return
// a less specific tmp2p.Connection value.
type GenericNetwork[C tmp2p.Connection] struct {
	Network interface {
		Connect(context.Context) (C, error)

		AddDriverAnnotations(context.Context, tmp2p.Connection, *tmconsensus.ProposedHeader) error

		Wait()

		Stabilize(context.Context) error
	}
}

func (n *GenericNetwork[C]) Connect(ctx context.Context) (tmp2p.Connection, error) {
	return n.Network.Connect(ctx)
}

func (n *GenericNetwork[C]) AddDriverAnnotations(
	ctx context.Context, c tmp2p.Connection, ph *tmconsensus.ProposedHeader,
) error {
	return n.Network.AddDriverAnnotations(ctx, c, ph)
}

func (n *GenericNetwork[C]) Wait() {
	n.Network.Wait()
}

func (n *GenericNetwork[C]) Stabilize(ctx context.Context) error {
	return n.Network.Stabilize(ctx)
}

func TestNetworkCompliance(t *testing.T, newNet NetworkConstructor) {
	t.Run("child connections are closed on main context cancellation", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		net, err := newNet(t, ctx)
		require.NoError(t, err)
		defer net.Wait()
		defer cancel()

		conn1, err := net.Connect(ctx)
		require.NoError(t, err)
		conn2, err := net.Connect(ctx)
		require.NoError(t, err)

		net.Stabilize(ctx)

		// No need to stabilize this time.
		// But do ensure the conn channels are not closed.
		select {
		case <-conn1.Disconnected():
			t.Fatal("conn1 should not have started in a disconnected state")
		default:
			// Okay.
		}
		select {
		case <-conn2.Disconnected():
			t.Fatal("conn2 should not have started in a disconnected state")
		default:
			// Okay.
		}

		// Cancel the context; wait for the network to report completion.
		cancel()
		net.Wait()

		// Now both connections' Disconnected channel should be closed.
		select {
		case <-conn1.Disconnected():
			// Okay.
		default:
			t.Fatal("conn1 did not report disconnected after network shutdown")
		}
		select {
		case <-conn2.Disconnected():
			// Okay.
		default:
			t.Fatal("conn2 did not report disconnected after network shutdown")
		}
	})

	t.Run("basic proposal send and receive", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		net, err := newNet(t, ctx)
		require.NoError(t, err)
		defer net.Wait()
		defer cancel()

		conn1, err := net.Connect(ctx)
		require.NoError(t, err)
		conn2, err := net.Connect(ctx)
		require.NoError(t, err)

		handler1 := tmconsensustest.NewChannelConsensusHandler(1)
		conn1.SetConsensusHandler(ctx, handler1)
		handler2 := tmconsensustest.NewChannelConsensusHandler(1)
		conn2.SetConsensusHandler(ctx, handler2)

		require.NoError(t, net.Stabilize(ctx))

		fx := tmconsensustest.NewEd25519Fixture(3)
		h := fx.NextProposedHeader([]byte("app_data"), 0)
		net.AddDriverAnnotations(ctx, conn1, &h)
		fx.RecalculateHash(&h.Header)
		fx.SignProposal(ctx, &h, 0)

		conn1.ConsensusBroadcaster().OutgoingProposedHeaders() <- h

		got := gtest.ReceiveOrTimeout(t, handler2.IncomingProposals(), gtest.ScaleMs(1000))
		require.Equal(t, h, got, "incoming proposal differed from outgoing")

		select {
		case got := <-handler1.IncomingProposals():
			t.Fatalf("got proposal %v back on same connection as sender", got)
		case <-time.After(25 * time.Millisecond):
			// Okay.
		}
	})

	t.Run("basic proposal after one connection disconnects", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		net, err := newNet(t, ctx)
		require.NoError(t, err)
		defer net.Wait()
		defer cancel()

		conn1, err := net.Connect(ctx)
		require.NoError(t, err)
		conn2, err := net.Connect(ctx)
		require.NoError(t, err)
		conn3, err := net.Connect(ctx)
		require.NoError(t, err)

		handler1 := tmconsensustest.NewChannelConsensusHandler(1)
		conn1.SetConsensusHandler(ctx, handler1)
		handler2 := tmconsensustest.NewChannelConsensusHandler(1)
		conn2.SetConsensusHandler(ctx, handler2)
		handler3 := tmconsensustest.NewChannelConsensusHandler(1)
		conn3.SetConsensusHandler(ctx, handler3)

		require.NoError(t, net.Stabilize(ctx))

		// Use a fixture so we populate all relevant fields.
		fx := tmconsensustest.NewEd25519Fixture(3)

		ph1 := fx.NextProposedHeader([]byte("app_data"), 0)
		net.AddDriverAnnotations(ctx, conn1, &ph1)
		fx.RecalculateHash(&ph1.Header)
		fx.SignProposal(ctx, &ph1, 0)

		// Outgoing proposal is seen on other channels.
		conn1.ConsensusBroadcaster().OutgoingProposedHeaders() <- ph1

		got := gtest.ReceiveSoon(t, handler2.IncomingProposals())
		require.Equal(t, ph1, got, "incoming proposal differed from outgoing")

		got = gtest.ReceiveSoon(t, handler3.IncomingProposals())
		require.Equal(t, ph1, got, "incoming proposal differed from outgoing")

		// Disconnect one channel, send a new proposal.
		conn3.Disconnect()

		ph2 := fx.NextProposedHeader([]byte("app_data_2"), 1)
		ph2.Header.Height = 2
		net.AddDriverAnnotations(ctx, conn2, &ph2)
		fx.RecalculateHash(&ph2.Header)
		fx.SignProposal(ctx, &ph2, 1)

		gtest.SendSoon(t, conn2.ConsensusBroadcaster().OutgoingProposedHeaders(), ph2)

		// New proposal visible on still-connected channel.
		got = gtest.ReceiveSoon(t, handler1.IncomingProposals())
		require.Equal(t, ph2, got, "incoming proposal differed from outgoing")

		// Disconnected handler didn't receive anything.
		select {
		case <-handler3.IncomingProposals():
			t.Fatal("handler for disconnected connection should not have received message")
		case <-time.After(25 * time.Millisecond):
			// Okay.
		}
	})

	t.Run("basic prevote proof send and receive", func(t *testing.T) {
		t.Parallel()

		fx := tmconsensustest.NewEd25519Fixture(2)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		net, err := newNet(t, ctx)
		require.NoError(t, err)
		defer net.Wait()
		defer cancel()

		conn1, err := net.Connect(ctx)
		require.NoError(t, err)
		conn2, err := net.Connect(ctx)
		require.NoError(t, err)

		handler1 := tmconsensustest.NewChannelConsensusHandler(1)
		conn1.SetConsensusHandler(ctx, handler1)

		handler2 := tmconsensustest.NewChannelConsensusHandler(1)
		conn2.SetConsensusHandler(ctx, handler2)

		require.NoError(t, net.Stabilize(ctx))

		ph := fx.NextProposedHeader([]byte("block_hash"), 0)
		net.AddDriverAnnotations(ctx, conn1, &ph)
		fx.RecalculateHash(&ph.Header)
		vt := tmconsensus.VoteTarget{
			Height:    1,
			Round:     0,
			BlockHash: string(ph.Header.Hash),
		}
		nilVT := tmconsensus.VoteTarget{
			Height:    1,
			Round:     0,
			BlockHash: "",
		}
		prevoteProof, err := tmconsensus.PrevoteProof{
			Height: 1,
			Round:  0,
			Proofs: map[string]gcrypto.CommonMessageSignatureProof{
				string(ph.Header.Hash): fx.PrevoteSignatureProof(ctx, vt, nil, []int{0}),
				"":                     fx.PrevoteSignatureProof(ctx, nilVT, nil, []int{1}),
			},
		}.AsSparse()
		require.NoError(t, err)

		gtest.SendSoon(t, conn1.ConsensusBroadcaster().OutgoingPrevoteProofs(), prevoteProof)

		got := gtest.ReceiveSoon(t, handler2.IncomingPrevoteProofs())
		require.Equal(t, prevoteProof, got, "incoming prevote proof differed from outgoing")

		select {
		case got := <-handler1.IncomingPrevoteProofs():
			t.Fatalf("got prevote proof %v back on same connection as sender", got)
		case <-time.After(25 * time.Millisecond):
			// Okay.
		}
	})

	t.Run("basic precommit send and receive", func(t *testing.T) {
		t.Parallel()

		fx := tmconsensustest.NewEd25519Fixture(2)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		net, err := newNet(t, ctx)
		require.NoError(t, err)
		defer net.Wait()
		defer cancel()

		conn1, err := net.Connect(ctx)
		require.NoError(t, err)
		conn2, err := net.Connect(ctx)
		require.NoError(t, err)

		handler1 := tmconsensustest.NewChannelConsensusHandler(1)
		conn1.SetConsensusHandler(ctx, handler1)
		handler2 := tmconsensustest.NewChannelConsensusHandler(1)
		conn2.SetConsensusHandler(ctx, handler2)

		require.NoError(t, net.Stabilize(ctx))

		ph := fx.NextProposedHeader([]byte("block_hash"), 0)
		net.AddDriverAnnotations(ctx, conn1, &ph)
		fx.RecalculateHash(&ph.Header)

		vt := tmconsensus.VoteTarget{
			Height:    1,
			Round:     0,
			BlockHash: string(ph.Header.Hash),
		}
		nilVT := tmconsensus.VoteTarget{
			Height:    1,
			Round:     0,
			BlockHash: "",
		}
		precommitProof, err := tmconsensus.PrecommitProof{
			Height: 1,
			Round:  0,
			Proofs: map[string]gcrypto.CommonMessageSignatureProof{
				string(ph.Header.Hash): fx.PrecommitSignatureProof(ctx, vt, nil, []int{0}),
				"":                     fx.PrecommitSignatureProof(ctx, nilVT, nil, []int{1}),
			},
		}.AsSparse()
		require.NoError(t, err)

		gtest.SendSoon(t, conn1.ConsensusBroadcaster().OutgoingPrecommitProofs(), precommitProof)

		got := gtest.ReceiveSoon(t, handler2.IncomingPrecommitProofs())
		require.Equal(t, precommitProof, got, "incoming precommit differed from outgoing")

		select {
		case got := <-handler1.IncomingPrecommitProofs():
			t.Fatalf("got precommit %v back on same connection as sender", got)
		case <-time.After(25 * time.Millisecond):
			// Okay.
		}
	})
}
