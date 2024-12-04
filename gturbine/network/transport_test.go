package network

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/gordian-engine/gordian/gturbine"
	"github.com/gordian-engine/gordian/gturbine/builder"
	"github.com/gordian-engine/gordian/tm/tmconsensus"
)

type testHandler struct {
	mu      sync.Mutex
	blocks  [][]byte
	heights []uint64
}

func (h *testHandler) OnProposedHeader(_ context.Context, ph tmconsensus.ProposedHeader) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	// Store block data from header for verification
	return nil
}

func TestTransport(t *testing.T) {
	makeValidators := func(count int) []gturbine.Validator {
		vals := make([]gturbine.Validator, count)
		for i := 0; i < count; i++ {
			_, key, _ := ed25519.GenerateKey(nil)
			vals[i] = gturbine.Validator{
				PubKey:  key.Public().(ed25519.PublicKey),
				Stake:   uint64(1000 * (i + 1)),
				NetAddr: fmt.Sprintf("127.0.0.1:%d", 50000+i),
			}
		}
		return vals
	}

	setupNetwork := func(t *testing.T, validators []gturbine.Validator) []*Transport {
		transports := make([]*Transport, len(validators))
		for i, val := range validators {
			cfg := Config{
				ListenAddr:   val.NetAddr,
				ChunkSize:    1024,
				DataShards:   4,
				ParityShards: 2,
				ValidatorKey: val.PubKey,
			}

			transport, err := NewTransport(cfg)
			if err != nil {
				t.Fatalf("failed to create transport %d: %v", i, err)
			}

			handler := &testHandler{}
			transport.SetConsensusHandler(context.Background(), handler)
			transports[i] = transport
		}
		return transports
	}

	t.Run("basic block broadcast", func(t *testing.T) {
		validators := makeValidators(4)
		transports := setupNetwork(t, validators)
		defer func() {
			for _, tr := range transports {
				tr.Disconnect()
			}
		}()

		// Build tree
		tb := builder.NewTreeBuilder(2)
		tree, err := tb.BuildTree(validators, 1, 0)
		if err != nil {
			t.Fatal(err)
		}

		for _, tr := range transports {
			tr.SetTree(tree)
		}

		// Broadcast block from root
		block := []byte("test block data")
		err = transports[0].BroadcastBlock(block, 1, 1, 0)
		if err != nil {
			t.Fatal(err)
		}

		// Wait for propagation
		time.Sleep(time.Second)

		// Verify all nodes received the block
		for i, tr := range transports {
			tr.mu.Lock()
			groups := len(tr.shredGroups)
			tr.mu.Unlock()

			if i > 0 && groups == 0 {
				t.Errorf("validator %d did not receive any shreds", i)
			}
		}
	})

	t.Run("partial network failure", func(t *testing.T) {
		validators := makeValidators(6)
		transports := setupNetwork(t, validators)
		defer func() {
			for _, tr := range transports {
				tr.Disconnect()
			}
		}()

		tb := builder.NewTreeBuilder(2)
		tree, _ := tb.BuildTree(validators, 1, 0)
		for _, tr := range transports {
			tr.SetTree(tree)
		}

		// Disconnect some nodes
		transports[2].Disconnect()
		transports[3].Disconnect()

		block := []byte("test block with failures")
		err := transports[0].BroadcastBlock(block, 1, 1, 0)
		if err != nil {
			t.Fatal(err)
		}

		time.Sleep(time.Second)

		// Check remaining nodes
		for i, tr := range transports {
			if i == 2 || i == 3 {
				continue
			}
			tr.mu.Lock()
			groups := len(tr.shredGroups)
			tr.mu.Unlock()

			if groups == 0 {
				t.Errorf("validator %d did not receive shreds", i)
			}
		}
	})

	t.Run("large block transmission", func(t *testing.T) {
		validators := makeValidators(4)
		transports := setupNetwork(t, validators)
		defer func() {
			for _, tr := range transports {
				tr.Disconnect()
			}
		}()

		tb := builder.NewTreeBuilder(2)
		tree, _ := tb.BuildTree(validators, 1, 0)
		for _, tr := range transports {
			tr.SetTree(tree)
		}

		// Create 1MB block
		block := make([]byte, 1024*1024)
		for i := range block {
			block[i] = byte(i % 256)
		}

		err := transports[0].BroadcastBlock(block, 1, 1, 0)
		if err != nil {
			t.Fatal(err)
		}

		time.Sleep(2 * time.Second)

		// Verify propagation
		for i, tr := range transports {
			tr.mu.Lock()
			groups := len(tr.shredGroups)
			tr.mu.Unlock()

			if i > 0 && groups == 0 {
				t.Errorf("validator %d did not receive large block shreds", i)
			}
		}
	})
}
