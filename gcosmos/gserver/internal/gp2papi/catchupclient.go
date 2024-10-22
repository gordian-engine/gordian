package gp2papi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"runtime/trace"
	"sync"
	"time"

	"cosmossdk.io/core/transaction"
	"github.com/gordian-engine/gordian/gcosmos/gserver/internal/gsbd"
	"github.com/gordian-engine/gordian/internal/gchan"
	"github.com/gordian-engine/gordian/tm/tmcodec"
	"github.com/gordian-engine/gordian/tm/tmconsensus"
	"github.com/gordian-engine/gordian/tm/tmengine/tmelink"
	libp2phost "github.com/libp2p/go-libp2p/core/host"
	libp2ppeer "github.com/libp2p/go-libp2p/core/peer"
	libp2pprotocol "github.com/libp2p/go-libp2p/core/protocol"
)

type resumeFetchRequest struct {
	Start, Stop uint64
}

type pauseFetchRequest struct{}

type nextPeerRequest struct {
	Resp chan<- libp2ppeer.ID
}

type addPeerRequest struct {
	P libp2ppeer.ID
}

type removePeerRequest struct {
	P libp2ppeer.ID
}

type excludePeerRequest struct {
	P libp2ppeer.ID

	// We could put an exclude reason here,
	// but the log line for what error we got
	// will usually be right before the log line for excluding the peer anyway.
}

type newFetchStateRequest struct {
	Ctx         context.Context
	Start, Stop uint64
}

// CatchupClient manages fetching committed headers,
// and optionally their block data, from peers
// when the engine indicates that the mirror subsystem is lagging
// behind the rest of the network.
type CatchupClient struct {
	log *slog.Logger

	host        libp2phost.Host
	unmarshaler tmcodec.Unmarshaler
	txDecoder   transaction.Codec[transaction.Tx]

	rCache *gsbd.RequestCache

	// Requests that originate externally (should be from the Driver specifically),
	// via calling an exported method on CatchupClient.
	resumeRequests      chan resumeFetchRequest
	pauseRequests       chan pauseFetchRequest
	addPeerRequests     chan addPeerRequest
	removePeerRequests  chan removePeerRequest
	excludePeerRequests chan excludePeerRequest

	// Where we send the committed headers that have fetched.
	replayedHeaders chan<- tmelink.ReplayedHeaderRequest

	// Communication between the main loop and the fetch worker.
	newFetchStateRequests chan newFetchStateRequest
	nextPeerRequests      chan nextPeerRequest

	wg sync.WaitGroup
}

// CatchupClientConfig is the configuration for a [CatchupClient].
type CatchupClientConfig struct {
	// The host from which we will open libp2p streams to other hosts.
	Host libp2phost.Host

	// How to unmarshal Gordian consensus messages.
	Unmarshaler tmcodec.Unmarshaler

	// How to decode SDK transactions encoded in block data.
	TxDecoder transaction.Codec[transaction.Tx]

	// Side channel for block data requests,
	// so that the driver's finalization handler
	// can be notified when block data is available.
	RequestCache *gsbd.RequestCache

	// Where to send the replayed headers.
	// This same channel should be passed to the
	// [tmengine.WithReplayedHeaderRequestChannel] option.
	ReplayedHeadersOut chan<- tmelink.ReplayedHeaderRequest
}

func NewCatchupClient(
	ctx context.Context,
	log *slog.Logger,
	cfg CatchupClientConfig,
) *CatchupClient {
	c := &CatchupClient{
		log: log,

		host: cfg.Host,

		unmarshaler: cfg.Unmarshaler,
		txDecoder:   cfg.TxDecoder,

		rCache: cfg.RequestCache,

		resumeRequests: make(chan resumeFetchRequest),
		pauseRequests:  make(chan pauseFetchRequest),

		// Arbitrarily sized.
		addPeerRequests:     make(chan addPeerRequest, 8),
		removePeerRequests:  make(chan removePeerRequest, 8),
		excludePeerRequests: make(chan excludePeerRequest, 8),

		replayedHeaders: cfg.ReplayedHeadersOut,

		// 1-buffered so we don't block the main loop on the first send.
		newFetchStateRequests: make(chan newFetchStateRequest, 1),

		nextPeerRequests: make(chan nextPeerRequest),
	}

	c.wg.Add(2)
	go c.mainLoop(ctx)
	go c.fetchWorker(ctx)

	return c
}

