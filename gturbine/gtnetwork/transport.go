package gtnetwork

import (
	"context"
	"fmt"
	"net"
	"sync"
)

// Transport handles shred sending/receiving over UDP
type Transport struct {
	basePort  int
	numPorts  int
	listeners []*net.UDPConn
	handlers  []ShredHandler
	ctx       context.Context
	cancel    context.CancelFunc
	mu        sync.RWMutex
}

// ShredHandler processes received shreds
type ShredHandler interface {
	HandleShred(data []byte, from net.Addr) error
}

// Config contains Transport configuration
type Config struct {
	BasePort int
	NumPorts int
}

// DefaultConfig returns standard Transport configuration
func DefaultConfig() Config {
	return Config{
		BasePort: 12000,
		NumPorts: 10,
	}
}

// NewTransport creates a new Transport instance
func NewTransport(cfg Config) *Transport {
	ctx, cancel := context.WithCancel(context.Background())
	return &Transport{
		basePort:  cfg.BasePort,
		numPorts:  cfg.NumPorts,
		listeners: make([]*net.UDPConn, 0, cfg.NumPorts),
		ctx:       ctx,
		cancel:    cancel,
	}
}

// Start initializes all UDP listeners
func (t *Transport) Start() error {
	t.mu.Lock()
	defer t.mu.Unlock()

	for i := 0; i < t.numPorts; i++ {
		addr := &net.UDPAddr{Port: t.basePort + i}
		conn, err := net.ListenUDP("udp", addr)
		if err != nil {
			t.close()
			return fmt.Errorf("failed to start UDP listener on port %d: %w", addr.Port, err)
		}
		t.listeners = append(t.listeners, conn)
		go t.listen(conn)
	}
	return nil
}

// Stop gracefully shuts down the transport
func (t *Transport) Stop() {
	t.cancel()
	t.close()
}

// BasePort returns the base port number for this transport
func (t *Transport) BasePort() int {
	return t.basePort
}

// AddHandler registers a new shred handler
func (t *Transport) AddHandler(h ShredHandler) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.handlers = append(t.handlers, h)
}

// SendShred sends data to the specified address
func (t *Transport) SendShred(data []byte, to *net.UDPAddr) error {
	t.mu.RLock()
	defer t.mu.RUnlock()

	// Try each listener until send succeeds
	var lastErr error
	for _, conn := range t.listeners {
		_, err := conn.WriteToUDP(data, to)
		if err == nil {
			return nil
		}
		lastErr = err
	}
	return fmt.Errorf("failed to send shred: %w", lastErr)
}

func (t *Transport) listen(conn *net.UDPConn) {
	buf := make([]byte, 65507) // Max UDP packet size

	for {
		select {
		case <-t.ctx.Done():
			return
		default:
			n, addr, err := conn.ReadFromUDP(buf)
			if err != nil {
				continue
			}

			data := make([]byte, n)
			copy(data, buf[:n])

			t.mu.RLock()
			for _, h := range t.handlers {
				go h.HandleShred(data, addr)
			}
			t.mu.RUnlock()
		}
	}
}

func (t *Transport) close() {
	t.mu.Lock()
	defer t.mu.Unlock()

	for _, l := range t.listeners {
		l.Close()
	}
	t.listeners = nil
}
