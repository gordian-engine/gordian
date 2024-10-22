package gsi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"sync"

	"cosmossdk.io/core/transaction"
	"github.com/gordian-engine/gordian/gcosmos/gserver/internal/gsbd"
	"github.com/gordian-engine/gordian/internal/gchan"
	libp2phost "github.com/libp2p/go-libp2p/core/host"
	libp2ppeer "github.com/libp2p/go-libp2p/core/peer"
	libp2pprotocol "github.com/libp2p/go-libp2p/core/protocol"
)

// pbdP2PFetchRequest is sent from [*PBDRetriever.Retrieve] to the main loop
// indicating a proposed block has data that must be fetched over libp2p.
type pbdP2PFetchRequest struct {
	DataID string
	Addrs  []libp2ppeer.AddrInfo

	Accepted chan struct{}
}

// workerP2PFetchRequest is sent to a worker goroutine.
// When the worker completes the request,
// it sends a response to the main goroutine.
type workerP2PFetchRequest struct {
	DataID string
	Addrs  []libp2ppeer.AddrInfo
}

// workerFetchResult is the successful result of a proposal's block data fetch.
type workerFetchResult struct {
	DataID string

	Txs        []transaction.Tx
	EncodedTxs []byte
}

// PBDRetriever, short for "Proposal Block Data" retriever,
// is responsible for retrieving the block data specified in proposal annotations.
type PBDRetriever struct {
	log *slog.Logger

	p2pClient *gsbd.Libp2pClient

	rCache *gsbd.RequestCache

	decoder transaction.Codec[transaction.Tx]

	host libp2phost.Host

	p2pFetchRequests       chan pbdP2PFetchRequest
	workerP2PFetchRequests chan workerP2PFetchRequest

	workerFetchResults chan workerFetchResult

	wg sync.WaitGroup
}

// PBDRetrieverConfig is the configuration value passed to [NewPBDRetriever].
type PBDRetrieverConfig struct {
	// P2PClient *gsbd.Libp2pClient

	// How to indicate availability of fetched proposed data.
	RequestCache *gsbd.RequestCache

	// How to decode transactions.
	Decoder transaction.Codec[transaction.Tx]

	// The libp2p host from which connections will be made.
	Host libp2phost.Host

	// How many worker goroutines to run.
	NWorkers int
}

func NewPBDRetriever(
	ctx context.Context,
	log *slog.Logger,
	cfg PBDRetrieverConfig,
) *PBDRetriever {
	if cfg.NWorkers <= 0 {
		panic(fmt.Errorf("BUG: NWorkers must be positive (got %d)", cfg.NWorkers))
	}

	r := &PBDRetriever{
		log: log,
		// p2pClient: cfg.P2PClient,
		rCache: cfg.RequestCache,

		decoder: cfg.Decoder,
		host:    cfg.Host,

		p2pFetchRequests:       make(chan pbdP2PFetchRequest),                  // Unbuffered.
		workerP2PFetchRequests: make(chan workerP2PFetchRequest, cfg.NWorkers), // One per worker. Should it be +1?

		workerFetchResults: make(chan workerFetchResult, cfg.NWorkers),
	}

	r.wg.Add(1)
	go r.mainLoop(ctx)

	r.wg.Add(cfg.NWorkers)
	for i := range cfg.NWorkers {
		go r.worker(ctx, i)
	}

	return r
}

func (r *PBDRetriever) Wait() {
	r.wg.Wait()
}

type pbdInFlight struct {
	Ready chan struct{}
	BDR   *gsbd.BlockDataRequest
}

