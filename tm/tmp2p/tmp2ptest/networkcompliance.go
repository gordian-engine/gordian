package tmp2ptest

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/rollchains/gordian/gcrypto"
	"github.com/rollchains/gordian/internal/gtest"
	"github.com/rollchains/gordian/tm/tmconsensus"
	"github.com/rollchains/gordian/tm/tmconsensus/tmconsensustest"
	"github.com/rollchains/gordian/tm/tmp2p"
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
type NetworkConstructor func(context.Context, *slog.Logger) (Network, error)

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

		Wait()

		Stabilize(context.Context) error
	}
}

func (n *GenericNetwork[C]) Connect(ctx context.Context) (tmp2p.Connection, error) {
	return n.Network.Connect(ctx)
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

		log := gtest.NewLogger(t)

		net, err := newNet(ctx, log)
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

		log := gtest.NewLogger(t)

		net, err := newNet(ctx, log)
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

		fx := tmconsensustest.NewStandardFixture(3)
		b := fx.NextProposedBlock([]byte("app_data"), 0)
		fx.SignProposal(ctx, &b, 0)

		conn1.ConsensusBroadcaster().OutgoingProposedBlocks() <- b

		got := gtest.ReceiveOrTimeout(t, handler2.IncomingProposals(), gtest.ScaleMs(1000))
		require.Equal(t, b, got, "incoming proposal differed from outgoing")

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

		log := gtest.NewLogger(t)

		net, err := newNet(ctx, log)
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
		fx := tmconsensustest.NewStandardFixture(3)

		pb1 := fx.NextProposedBlock([]byte("app_data"), 0)
		fx.SignProposal(ctx, &pb1, 0)

		// Outgoing proposal is seen on other channels.
		conn1.ConsensusBroadcaster().OutgoingProposedBlocks() <- pb1

		got := gtest.ReceiveSoon(t, handler2.IncomingProposals())
		require.Equal(t, pb1, got, "incoming proposal differed from outgoing")

		got = gtest.ReceiveSoon(t, handler3.IncomingProposals())
		require.Equal(t, pb1, got, "incoming proposal differed from outgoing")

		// Disconnect one channel, send a new proposal.
		conn3.Disconnect()

		pb2 := fx.NextProposedBlock([]byte("app_data_2"), 1)
		pb2.Block.Height = 2
		fx.RecalculateHash(&pb2.Block)
		fx.SignProposal(ctx, &pb2, 1)

		gtest.SendSoon(t, conn2.ConsensusBroadcaster().OutgoingProposedBlocks(), pb2)

		// New proposal visible on still-connected channel.
		got = gtest.ReceiveSoon(t, handler1.IncomingProposals())
		require.Equal(t, pb2, got, "incoming proposal differed from outgoing")

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

		fx := tmconsensustest.NewStandardFixture(2)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		log := gtest.NewLogger(t)

		net, err := newNet(ctx, log)
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

		pb := fx.NextProposedBlock([]byte("block_hash"), 0)
		vt := tmconsensus.VoteTarget{
			Height:    1,
			Round:     0,
			BlockHash: string(pb.Block.Hash),
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
				string(pb.Block.Hash): fx.PrevoteSignatureProof(ctx, vt, nil, []int{0}),
				"":                    fx.PrevoteSignatureProof(ctx, nilVT, nil, []int{1}),
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

		fx := tmconsensustest.NewStandardFixture(2)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		log := gtest.NewLogger(t)

		net, err := newNet(ctx, log)
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

		pb := fx.NextProposedBlock([]byte("block_hash"), 0)

		vt := tmconsensus.VoteTarget{
			Height:    1,
			Round:     0,
			BlockHash: string(pb.Block.Hash),
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
				string(pb.Block.Hash): fx.PrecommitSignatureProof(ctx, vt, nil, []int{0}),
				"":                    fx.PrecommitSignatureProof(ctx, nilVT, nil, []int{1}),
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