func (c *CatchupClient) mainLoop(ctx context.Context) {
	defer c.wg.Done()

	ctx, task := trace.NewTask(ctx, "gp2papi.CatchupClient.mainLoop")
	defer task.End()

	fetchCtx, fetchCancel := context.WithCancelCause(ctx)
	defer func() {
		// Ensure the fetch is cancelled on all return paths.
		// Wrapped inside an anonymous function to avoid closing over initial value.
		fetchCancel(nil)
	}()

	peers := make(map[libp2ppeer.ID]struct{})

	// NOTE: the excludedPeers map never shrinks,
	// and entries are never removed from it.
	// So, this is a possible attack vector if the added peers are unbounded
	// (that map may shrink upon a call to RemovePeer)
	// and the excluded map repeatedly grows.
	excludedPeers := make(map[libp2ppeer.ID]struct{})

	var requestBlockedOnMissingPeer *nextPeerRequest
	for {
		// Normally we want to accept peer requests,
		// but if we are blocked by lack of peers,
		// then we will not accept any new next peer requests.
		nextPeerReqCh := c.nextPeerRequests
		if requestBlockedOnMissingPeer != nil {
			nextPeerReqCh = nil
		}

		select {
		case <-ctx.Done():
			c.log.Info("Stopping due to context cancellation", "cause", context.Cause(ctx))
			return

		case req := <-c.resumeRequests:
			if !gchan.SendC(
				ctx, c.log,
				c.newFetchStateRequests, newFetchStateRequest{
					Ctx:   fetchCtx,
					Start: req.Start,
					Stop:  req.Stop,
				},
				"making new fetch state request",
			) {
				return
			}

		case <-c.pauseRequests:
			fetchCancel(errFetchPause)

			fetchCtx, fetchCancel = context.WithCancelCause(ctx)

		case req := <-nextPeerReqCh:
			if len(peers) == 0 {
				c.log.Warn("Next peer request blocked due to lack of peers")
				requestBlockedOnMissingPeer = &req
				continue
			}

			// Rely on map iteration to send a peer at random.
			for p := range peers {
				// Response must be 1-buffered, having originated from the fetch worker goroutine.
				req.Resp <- p
				break
			}

		case req := <-c.addPeerRequests:
			if _, ok := excludedPeers[req.P]; ok {
				// Don't add a peer who is in the exclusion list.
				continue
			}

			peers[req.P] = struct{}{}

			// Send immediately to the blocked request,
			// if a blocked request exists.
			if requestBlockedOnMissingPeer != nil {
				requestBlockedOnMissingPeer.Resp <- req.P
				requestBlockedOnMissingPeer = nil
				c.log.Info(
					"Unblocked next peer request",
					"peer_id", req.P,
				)
			}

		case req := <-c.removePeerRequests:
			delete(peers, req.P)

		case req := <-c.excludePeerRequests:
			// Remove from active peers if they are in that list.
			delete(peers, req.P)

			// And record in the exclusion list,
			// so they can't get added again later.
			excludedPeers[req.P] = struct{}{}

			c.log.Info("Excluding peer", "peer_id", req.P)
		}
	}
}

var errFetchPause = errors.New("fetches paused")

// fetchWorker handles committed header and block data fetching on a dedicated goroutine.
func (c *CatchupClient) fetchWorker(ctx context.Context) {
	defer c.wg.Done()

	ctx, task := trace.NewTask(ctx, "gp2papi.CatchupClient.fetchWorker")
	defer task.End()

	for {
		select {
		case <-ctx.Done():
			c.log.Info("Fetch worker stopping due to context cancellation", "cause", context.Cause(ctx))
			return

		case startReq := <-c.newFetchStateRequests:
			fetchCtx := startReq.Ctx
			c.doFetches(fetchCtx, startReq.Start, startReq.Stop)
		}
	}
}