// mainLoop coordinates requests originating from exported methods called from external goroutines
// and internal worker goroutines.
func (r *PBDRetriever) mainLoop(ctx context.Context) {
	defer r.wg.Done()

	ifrs := make(map[string]pbdInFlight)

	for {
		select {
		case <-ctx.Done():
			r.log.Info("Quitting due to context cancellation", "cause", context.Cause(ctx))
			return

		case req := <-r.p2pFetchRequests:
			ready := make(chan struct{})
			bdr := &gsbd.BlockDataRequest{Ready: ready}

			// TODO: check if we are overwriting.
			ifrs[req.DataID] = pbdInFlight{
				Ready: ready,
				BDR:   bdr,
			}

			// This will currently fail if we get two different proposed blocks
			// with the same data ID.
			// Or maybe if we have two different schemes in flight for the same data ID.
			r.rCache.SetInFlight(req.DataID, bdr)

			// This might be risky, but block sending it to an available worker.
			// It might be better to just fail if it blocks?
			if !gchan.SendC(
				ctx, r.log,
				r.workerP2PFetchRequests, workerP2PFetchRequest{
					DataID: req.DataID,
					Addrs:  req.Addrs,
				},
				"sending p2p fetch request to workers",
			) {
				return
			}

			// Signal that it's been sent to a worker.
			close(req.Accepted)

		case res := <-r.workerFetchResults:
			// TODO: handle failed lookup.
			ifr := ifrs[res.DataID]
			ifr.BDR.Transactions = res.Txs
			ifr.BDR.EncodedTransactions = res.EncodedTxs
			close(ifr.Ready)

			delete(ifrs, res.DataID)
		}
	}
}

func (r *PBDRetriever) worker(ctx context.Context, idx int) {
	defer r.wg.Done()

	wLog := r.log.With("worker_idx", idx)

	for {
		select {
		case <-ctx.Done():
			wLog.Info("Quitting due to context cancellation", "cause", context.Cause(ctx))
			return

		case req := <-r.workerP2PFetchRequests:
			if !r.workerFetchP2P(ctx, wLog, req) {
				return
			}
		}
	}
}

func (r *PBDRetriever) workerFetchP2P(
	ctx context.Context,
	wLog *slog.Logger,
	req workerP2PFetchRequest,
) bool {
	dec, err := gsbd.NewBlockDataDecoder(req.DataID, r.decoder)
	if err != nil {
		panic(fmt.Errorf("BUG: requested to fetch invalid data ID %q", req.DataID))
	}

	// Shuffle the addresses in place.
	// If every node does this, as a courtesy,
	// then hopefully the first address won't become overloaded immediately.
	// The address list should be short,
	// and we don't often reach into the global rng,
	// so this is unlikely to be a point of mutex contention.
	rand.Shuffle(len(req.Addrs), func(i, j int) {
		req.Addrs[i], req.Addrs[j] = req.Addrs[j], req.Addrs[i]
	})

	for _, addr := range req.Addrs {
		done, ok := r.workerFetchOneP2P(ctx, wLog, req.DataID, addr, dec)
		if !ok {
			return false
		}
		if done {
			return true
		}
	}

	// We didn't return in the address loop,
	// which means we failed to fetch the data.
	wLog.Warn(
		"Failed to fetch block data from any proposer-supplied address",
		"data_id", req.DataID,
	)

	// The outer loop can continue even though we failed here.
	// TODO: we probably need some other way to signal that we need to fall back
	// to another mechanism of retrieving block data.
	return true
}

