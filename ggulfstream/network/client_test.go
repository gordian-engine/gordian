package network

import (
	"context"
	"testing"
	"time"

	"github.com/gordian-engine/gordian/tm/tmconsensus"
	"github.com/gordian-engine/gordian/tm/tmp2p"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

// mockConnection implements tmp2p.Connection for testing
type mockConnection struct {
	mock.Mock
	disconnectCh chan struct{}
	broadcaster  *mockBroadcaster
}

func newMockConnection() *mockConnection {
	m := &mockConnection{
		disconnectCh: make(chan struct{}),
		broadcaster:  newMockBroadcaster(),
	}
	m.On("ConsensusBroadcaster").Return(m.broadcaster)
	return m
}

func (m *mockConnection) ConsensusBroadcaster() tmp2p.ConsensusBroadcaster {
	args := m.Called()
	return args.Get(0).(tmp2p.ConsensusBroadcaster)
}

func (m *mockConnection) SetConsensusHandler(ctx context.Context, handler tmconsensus.ConsensusHandler) {
	m.Called(ctx, handler)
}

func (m *mockConnection) Disconnect() {
	m.Called()
	close(m.disconnectCh)
}

func (m *mockConnection) Disconnected() <-chan struct{} {
	return m.disconnectCh
}

// mockBroadcaster implements tmp2p.ConsensusBroadcaster for testing
type mockBroadcaster struct {
	mock.Mock
	proposalCh  chan tmconsensus.ProposedHeader
	prevoteCh   chan tmconsensus.PrevoteSparseProof
	precommitCh chan tmconsensus.PrecommitSparseProof
}

func newMockBroadcaster() *mockBroadcaster {
	return &mockBroadcaster{
		proposalCh:  make(chan tmconsensus.ProposedHeader, 10),
		prevoteCh:   make(chan tmconsensus.PrevoteSparseProof, 10),
		precommitCh: make(chan tmconsensus.PrecommitSparseProof, 10),
	}
}

func (m *mockBroadcaster) OutgoingProposedHeaders() chan<- tmconsensus.ProposedHeader {
	return m.proposalCh
}

func (m *mockBroadcaster) OutgoingPrevoteProofs() chan<- tmconsensus.PrevoteSparseProof {
	return m.prevoteCh
}

func (m *mockBroadcaster) OutgoingPrecommitProofs() chan<- tmconsensus.PrecommitSparseProof {
	return m.precommitCh
}

func TestNetworkClient(t *testing.T) {
	t.Run("initializes with valid config", func(t *testing.T) {
		conn := newMockConnection()
		cfg := &Config{
			MaxRetries: 3,
		}

		client, err := New(context.Background(), conn, cfg)
		assert.NoError(t, err)
		assert.NotNil(t, client)
		assert.Equal(t, 3, client.maxRetries)
	})

	t.Run("uses default config when nil", func(t *testing.T) {
		conn := newMockConnection()
		client, err := New(context.Background(), conn, nil)
		assert.NoError(t, err)
		assert.NotNil(t, client)
		assert.Equal(t, DefaultConfig().MaxRetries, client.maxRetries)
	})

	t.Run("fails without connection", func(t *testing.T) {
		_, err := New(context.Background(), nil, nil)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "p2p connection required")
	})

	t.Run("handles context cancellation", func(t *testing.T) {
		conn := newMockConnection()
		ctx, cancel := context.WithCancel(context.Background())

		client, err := New(ctx, conn, nil)
		assert.NoError(t, err)

		// Send a batch before cancellation
		batch1 := &TransactionBatch{
			Transactions: [][]byte{[]byte("tx1")},
			NodeID:       "node1",
		}
		err = client.SendTransactionBatch(batch1)
		assert.NoError(t, err)

		// Cancel context
		cancel()
		time.Sleep(10 * time.Millisecond)

		// Try to send after cancellation
		batch2 := &TransactionBatch{
			Transactions: [][]byte{[]byte("tx2")},
			NodeID:       "node1",
		}
		err = client.SendTransactionBatch(batch2)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "client is closed")
	})

	t.Run("tracks statistics correctly", func(t *testing.T) {
		conn := newMockConnection()
		client, err := New(context.Background(), conn, nil)
		assert.NoError(t, err)

		// Send multiple batches
		batches := []*TransactionBatch{
			{
				Transactions: [][]byte{[]byte("tx1"), []byte("tx2")},
				NodeID:       "node1",
			},
			{
				Transactions: [][]byte{[]byte("tx3")},
				NodeID:       "node2",
			},
		}

		for _, batch := range batches {
			err := client.SendTransactionBatch(batch)
			assert.NoError(t, err)
		}

		stats := client.GetStats()
		assert.Equal(t, uint64(2), stats.BatchesSent)
		assert.Equal(t, uint64(3), stats.TxSent)
		assert.Equal(t, uint64(0), stats.SendErrors)
		assert.Equal(t, uint32(0), stats.ActiveSends)
	})

	t.Run("tracks active sends", func(t *testing.T) {
		t.Skip()
		conn := newMockConnection()
		client, err := New(context.Background(), conn, nil)
		assert.NoError(t, err)

		// Start concurrent sends to ensure we track multiple active sends
		done := make(chan struct{})
		go func() {
			defer close(done)
			batch := &TransactionBatch{
				Transactions: [][]byte{[]byte("tx1")},
				NodeID:       "node1",
			}
			client.SendTransactionBatch(batch)
		}()

		// Read stats immediately after starting send
		stats := client.GetStats()
		assert.Equal(t, uint32(1), stats.ActiveSends, "should have 1 active send")

		// Wait for send to complete
		<-done

		// Verify active sends is back to 0
		stats = client.GetStats()
		assert.Equal(t, uint32(0), stats.ActiveSends, "active sends should be 0 after completion")
	})

	t.Run("closes cleanly", func(t *testing.T) {
		conn := newMockConnection()
		conn.On("Disconnect").Return()

		client, err := New(context.Background(), conn, nil)
		assert.NoError(t, err)

		err = client.Close()
		assert.NoError(t, err)

		// Try to send after close
		batch := &TransactionBatch{
			Transactions: [][]byte{[]byte("tx1")},
			NodeID:       "node1",
		}
		err = client.SendTransactionBatch(batch)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "client is closed")
	})
}
