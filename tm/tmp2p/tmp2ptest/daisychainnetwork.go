package tmp2ptest

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/gordian-engine/gordian/gexchange"
	"github.com/gordian-engine/gordian/internal/gchan"
	"github.com/gordian-engine/gordian/internal/gtest"
	"github.com/gordian-engine/gordian/tm/tmconsensus"
	"github.com/gordian-engine/gordian/tm/tmp2p"
)

// DaisyChain is an in-memory network modeled as a series of point-to-point connections.
//
// For a network shaped like
//
//	A --- B --- C --- D --- E
//
// If an outgoing message is sent from C, it will be sent to nodes B and D first.
// If B rejects or ignores the message, the message will not propagate to A;
// otherwise if B accepts the message, the message propagates to A.
// The identical message sent to D behaves the same:
// accepting the message propagates it to E,
// otherwise the message is discarded and E will not see it.
type DaisyChainNetwork struct {
	log *slog.Logger

	newConnRequests chan dcConnectRequest

	done chan struct{}
}

type dcConnectRequest struct {
	result chan *DaisyChainConnection
}

type dcSetHandlerRequest struct {
	H tmconsensus.ConsensusHandler

	Ready chan struct{}
}

// NewDaisyChainNetwork returns a new DaisyChainNetwork.
// Cancelling the context will stop the network and disconnect all created connections.
func NewDaisyChainNetwork(t *testing.T, ctx context.Context) *DaisyChainNetwork {
	n := &DaisyChainNetwork{
		log: gtest.NewLogger(t).With("net_idx", atomic.AddUint64(&dcNetworkIdxCounter, 1)),

		newConnRequests: make(chan dcConnectRequest), // Unbuffered since this is effectively synchronous.

		done: make(chan struct{}),
	}

	go n.kernel(ctx)
	return n
}

func (n *DaisyChainNetwork) kernel(ctx context.Context) {
	defer close(n.done)

	var conns []*DaisyChainConnection
	for {
		select {
		case <-ctx.Done():
			n.log.Debug("Network closing")

			// Range over all the conns for each step in this sequence,
			// so that if one step is slow, we don't block the others from shutting down.
			for _, c := range conns {
				c.Disconnect()
			}
			for _, c := range conns {
				<-c.Disconnected()
			}
			for _, c := range conns {
				c.wait()
			}

			return

		case req := <-n.newConnRequests:
			idx := atomic.AddUint64(&dcConnIdxCounter, 1)

			conn := n.newConn(ctx, idx)

			if len(conns) > 0 {
				conns[len(conns)-1].pairRight(ctx, conn)
			}
			conns = append(conns, conn)

			// Don't start the background work until after we've paired right,
			// otherwise there is a data race on the left channels of the new connection.
			go conn.background(ctx)

			req.result <- conn
		}
	}
}

// Connect creates and returns a new connection.
func (n *DaisyChainNetwork) Connect(ctx context.Context) (*DaisyChainConnection, error) {
	req := dcConnectRequest{
		result: make(chan *DaisyChainConnection, 1),
	}

	select {
	case <-ctx.Done():
		return nil, fmt.Errorf("context finished while creating connection to network: %w", context.Cause(ctx))
	case n.newConnRequests <- req:
		// Okay.
	}

	select {
	case <-ctx.Done():
		return nil, fmt.Errorf("context finished while waiting for connection to be created: %w", context.Cause(ctx))
	case conn := <-req.result:
		return conn, nil
	}
}

// AddDriverAnnotations is a no-op for the DaisyChainNetwork.
func (n *DaisyChainNetwork) AddDriverAnnotations(context.Context, tmp2p.Connection, *tmconsensus.ProposedHeader) error {
	return nil
}

// Stabilize is a no-op for the DaisyChainNetwork.
func (n *DaisyChainNetwork) Stabilize(context.Context) error {
	return nil
}

// Wait blocks until all of n's background work completes.
// Initiate shutdown by canceling the context passed to [NewDaisyChainNetwork].
func (n *DaisyChainNetwork) Wait() {
	<-n.done
}