func (c *CatchupClient) doFetches(ctx context.Context, start, stop uint64) {
	defer trace.StartRegion(ctx, "doFetches").End()

	height := start
	for stop == 0 || (stop > 0 && height < stop) {
		respCh := make(chan libp2ppeer.ID, 1)
		p, ok := gchan.ReqResp(
			ctx, c.log,
			c.nextPeerRequests, nextPeerRequest{Resp: respCh},
			respCh,
			"requesting next peer for fetch",
		)
		if !ok {
			c.log.Info(
				"Committed header fetch interrupted",
				"height", height,
				"cause", context.Cause(ctx),
			)
			return
		}

		res := c.doFetch(ctx, height, p)
		if res.ExcludePeer {
			// This should be an exceptional case,
			// so let's go ahead and do a blocking here.
			// Note, the c.excludePeerRequests channel is buffered,
			// so it is possible that we send this
			// and the main loop does not process that request
			// before we make the next peer request.
			// In that case, if we make two exclude peer requests,
			// the second one will be a no-op.
			if !gchan.SendC(
				ctx, c.log,
				c.excludePeerRequests, excludePeerRequest{P: p},
				"sending exclude peer request",
			) {
				return
			}
			continue
		}

		if !res.Success {
			// Try again with the same height,
			// and hopefully a new peer.
			continue
		}

		// We succeeded, so we can increment the height now.
		height++

		// TODO: should do a non-blocking check on c.newFetchStateRequests here,
		// in case the stop value has been adjusted.
	}
}

// fetchResult is the outcome of a call to [*CatchupClient.doFetch].
type fetchResult struct {
	Success bool

	// Implies that it can be retried.
	ExcludePeer bool
}

var errFetchHeaderDeadlineExceeded = errors.New("deadline for retrieving header exceeded")

// doFetch executes a single committed header and block data fetch,
// at the given height, from the given peer.
func (c *CatchupClient) doFetch(ctx context.Context, height uint64, p libp2ppeer.ID) fetchResult {
	defer trace.StartRegion(ctx, "doFetch").End()

	const timeout = 2 * time.Second // Arbitrarily chosen.
	streamCtx, cancel := context.WithTimeoutCause(
		ctx, timeout, errFetchHeaderDeadlineExceeded,
	)
	defer cancel()

	// If we want to support a header-only CatchupClient,
	// we would need to use headerV1HeightPrefix here.
	s, err := c.host.NewStream(streamCtx, p, libp2pprotocol.ID(
		fmt.Sprintf("%s%d", fullBlockV1HeightPrefix, height),
	))
	if err != nil {
		c.log.Info("Failed to open stream to peer", "peer_id", p, "err", err)
		return fetchResult{
			ExcludePeer: true,
		}
	}
	defer s.Close()

	// We have a stream to the right protocol, let's parse the result.
	// Arbitrary limit on header size.
	var res JSONResult
	err = json.NewDecoder(io.LimitReader(s, 16*1024)).Decode(&res)
	_ = s.Close() // Nothing left to do with stream.
	cancel()      // And free up any streamCtx resources as early as possible.
	if err != nil {
		c.log.Info(
			"Failed to parse stream response from peer",
			"peer_id", p,
			"height", height,
			"err", err,
		)
		return fetchResult{
			ExcludePeer: true,
		}
	}

	if res.Err != "" {
		if res.Err == "height unknown" {
			// Special case of the height isn't ready.
			// Assume we trust this peer.
			// Just back off slightly.
			// Nothing to log in this case.
			t := time.NewTimer(time.Second)
			defer t.Stop()
			select {
			case <-ctx.Done():
			case <-t.C:
			}
		} else {
			c.log.Info(
				"Got error response from peer",
				"peer_id", p,
				"height", height,
				"err", res.Err,
			)
		}

		return fetchResult{
			// They sent a valid error back,
			// so we aren't going to exclude them on these grounds.
		}
	}

	var fbr FullBlockResult
	if err := json.Unmarshal(res.Result, &fbr); err != nil {
		c.log.Info(
			"Failed to parse full block result",
			"peer_id", p,
			"height", height,
			"err", err,
		)
		return fetchResult{
			ExcludePeer: true,
		}
	}

	// Parsed the entire result,
	// now parse the committed header.
	var ch tmconsensus.CommittedHeader
	if err := c.unmarshaler.UnmarshalCommittedHeader(fbr.Header, &ch); err != nil {
		c.log.Info(
			"Failed to parse header from result",
			"peer_id", p,
			"height", height,
			"err", err,
		)
		return fetchResult{
			ExcludePeer: true,
		}
	}

	// Confirm that the block data is appropriate for the header.
	if gsbd.IsZeroTxDataID(string(ch.Header.DataID)) {
		if len(fbr.BlockData) > 0 {
			c.log.Info(
				"Got non-empty block data for zero data ID",
				"peer_id", p,
				"height", height,
				"data_id", ch.Header.DataID,
				"block_data_size", len(fbr.BlockData),
			)
			return fetchResult{
				ExcludePeer: true,
			}
		}
	} else {
		if len(fbr.BlockData) == 0 {
			c.log.Info(
				"Got empty block data for non-zero data ID",
				"peer_id", p,
				"height", height,
				"data_id", ch.Header.DataID,
			)
			return fetchResult{
				ExcludePeer: true,
			}
		}
	}

	if len(fbr.BlockData) > 0 {
		dec, err := gsbd.NewBlockDataDecoder(string(ch.Header.DataID), c.txDecoder)
		if err != nil {
			c.log.Info(
				"Got error when creating block decoder",
				"peer_id", p,
				"height", height,
				"err", err,
			)
			return fetchResult{
				ExcludePeer: true,
			}
		}

		txs, err := dec.Decode(bytes.NewReader(fbr.BlockData))
		if err != nil {
			c.log.Info(
				"Got error when parsing block data",
				"peer_id", p,
				"height", height,
				"err", err,
			)
			return fetchResult{
				ExcludePeer: true,
			}
		}

		// Since we have the block data and it matches the header's data ID,
		// we can set it in the request cache as completed.
		c.rCache.SetImmediatelyAvailable(string(ch.Header.DataID), txs, fbr.BlockData)
	}

	// Now we have a committed header, so we have to send it to the engine.
	respCh := make(chan tmelink.ReplayedHeaderResponse, 1)
	req := tmelink.ReplayedHeaderRequest{
		Header: ch.Header,
		Proof:  ch.Proof,
		Resp:   respCh,
	}
	resp, ok := gchan.ReqResp(
		ctx, c.log,
		c.replayedHeaders, req,
		respCh,
		"sending replayed header to engine",
	)
	if !ok {
		// Context was cancelled, so result is meaningless here.
		return fetchResult{}
	}

	if resp.Err != nil {
		// TODO: this should special case the expected error types
		// that replayed header requests are documented to return.
		c.log.Info(
			"Got error when applying replayed header",
			"peer_id", p,
			"height", height,
			"err", resp.Err,
		)
		return fetchResult{
			ExcludePeer: true,
		}
	}

	return fetchResult{
		Success: true,
	}
}

