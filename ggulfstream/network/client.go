package network

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"

	"github.com/gordian-engine/gordian/tm/tmp2p"
)

// Client handles sending transactions to proposers over the network
type Client struct {
	log     *slog.Logger
	conn    tmp2p.Connection
	maxRetries int

	// Statistics
	stats Stats

	// Active sends tracking
	activeSends sync.WaitGroup

	// Lifecycle
	ctx     context.Context
	cancel  context.CancelFunc
}

// Config configures the network client
type Config struct {
	MaxRetries int
	Log        *slog.Logger
}

// DefaultConfig returns default configuration
func DefaultConfig() *Config {
	return &Config{
		MaxRetries: 3,
	}
}

// New creates a new network client
func New(ctx context.Context, conn tmp2p.Connection, cfg *Config) (*Client, error) {
	if cfg == nil {
		cfg = DefaultConfig()
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	if conn == nil {
		return nil, fmt.Errorf("p2p connection required")
	}

	ctx, cancel := context.WithCancel(ctx)

	return &Client{
		log:        cfg.Log,
		conn:       conn,
		maxRetries: cfg.MaxRetries,
		ctx:        ctx,
		cancel:     cancel,
	}, nil
}

// SendTransactionBatch sends a batch of transactions to a proposer
func (c *Client) SendTransactionBatch(batch *TransactionBatch) error {
	if err := c.ctx.Err(); err != nil {
		return fmt.Errorf("client is closed: %w", err)
	}

	// Track active send
	c.activeSends.Add(1)
	atomic.AddUint32(&c.stats.ActiveSends, 1)
	defer func() {
		c.activeSends.Done()
		atomic.AddUint32(&c.stats.ActiveSends, ^uint32(0))
	}()

	// TODO: Implement proper transaction broadcast
	// This will need:
	// 1. Integration with Gordian's message types
	// 2. Proper use of ConsensusBroadcaster
	// 3. Handling of responses/acknowledgments

	atomic.AddUint64(&c.stats.BatchesSent, 1)
	atomic.AddUint64(&c.stats.TxSent, uint64(len(batch.Transactions)))

	return nil
}

// GetStats returns current statistics
func (c *Client) GetStats() Stats {
	return Stats{
		BatchesSent: atomic.LoadUint64(&c.stats.BatchesSent),
		TxSent:      atomic.LoadUint64(&c.stats.TxSent),
		SendErrors:  atomic.LoadUint64(&c.stats.SendErrors),
		ActiveSends: atomic.LoadUint32(&c.stats.ActiveSends),
	}
}

// Close shuts down the client
func (c *Client) Close() error {
	c.cancel()
	c.activeSends.Wait()
	return nil
}