func (n *DaisyChainNetwork) newConn(ctx context.Context, idx uint64) *DaisyChainConnection {
	c := &DaisyChainConnection{
		log: n.log.With("conn_idx", idx),
		idx: idx,

		// Unbuffered is fine since these are effectively synchronous calls.
		setHandlerRequests: make(chan dcSetHandlerRequest),
		pairRightRequests:  make(chan dcPairRightRequest),

		// Arbitrarily sizing with dcMessageBufSize.
		outgoingPHs:        make(chan tmconsensus.ProposedHeader, dcMessageBufSize),
		outgoingPrevotes:   make(chan tmconsensus.PrevoteSparseProof, dcMessageBufSize),
		outgoingPrecommits: make(chan tmconsensus.PrecommitSparseProof, dcMessageBufSize),

		disconnectReq: make(chan struct{}),
		disconnected:  make(chan struct{}),

		done: make(chan struct{}),
	}
	return c
}

// DaisyChainConnection is one node in a [DaisyChainNetwork].
type DaisyChainConnection struct {
	log *slog.Logger

	idx uint64

	setHandlerRequests chan dcSetHandlerRequest
	pairRightRequests  chan dcPairRightRequest

	outgoingPHs        chan tmconsensus.ProposedHeader
	outgoingPrevotes   chan tmconsensus.PrevoteSparseProof
	outgoingPrecommits chan tmconsensus.PrecommitSparseProof

	// Set during call to pairRight.
	fromLeft, toLeft chan dcMessage

	disconnectOnce sync.Once
	disconnectReq  chan struct{}
	disconnected   chan struct{}

	done chan struct{}
}

// dccbWrapper wraps a DaisyChainConnection as a tmp2p.ConsensusBroadcaster.
type dccbWrapper struct {
	c *DaisyChainConnection
}

func (w dccbWrapper) OutgoingProposedHeaders() chan<- tmconsensus.ProposedHeader {
	return w.c.outgoingPHs
}
func (w dccbWrapper) OutgoingPrevoteProofs() chan<- tmconsensus.PrevoteSparseProof {
	return w.c.outgoingPrevotes
}
func (w dccbWrapper) OutgoingPrecommitProofs() chan<- tmconsensus.PrecommitSparseProof {
	return w.c.outgoingPrecommits
}

func (c *DaisyChainConnection) ConsensusBroadcaster() tmp2p.ConsensusBroadcaster {
	return dccbWrapper{c: c}
}

func (c *DaisyChainConnection) SetConsensusHandler(ctx context.Context, h tmconsensus.ConsensusHandler) {
	req := dcSetHandlerRequest{
		H:     h,
		Ready: make(chan struct{}),
	}

	_, _ = gchan.ReqResp(
		ctx, c.log,
		c.setHandlerRequests, req,
		req.Ready,
		"updating connection's consensus handler",
	)
}

func (c *DaisyChainConnection) Disconnect() {
	c.disconnectOnce.Do(func() {
		close(c.disconnectReq)
	})
}

func (c *DaisyChainConnection) Disconnected() <-chan struct{} {
	return c.disconnected
}

type dcPairRightRequest struct {
	NewConn *DaisyChainConnection
	Done    chan struct{}
}

type dcMessage struct {
	srcIdx uint64

	// Exactly one of the following fields should be set.
	PH        *tmconsensus.ProposedHeader
	Prevote   *tmconsensus.PrevoteSparseProof
	Precommit *tmconsensus.PrecommitSparseProof
}

const dcMessageBufSize = 16 // Arbitrary.

// pairRight connects the right channels on n with the left channels on newNode.
func (c *DaisyChainConnection) pairRight(ctx context.Context, newConn *DaisyChainConnection) {
	req := dcPairRightRequest{
		NewConn: newConn,

		Done: make(chan struct{}),
	}

	_, _ = gchan.ReqResp(
		ctx, c.log,
		c.pairRightRequests, req,
		req.Done,
		"pairing last connection with new connection",
	)
}

