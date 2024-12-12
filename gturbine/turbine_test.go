package gturbine_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/gordian-engine/gordian/gturbine/gtencoding"
	"github.com/gordian-engine/gordian/gturbine/gtnetwork"
	"github.com/gordian-engine/gordian/gturbine/gtshred"
)

type testNode struct {
	transport    *gtnetwork.Transport
	processor    *gtshred.Processor
	codec        gtencoding.ShardCodec
	shredHandler *testShredHandler
	blockHandler *testBlockHandler
}

type testShredHandler struct {
	node *testNode // back-reference for reconstruction
}

func (h *testShredHandler) HandleShred(data []byte, from net.Addr) error {
	shred, err := h.node.codec.Decode(data)
	if err != nil {
		return fmt.Errorf("failed to decode shred: %w", err)
	}
	return h.node.processor.CollectShred(shred)
}

type testBlock struct {
	height    uint64
	blockHash []byte
	block     []byte
}

type testBlockHandler struct {
	blocks []*testBlock
	mu     sync.Mutex
}

func (h *testBlockHandler) ProcessBlock(height uint64, blockHash []byte, block []byte) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.blocks = append(h.blocks, &testBlock{
		height:    height,
		blockHash: blockHash,
		block:     block,
	})
	return nil
}

func newTestNode(t *testing.T, basePort int) *testNode {
	encoder := gtencoding.NewBinaryShardCodec()

	transport := gtnetwork.NewTransport(gtnetwork.Config{
		BasePort: basePort,
		NumPorts: 10,
	})

	cb := &testBlockHandler{}

	hasher := sha256.New
	processor := gtshred.NewProcessor(cb, hasher, hasher, time.Minute)
	go processor.RunBackgroundCleanup(context.Background())

	shredHandler := &testShredHandler{}
	node := &testNode{
		transport:    transport,
		processor:    processor,
		codec:        encoder,
		shredHandler: shredHandler,
		blockHandler: cb,
	}
	shredHandler.node = node

	transport.AddHandler(shredHandler)
	if err := transport.Start(); err != nil {
		t.Fatalf("Failed to start transport: %v", err)
	}

	return node
}

func (n *testNode) stop() {
	n.transport.Stop()
}

func TestBlockPropagation(t *testing.T) {
	// Create two test nodes
	node1 := newTestNode(t, 40000)
	defer node1.stop()

	node2 := newTestNode(t, 40010)
	defer node2.stop()

	// Allow transports to start
	time.Sleep(100 * time.Millisecond)

	// Create and process a test block
	originalBlock := []byte("test block data for propagation")

	const testHeight = 12345

	// Node 1: Shred the block
	shredGroup, err := gtshred.ShredBlock(originalBlock, sha256.New, testHeight, 16, 4)
	if err != nil {
		t.Fatalf("Failed to shred block: %v", err)
	}

	// Node 1: Encode and send shreds to Node 2
	for i, shred := range shredGroup.Shreds {
		encodedShred, err := node1.codec.Encode(shred)
		if err != nil {
			t.Fatalf("Failed to encode shred: %v", err)
		}
		err = node1.transport.SendShred(encodedShred, &net.UDPAddr{
			IP:   net.ParseIP("127.0.0.1"),
			Port: node2.transport.BasePort() + i%10,
		})
		if err != nil {
			t.Fatalf("Failed to send shred: %v", err)
		}
	}

	// Wait for processing
	time.Sleep(300 * time.Millisecond)

	node2.blockHandler.mu.Lock()
	defer node2.blockHandler.mu.Unlock()

	// Verify Node 2 received and reconstructed the block
	if len(node2.blockHandler.blocks) != 1 {
		t.Fatalf("Expected 1 reconstructed block, got %d", len(node2.blockHandler.blocks))
	}

	if !bytes.Equal(node2.blockHandler.blocks[0].block, originalBlock) {
		t.Errorf("Block mismatch: got %q, want %q", node2.blockHandler.blocks[0], originalBlock)
	}

	if node2.blockHandler.blocks[0].height != testHeight {
		t.Fatalf("Block height mismatch: got %d, want %d", node2.blockHandler.blocks[0].height, testHeight)
	}
}

func TestPartialBlockReconstruction(t *testing.T) {
	node1 := newTestNode(t, 40020)
	defer node1.stop()

	node2 := newTestNode(t, 40030)
	defer node2.stop()

	time.Sleep(100 * time.Millisecond)

	originalBlock := []byte("test block for partial reconstruction")

	const testHeight = 54321

	// Create shreds
	shredGroup, err := gtshred.ShredBlock(originalBlock, sha256.New, testHeight, 16, 4)
	if err != nil {
		t.Fatalf("Failed to shred block: %v", err)
	}

	// Send only minimum required shreds
	minShreds := append(shredGroup.Shreds[:12], shredGroup.Shreds[16:]...)
	for i, shred := range minShreds {
		encodedShred, err := node1.codec.Encode(shred)
		if err != nil {
			t.Fatalf("Failed to encode shred: %v", err)
		}

		err = node1.transport.SendShred(encodedShred, &net.UDPAddr{
			IP:   net.ParseIP("127.0.0.1"),
			Port: node2.transport.BasePort() + i%10,
		})
		if err != nil {
			t.Fatalf("Failed to send shred: %v", err)
		}
	}

	time.Sleep(100 * time.Millisecond)

	node2.blockHandler.mu.Lock()
	defer node2.blockHandler.mu.Unlock()

	// Verify Node 2 received and reconstructed the block
	if len(node2.blockHandler.blocks) != 1 {
		t.Fatalf("Expected 1 reconstructed block, got %d", len(node2.blockHandler.blocks))
	}

	if !bytes.Equal(node2.blockHandler.blocks[0].block, originalBlock) {
		t.Errorf("Block mismatch: got %q, want %q", node2.blockHandler.blocks[0], originalBlock)
	}

	if node2.blockHandler.blocks[0].height != testHeight {
		t.Fatalf("Block height mismatch: got %d, want %d", node2.blockHandler.blocks[0].height, testHeight)
	}
}