func (r *PBDRetriever) workerFetchOneP2P(
	ctx context.Context,
	wLog *slog.Logger,
	dataID string,
	addr libp2ppeer.AddrInfo,
	dec *gsbd.BlockDataDecoder,
) (done, ok bool) {
	// Ensure we have a connection to the peer.
	if err := r.host.Connect(ctx, addr); err != nil {
		wLog.Info(
			"Failed to connect to peer to fetch proposed data",
			"peer_id", addr.ID,
			"data_id", dataID,
			"err", err,
		)
		return false, ctx.Err() == nil
	}

	// Open the stream to the peer.
	s, err := r.host.NewStream(ctx, addr.ID, libp2pprotocol.ID(gsbd.ProposedBlockDataV1Prefix+dataID))
	if err != nil {
		wLog.Info(
			"Failed to open stream to peer when attempting to fetch proposed data",
			"peer_id", addr.ID,
			"data_id", dataID,
			"err", err,
		)
		return false, ctx.Err() == nil
	}
	defer s.Close()

	if err := s.CloseWrite(); err != nil {
		// Not sure how informative this error is,
		// but we can log it and keep going anyway.
		wLog.Info(
			"Failed to close stream for write when attempting to fetch proposed data",
			"peer_id", addr.ID,
			"data_id", dataID,
			"err", err,
		)
	}

	// We want to decode the stream,
	// but we also want to grab the bytes we've read,
	// so tee them off into a separate buffer.
	// The bytes.Buffer could be part of a sync.Pool,
	// but if we are only doing one fetch every few seconds,
	// it probably won't help.
	var buf bytes.Buffer
	tee := io.TeeReader(s, &buf)
	txs, err := dec.Decode(tee)

	// We are done with the stream, so eagerly close it as a courtesy to the remote end.
	if closeErr := s.Close(); closeErr != nil {
		wLog.Info(
			"Failed to close stream after fetching proposed data",
			"peer_id", addr.ID,
			"data_id", dataID,
			"err", closeErr,
		)
	}

	if err != nil {
		wLog.Info(
			"Failed to decode fetched proposed data",
			"peer_id", addr.ID,
			"data_id", dataID,
			"err", err,
		)
		return false, ctx.Err() == nil
	}

	// No error, and we have the transactions and the decoded data.
	// NOTE: it is probably possible, with a malicious or misconfigured remote end,
	// that our buffer read more data than we decoded.
	// To address that, we would probably need to adjust the Decode signature
	// to return the number of bytes read,
	// and ensure we truncate the byte buffer to that same number.

	ok = gchan.SendC(
		ctx, wLog,
		r.workerFetchResults, workerFetchResult{
			DataID:     dataID,
			Txs:        txs,
			EncodedTxs: buf.Bytes(),
		},
		"sending fetch result to main goroutine",
	)

	return true, ok
}

func (r *PBDRetriever) Retrieve(
	ctx context.Context, dataID string, metadata []byte,
) error {
	var pda ProposalDriverAnnotation
	if err := json.Unmarshal(metadata, &pda); err != nil {
		return fmt.Errorf("failed to decode metadata: %w", err)
	}

	// TODO: gassert: dataID can be parsed without error.

	req := pbdP2PFetchRequest{
		DataID: dataID,
		Addrs:  make([]libp2ppeer.AddrInfo, 0, len(pda.Locations)),

		Accepted: make(chan struct{}),
	}

	for _, loc := range pda.Locations {
		if loc.Scheme != gsbd.Libp2pScheme {
			r.log.Warn("Unknown scheme for proposal annotation", "scheme_id", uint8(loc.Scheme))
			continue
		}

		// If it is a libp2p scheme,
		// then the Addr field should be a JSON-encoded libp2p addr info.
		var ai libp2ppeer.AddrInfo
		if err := json.Unmarshal([]byte(loc.Addr), &ai); err != nil {
			r.log.Debug(
				"Skipping retrieval location due to failure to unmarshal AddrInfo",
				"err", err,
			)
			continue
		}

		req.Addrs = append(req.Addrs, ai)
	}

	// We've parsed out all the addresses.
	// If we ended up with zero, something went quite wrong.
	if len(req.Addrs) == 0 {
		return fmt.Errorf("cannot fetch block data for data ID %q; no usable addresses in metadata", dataID)
	}

	// Then we have at least one address. so send the fetch request back to the main loop.
	_, ok := gchan.ReqResp(
		ctx, r.log,
		r.p2pFetchRequests, req,
		req.Accepted,
		"requesting libp2p fetch of proposed block's data",
	)
	if !ok {
		return context.Cause(ctx)
	}

	return nil
}
