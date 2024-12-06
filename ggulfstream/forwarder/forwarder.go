package forwarder

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"

	"github.com/gordian-engine/gordian/gdriver/gtxbuf"
	"github.com/gordian-engine/gordian/ggulfstream/network"
	"github.com/gordian-engine/gordian/ggulfstream/types"
)

// Config holds forwarder configuration
type Config struct {
	MaxBatchSize       int
	MaxConcurrentSends int
}

// DefaultConfig returns default configuration values
func DefaultConfig() *Config {
	return &Config{
		MaxBatchSize:       100,
		MaxConcurrentSends: 4,
	}
}

// Forwarder manages transaction forwarding to potential proposers
type Forwarder[S, T any] struct {
	log    *slog.Logger
	cfg    *Config
	buffer *gtxbuf.Buffer[S, T]
	client *network.Client

	// Track proposers and transaction state
	proposers   *priorityQueue
	txSentTo    sync.Map // map[string]map[string]struct{} // txHash -> nodeIDs
	txCommitted sync.Map // map[string]struct{} // txHash -> exists

	// Stats
	txForwarded atomic.Uint64
	txDropped   atomic.Uint64

	// Lifecycle
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	done    chan struct{}
}

// New creates a new transaction forwarder
func New[S, T any](
	ctx context.Context,
	buffer *gtxbuf.Buffer[S, T],
	client *network.Client,
	log *slog.Logger,
	cfg *Config,
) (*Forwarder[S, T], error) {
	if cfg == nil {
		cfg = DefaultConfig()
	}
	if log == nil {
		log = slog.Default()
	}
	if client == nil {
		return nil, fmt.Errorf("network client required")
	}

	ctx, cancel := context.WithCancel(ctx)

	f := &Forwarder[S, T]{
		log:       log,
		cfg:       cfg,
		buffer:    buffer,
		client:    client,
		proposers: newPriorityQueue(),
		ctx:       ctx,
		cancel:    cancel,
		done:      make(chan struct{}),
	}

	f.wg.Add(1)
	go f.processBuffer()

	return f, nil
}

// HandleRoundUpdate processes a consensus state update
func (f *Forwarder[S, T]) HandleRoundUpdate(update types.RoundUpdate) error {
	// Update committed transaction tracking
	for _, txHash := range update.CommittedTxs {
		f.txCommitted.Store(string(txHash), struct{}{})
	}

	// Update proposer list
	f.proposers.Update(update.NextProposers)

	// Trigger rebroadcast of pending transactions
	return f.broadcastPendingTransactions()
}

func (f *Forwarder[S, T]) broadcastPendingTransactions() error {
	proposers := f.proposers.GetAll()
	if len(proposers) == 0 {
		return nil
	}

	// Get buffered transactions
	txs := f.buffer.Buffered(f.ctx, nil)
	if len(txs) == 0 {
		return nil
	}

	var errs []error
	for _, prop := range proposers {
		if err := f.forwardBatch(prop, txs); err != nil {
			errs = append(errs, fmt.Errorf("forward to %s: %w", prop.NodeID, err))
			continue
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("broadcast errors: %v", errs)
	}
	return nil
}

func (f *Forwarder[S, T]) forwardBatch(proposer types.Proposer, txs []T) error {
	// Convert transactions to network format
	batch := make([][]byte, 0, len(txs))
	for _, tx := range txs {
		// TODO: Implement actual transaction serialization
		batch = append(batch, []byte(fmt.Sprintf("%v", tx)))
	}

	networkBatch := &network.TransactionBatch{
		Transactions: batch,
		NodeID:      proposer.NodeID,
		Height:      proposer.Height,
		Round:       proposer.Round,
	}

	if err := f.client.SendTransactionBatch(networkBatch); err != nil {
		f.txDropped.Add(uint64(len(txs)))
		return fmt.Errorf("send batch: %w", err)
	}

	// Update sent tracking
	for _, tx := range txs {
		txKey := fmt.Sprintf("%v", tx) // TODO: Get actual tx hash
		if sentMap, loaded := f.txSentTo.LoadOrStore(txKey, make(map[string]struct{})); loaded {
			sentMap.(map[string]struct{})[proposer.NodeID] = struct{}{}
		}
	}

	f.txForwarded.Add(uint64(len(txs)))
	return nil
}

func (f *Forwarder[S, T]) processBuffer() {
	defer f.wg.Done()
	defer close(f.done)

	for {
		select {
		case <-f.ctx.Done():
			return
		default:
			if err := f.broadcastPendingTransactions(); err != nil {
				f.log.Error("broadcast failed", "error", err)
			}
		}
	}
}

// Stats returns current forwarder statistics
type Stats struct {
	TxForwarded  uint64
	TxDropped    uint64
	NetworkStats network.Stats
}

// GetStats returns current statistics
func (f *Forwarder[S, T]) GetStats() Stats {
	return Stats{
		TxForwarded:  f.txForwarded.Load(),
		TxDropped:    f.txDropped.Load(),
		NetworkStats: f.client.GetStats(),
	}
}

// Close shuts down the forwarder
func (f *Forwarder[S, T]) Close() error {
	f.cancel()
	f.wg.Wait()
	return nil
}

// Wait blocks until shutdown is complete
func (f *Forwarder[S, T]) Wait() {
	<-f.done
}