func (c *DaisyChainConnection) background(ctx context.Context) {
	defer close(c.done)

	var h tmconsensus.ConsensusHandler
	var disconnected bool

	var toRight, fromRight chan dcMessage

	// Local value so we can set it to nil to avoid selecting against it
	// after it's been closed.
	disconnectReqCh := c.disconnectReq

	defer func() {
		// There are multiple return paths, so ensure we close the disconnected channel once the loop finishes.

		// Consume the disconnectOnce as soon as possible,
		// so that a separate call to c.Disconnect() will not block.
		c.disconnectOnce.Do(func() {})

		if !disconnected {
			close(c.disconnected)
		}
	}()

	for {
		select {
		case <-ctx.Done():
			c.log.Info("Stopping due to context cancellation", "cause", context.Cause(ctx))
			return

		case req := <-c.pairRightRequests:
			if toRight != nil || fromRight != nil {
				panic(errors.New(
					"BUG: pairRight called more than once",
				))
			}

			toRight = make(chan dcMessage, dcMessageBufSize)
			req.NewConn.fromLeft = toRight

			fromRight = make(chan dcMessage, dcMessageBufSize)
			req.NewConn.toLeft = fromRight

			close(req.Done)

		case req := <-c.setHandlerRequests:
			if disconnected {
				panic(errors.New("BUG: SetConsensusHandler called after Disconnect"))
			}

			h = req.H
			close(req.Ready)

		case <-disconnectReqCh:
			h = nil
			disconnected = true
			disconnectReqCh = nil
			close(c.disconnected)

		case msg := <-c.fromLeft:
			if h == nil {
				// No handler. Can we propagate the message rightwards?
				if toRight == nil {
					continue
				}

				// There is a connection to the right. Pass the message through.
				if !gchan.SendC(
					ctx, c.log,
					toRight, msg,
					"propagating message to right without handler",
				) {
					return
				}

				// Nil handler and it's been propagated,
				// so wait for the next signal.
				continue
			}

			// We have a non-nil handler.
			// For now block our main loop handling it,
			// but we could potentially push this to a worker goroutine.
			c.handleMessage(ctx, msg, h, toRight, "right")

		case msg := <-fromRight:
			if h == nil {
				// No handler. Can we propagate the message leftwards?
				if c.toLeft == nil {
					continue
				}

				// There is a connection to the left. Pass the message through.
				if !gchan.SendC(
					ctx, c.log,
					c.toLeft, msg,
					"propagating message to left without handler",
				) {
					return
				}

				// Nil handler and it's been propagated,
				// so wait for the next signal.
				continue
			}

			// We have a non-nil handler.
			// For now block our main loop handling it,
			// but we could potentially push this to a worker goroutine.
			c.handleMessage(ctx, msg, h, c.toLeft, "left")

		case ph := <-c.outgoingPHs:
			msg := dcMessage{
				srcIdx: c.idx,

				PH: &ph,
			}
			if !c.propagateMessage(ctx, msg, c.toLeft, toRight) {
				return
			}

		case prevote := <-c.outgoingPrevotes:
			msg := dcMessage{
				srcIdx: c.idx,

				Prevote: &prevote,
			}
			if !c.propagateMessage(ctx, msg, c.toLeft, toRight) {
				return
			}

		case precommit := <-c.outgoingPrecommits:
			msg := dcMessage{
				srcIdx: c.idx,

				Precommit: &precommit,
			}
			if !c.propagateMessage(ctx, msg, c.toLeft, toRight) {
				return
			}
		}
	}
}

func (c *DaisyChainConnection) wait() {
	<-c.done
}

// handleMessage handles msg with the appropriate method on h,
func (c *DaisyChainConnection) handleMessage(
	ctx context.Context,
	msg dcMessage,
	h tmconsensus.ConsensusHandler,
	outCh chan dcMessage,
	dir string,
) {
	// Assume h is non-nil if this method is being called.
	switch {
	case msg.PH != nil:
		if h.HandleProposedHeader(ctx, *msg.PH) != gexchange.FeedbackAccepted {
			return
		}
	case msg.Prevote != nil:
		if h.HandlePrevoteProofs(ctx, *msg.Prevote) != gexchange.FeedbackAccepted {
			return
		}
	case msg.Precommit != nil:
		if h.HandlePrecommitProofs(ctx, *msg.Precommit) != gexchange.FeedbackAccepted {
			return
		}
	default:
		panic(errors.New("BUG: no proposed block, prevote, or precommit set in dcMessage"))
	}

	// No more work to do if we can't propagate the message leftwards.
	if outCh == nil {
		return
	}

	// It was accepted and we have an output channel.
	// Block propagating the message.
	_ = gchan.SendC(
		ctx, c.log,
		outCh, msg,
		"propagating message to the "+dir+" after handling and accepting it",
	)
}

// Propagate message sends msg to all of the non-nil channels in toLeft and toRight.
func (c *DaisyChainConnection) propagateMessage(
	ctx context.Context,
	msg dcMessage,
	toLeft, toRight chan dcMessage,
) (ok bool) {
	for toLeft != nil || toRight != nil {
		select {
		case <-ctx.Done():
			return false

		case toLeft <- msg:
			toLeft = nil

		case toRight <- msg:
			toRight = nil
		}
	}

	return true
}

// Atomic counters used for sequencing certain identifiers within the daisy chain types.
var (
	dcNetworkIdxCounter uint64
	dcConnIdxCounter    uint64
)