// Wait blocks until all of c's background work is completed.
// Initiate a shutdown by canceling the context passed to [NewCatchupClient].
func (c *CatchupClient) Wait() {
	c.wg.Wait()
}

// ResumeFetching requests that committed header and block data fetches are restarted,
// or it can indicate that the start and stop height values have changed.
func (c *CatchupClient) ResumeFetching(
	ctx context.Context,
	startHeight, stopHeight uint64,
) (ok bool) {
	return gchan.SendC(
		ctx, c.log,
		c.resumeRequests, resumeFetchRequest{
			Start: startHeight,
			Stop:  stopHeight,
		},
		"making resume fetch request",
	)
}

// PauseFetching requests that outstanding committed header fetches are interrupted.
func (c *CatchupClient) PauseFetching(ctx context.Context) (ok bool) {
	return gchan.SendC(
		ctx, c.log,
		c.pauseRequests, pauseFetchRequest{},
		"making pause fetch request",
	)
}

// AddPeer requests to add the given peer ID as a candidate peer
// for fetching committed headers and block data.
func (c *CatchupClient) AddPeer(ctx context.Context, p libp2ppeer.ID) (ok bool) {
	return gchan.SendC(
		ctx, c.log,
		c.addPeerRequests, addPeerRequest{P: p},
		"making add peer request",
	)
}

// RemovePeer requests to remove the given peer ID as a candidate peer
// for fetching committed headers and block data.
func (c *CatchupClient) RemovePeer(ctx context.Context, p libp2ppeer.ID) (ok bool) {
	return gchan.SendC(
		ctx, c.log,
		c.removePeerRequests, removePeerRequest{P: p},
		"making remove peer request",
	)
}
