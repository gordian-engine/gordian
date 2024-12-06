package ggulfstream

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/gordian-engine/gordian/gdriver/gtxbuf"
	"github.com/gordian-engine/gordian/ggulfstream/forwarder"
	"github.com/gordian-engine/gordian/ggulfstream/network"
	"github.com/gordian-engine/gordian/ggulfstream/types"
	"github.com/gordian-engine/gordian/tm/tmp2p"
)

// GulfStream coordinates transaction forwarding to potential proposers.
type GulfStream[S, T any] struct {
	log    *slog.Logger
	buffer *gtxbuf.Buffer[S, T]

	// Component management
	forwarder *forwarder.Forwarder[S, T]
	client    *network.Client

	// Async update channels
	roundUpdateCh chan types.RoundUpdate

	// Lifecycle management
	ctx       context.Context
	cancel    context.CancelFunc
	closeOnce sync.Once
	wg        sync.WaitGroup
	done      chan struct{}
}

// Options configures a new GulfStream instance
type Options struct {
	Log             *slog.Logger
	RoundUpdates    chan types.RoundUpdate
	Connection      tmp2p.Connection // P2P network connection
	ForwarderConfig *forwarder.Config
	NetworkConfig   *network.Config
}

// New creates a new GulfStream instance
func New[S, T any](
	ctx context.Context,
	buffer *gtxbuf.Buffer[S, T],
	opts *Options,
) (*GulfStream[S, T], error) {
	if opts == nil {
		return nil, fmt.Errorf("options required")
	}
	if opts.RoundUpdates == nil {
		return nil, fmt.Errorf("round updates channel required")
	}
	if opts.Connection == nil {
		return nil, fmt.Errorf("p2p connection required")
	}
	if opts.Log == nil {
		opts.Log = slog.Default()
	}

	// Create cancellable context
	ctx, cancel := context.WithCancel(ctx)

	// Initialize network client
	client, err := network.New(ctx, opts.Connection, opts.NetworkConfig)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("create network client: %w", err)
	}

	// Initialize forwarder
	fwd, err := forwarder.New(ctx, buffer, client, opts.Log, opts.ForwarderConfig)
	if err != nil {
		cancel()
		client.Close()
		return nil, fmt.Errorf("create forwarder: %w", err)
	}

	gs := &GulfStream[S, T]{
		log:           opts.Log,
		buffer:        buffer,
		client:        client,
		forwarder:     fwd,
		roundUpdateCh: opts.RoundUpdates,
		ctx:           ctx,
		cancel:        cancel,
		done:          make(chan struct{}),
	}

	// Start update processing
	gs.wg.Add(1)
	go gs.processUpdates()

	return gs, nil
}

// processUpdates handles incoming consensus updates
func (gs *GulfStream[S, T]) processUpdates() {
	defer gs.wg.Done()

	for {
		select {
		case <-gs.ctx.Done():
			return
		case update := <-gs.roundUpdateCh:
			if err := gs.forwarder.HandleRoundUpdate(update); err != nil {
				gs.log.Error("failed to handle round update",
					"error", err,
					"height", update.Height,
					"round", update.Round)
			}
		}
	}
}

// Stats returns current Gulf Stream statistics
type Stats struct {
	ForwarderStats forwarder.Stats
	NetworkStats   network.Stats
}

// GetStats returns current statistics
func (gs *GulfStream[S, T]) GetStats() Stats {
	return Stats{
		ForwarderStats: gs.forwarder.GetStats(),
		NetworkStats:   gs.client.GetStats(),
	}
}

// Close initiates shutdown
func (gs *GulfStream[S, T]) Close() error {
	var err error
	gs.closeOnce.Do(func() {
		gs.cancel()

		if e := gs.forwarder.Close(); e != nil {
			err = fmt.Errorf("forwarder close: %w", e)
		}
		if e := gs.client.Close(); e != nil {
			if err != nil {
				err = fmt.Errorf("%v; client close: %w", err, e)
			} else {
				err = fmt.Errorf("client close: %w", e)
			}
		}

		gs.wg.Wait()
		close(gs.done)
	})
	return err
}

// Wait blocks until shutdown is complete
func (gs *GulfStream[S, T]) Wait() {
	<-gs.done
}
