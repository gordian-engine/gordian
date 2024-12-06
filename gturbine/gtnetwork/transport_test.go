package gtnetwork

import (
	"net"
	"sync"
	"testing"
	"time"
)

type testHandler struct {
	mu     sync.Mutex
	shreds [][]byte
	addrs  []net.Addr
}

func (h *testHandler) HandleShred(data []byte, from net.Addr) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.shreds = append(h.shreds, data)
	h.addrs = append(h.addrs, from)
	return nil
}

func TestTransport(t *testing.T) {
	cfg := Config{
		BasePort: 30000, // Use high ports for testing
		NumPorts: 2,
	}

	tr := NewTransport(cfg)
	handler := &testHandler{}
	tr.AddHandler(handler)

	if err := tr.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer tr.Stop()

	// Allow listeners to start
	time.Sleep(100 * time.Millisecond)

	// Test send/receive
	testData := []byte("test data")
	addr := &net.UDPAddr{
		IP:   net.ParseIP("127.0.0.1"),
		Port: cfg.BasePort,
	}

	if err := tr.SendShred(testData, addr); err != nil {
		t.Fatalf("SendShred failed: %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	handler.mu.Lock()
	if len(handler.shreds) != 1 {
		t.Fatalf("Expected 1 shred, got %d", len(handler.shreds))
	}
	if string(handler.shreds[0]) != string(testData) {
		t.Errorf("Expected %q, got %q", testData, handler.shreds[0])
	}
	handler.mu.Unlock()
}

func TestMultipleHandlers(t *testing.T) {
	cfg := Config{
		BasePort: 30100,
		NumPorts: 1,
	}

	tr := NewTransport(cfg)
	h1, h2 := &testHandler{}, &testHandler{}
	tr.AddHandler(h1)
	tr.AddHandler(h2)

	if err := tr.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer tr.Stop()

	time.Sleep(100 * time.Millisecond)

	testData := []byte("test multiple handlers")
	addr := &net.UDPAddr{
		IP:   net.ParseIP("127.0.0.1"),
		Port: cfg.BasePort,
	}

	if err := tr.SendShred(testData, addr); err != nil {
		t.Fatalf("SendShred failed: %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	for i, h := range []*testHandler{h1, h2} {
		h.mu.Lock()
		if len(h.shreds) != 1 {
			t.Errorf("Handler %d: Expected 1 shred, got %d", i, len(h.shreds))
		}
		if string(h.shreds[0]) != string(testData) {
			t.Errorf("Handler %d: Expected %q, got %q", i, testData, h.shreds[0])
		}
		h.mu.Unlock()
	}
}
