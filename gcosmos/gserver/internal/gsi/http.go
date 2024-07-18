package gsi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"

	"github.com/gorilla/mux"
	"github.com/rollchains/gordian/tm/tmstore"
	"github.com/rollchains/gordian/tm/tmp2p/tmlibp2p"
)

type HTTPServer struct {
	done chan struct{}
}

type HTTPServerConfig struct {
	Listener net.Listener

	MirrorStore tmstore.MirrorStore

	Libp2pHost   *tmlibp2p.Host
	libp2pconn   *tmlibp2p.Connection
	// driver *gsi.Driver[T]

}

func NewHTTPServer(ctx context.Context, log *slog.Logger, cfg HTTPServerConfig) *HTTPServer {
	srv := &http.Server{
		Handler: newMux(log, cfg),

		BaseContext: func(net.Listener) context.Context {
			return ctx
		},
	}

	h := &HTTPServer{
		done: make(chan struct{}),
	}
	go h.serve(log, cfg.Listener, srv)
	go h.waitForShutdown(ctx, srv)

	return h
}

func (h *HTTPServer) Wait() {
	<-h.done
}

func (h *HTTPServer) waitForShutdown(ctx context.Context, srv *http.Server) {
	select {
	case <-h.done:
		// h.serve returned on its own, nothing left to do here.
		return
	case <-ctx.Done():
		// Forceful shutdown. We could probably log any returned error on this.
		_ = srv.Close()
	}
}

func (h *HTTPServer) serve(log *slog.Logger, ln net.Listener, srv *http.Server) {
	defer close(h.done)

	if err := srv.Serve(ln); err != nil {
		if errors.Is(err, net.ErrClosed) || errors.Is(err, http.ErrServerClosed) {
			log.Info("HTTP server shutting down")
		} else {
			log.Info("HTTP server shutting down due to error", "err", err)
		}
	}
}

func newMux(log *slog.Logger, cfg HTTPServerConfig) http.Handler {
	r := mux.NewRouter()

	r.HandleFunc("/blocks/watermark", handleBlocksWatermark(log, cfg)).Methods("GET")

	// CometBFT Query methods for compatability with CometRPC
	r.HandleFunc("abci_info", handleABCIInfo(log,cfg)).Methods("GET")
	r.HandleFunc("abci_query", handleABCIQuery(log,cfg)).Methods("POST")
	r.HandleFunc("block", handleBlock(log,cfg)).Methods("POST")
	r.HandleFunc("block_by_hash", handleBlocksByHash(log,cfg)).Methods("GET")
	r.HandleFunc("block_results", handleBlockResults(log,cfg)).Methods("POST")
	r.HandleFunc("block_search", handleBlockSearch(log,cfg)).Methods("POST")
	r.HandleFunc("broadcast_evidence", handleBroadcastEvidence(log,cfg)).Methods("POST")
	r.HandleFunc("broadcast_tx_async", handleBroadcastTxAsync(log,cfg)).Methods("POST")
	r.HandleFunc("broadcast_tx_commit", handleBroadcastTxCommit(log,cfg)).Methods("POST")
	r.HandleFunc("broadcast_tx_sync", handleBroadcastTxSync(log,cfg)).Methods("POST")
	r.HandleFunc("check_tx", handleCheckTx(log,cfg)).Methods("POST")
	r.HandleFunc("commit", handleCommit(log,cfg)).Methods("POST")
	r.HandleFunc("consensus_params", handleConsensusParams(log,cfg)).Methods("GET")
	r.HandleFunc("consensus_state", handleConsensusState(log,cfg)).Methods("GET")
	r.HandleFunc("dump_consensus_state", handleDumpConsensusState(log,cfg)).Methods("GET")
	r.HandleFunc("genesis", handleGenesis(log,cfg)).Methods("GET")
	r.HandleFunc("genesis_chunked", handleGenesisChunked(log,cfg)).Methods("GET")
	r.HandleFunc("header", handleHeader(log,cfg)).Methods("GET")
	r.HandleFunc("header_by_hash", handleHeaderByHash(log,cfg)).Methods("GET")
	r.HandleFunc("health", handleHealth(log,cfg)).Methods("GET")
	r.HandleFunc("net_info", handleNetInfo(log,cfg)).Methods("GET")
	r.HandleFunc("num_unconfirmed_txs", handleNumUnconfirmedTxs(log,cfg)).Methods("GET")
	r.HandleFunc("status", handleStatus(log,cfg)).Methods("GET")
	r.HandleFunc("subscribe", handleSubscribe(log,cfg)).Methods("GET")
	r.HandleFunc("tx", handleTx(log,cfg)).Methods("GET")
	r.HandleFunc("tx_search", handleTxSearch(log,cfg)).Methods("GET")
	r.HandleFunc("unconfirmed_txs", handleUnconfirmedTxs(log,cfg)).Methods("GET")
	r.HandleFunc("unsubscribe", handleUnsubscribe(log,cfg)).Methods("GET")
	r.HandleFunc("unsubscribe_all", unsubscribeAll(log,cfg)).Methods("GET")
	r.HandleFunc("validators", handleValidators(log,cfg)).Methods("GET")

	return r
}

func handleBlocksWatermark(log *slog.Logger, cfg HTTPServerConfig) func (w http.ResponseWriter, req *http.Request) {
	return func(w http.ResponseWriter, req *http.Request) {
		http.Error(w, "not yet implemented", http.StatusNotImplemented)
			vh, vr, ch, cr, err := cfg.MirrorStore.NetworkHeightRound(req.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		// TODO: this should probably be an exported type somewhere.
		var currentBlock struct {
			VotingHeight uint64
			VotingRound  uint32

			CommittingHeight uint64
			CommittingRound  uint32
		}

		currentBlock.VotingHeight = vh
		currentBlock.VotingRound = vr
		currentBlock.CommittingHeight = ch
		currentBlock.CommittingRound = cr

		if err := json.NewEncoder(w).Encode(currentBlock); err != nil {
			log.Warn("Failed to marshal current block", "err", err)
			return
		}
	}
}

func handleABCIInfo(log *slog.Logger, cfg HTTPServerConfig) func (w http.ResponseWriter, req *http.Request) {
	return func(w http.ResponseWriter, req *http.Request) {
		http.Error(w, "not yet implemented", http.StatusNotImplemented)
	}
}
func handleABCIQuery(log *slog.Logger, cfg HTTPServerConfig) func (w http.ResponseWriter, req *http.Request) {
	return func(w http.ResponseWriter, req *http.Request) {
		http.Error(w, "not yet implemented", http.StatusNotImplemented)
	}
}
func handleBlock(log *slog.Logger, cfg HTTPServerConfig) func (w http.ResponseWriter, req *http.Request) {
	return func(w http.ResponseWriter, req *http.Request) {
		http.Error(w, "not yet implemented", http.StatusNotImplemented)
	}
}
func handleBlocksByHash(log *slog.Logger, cfg HTTPServerConfig) func (w http.ResponseWriter, req *http.Request) {
	return func(w http.ResponseWriter, req *http.Request) {
		http.Error(w, "not yet implemented", http.StatusNotImplemented)
	}
}
func handleBlockResults(log *slog.Logger, cfg HTTPServerConfig) func (w http.ResponseWriter, req *http.Request) {
	return func(w http.ResponseWriter, req *http.Request) {
		http.Error(w, "not yet implemented", http.StatusNotImplemented)
	}
}
func handleBlockSearch(log *slog.Logger, cfg HTTPServerConfig) func (w http.ResponseWriter, req *http.Request) {
	return func(w http.ResponseWriter, req *http.Request) {
		http.Error(w, "not yet implemented", http.StatusNotImplemented)
	}
}
func handleBroadcastEvidence(log *slog.Logger, cfg HTTPServerConfig) func (w http.ResponseWriter, req *http.Request) {
	return func(w http.ResponseWriter, req *http.Request) {
		http.Error(w, "not yet implemented", http.StatusNotImplemented)
	}
}
func handleBroadcastTxAsync(log *slog.Logger, cfg HTTPServerConfig) func (w http.ResponseWriter, req *http.Request) {
	return func(w http.ResponseWriter, req *http.Request) {
		http.Error(w, "not yet implemented", http.StatusNotImplemented)
	}
}
func handleBroadcastTxCommit(log *slog.Logger, cfg HTTPServerConfig) func (w http.ResponseWriter, req *http.Request) {
	return func(w http.ResponseWriter, req *http.Request) {
		http.Error(w, "not yet implemented", http.StatusNotImplemented)
	}
}
func handleBroadcastTxSync(log *slog.Logger, cfg HTTPServerConfig) func (w http.ResponseWriter, req *http.Request) {
	return func(w http.ResponseWriter, req *http.Request) {
		http.Error(w, "not yet implemented", http.StatusNotImplemented)
	}
}
func handleCheckTx(log *slog.Logger, cfg HTTPServerConfig) func (w http.ResponseWriter, req *http.Request) {
	return func(w http.ResponseWriter, req *http.Request) {
		http.Error(w, "not yet implemented", http.StatusNotImplemented)
	}
}
func handleCommit(log *slog.Logger, cfg HTTPServerConfig) func (w http.ResponseWriter, req *http.Request) {
	return func(w http.ResponseWriter, req *http.Request) {
		http.Error(w, "not yet implemented", http.StatusNotImplemented)
	}
}
func handleConsensusParams(log *slog.Logger, cfg HTTPServerConfig) func (w http.ResponseWriter, req *http.Request) {
	return func(w http.ResponseWriter, req *http.Request) {
		http.Error(w, "not yet implemented", http.StatusNotImplemented)
	}
}
func handleConsensusState(log *slog.Logger, cfg HTTPServerConfig) func (w http.ResponseWriter, req *http.Request) {
	return func(w http.ResponseWriter, req *http.Request) {
		http.Error(w, "not yet implemented", http.StatusNotImplemented)
	}
}
func handleDumpConsensusState(log *slog.Logger, cfg HTTPServerConfig) func (w http.ResponseWriter, req *http.Request) {
	return func(w http.ResponseWriter, req *http.Request) {
		http.Error(w, "not yet implemented", http.StatusNotImplemented)
	}
}
func handleGenesis(log *slog.Logger, cfg HTTPServerConfig) func (w http.ResponseWriter, req *http.Request) {
	return func(w http.ResponseWriter, req *http.Request) {
		http.Error(w, "not yet implemented", http.StatusNotImplemented)
	}
}
func handleGenesisChunked(log *slog.Logger, cfg HTTPServerConfig) func (w http.ResponseWriter, req *http.Request) {
	return func(w http.ResponseWriter, req *http.Request) {
		http.Error(w, "not yet implemented", http.StatusNotImplemented)
	}
}
func handleHeader(log *slog.Logger, cfg HTTPServerConfig) func (w http.ResponseWriter, req *http.Request) {
	return func(w http.ResponseWriter, req *http.Request) {
		http.Error(w, "not yet implemented", http.StatusNotImplemented)
	}
}
func handleHeaderByHash(log *slog.Logger, cfg HTTPServerConfig) func (w http.ResponseWriter, req *http.Request) {
	return func(w http.ResponseWriter, req *http.Request) {
		http.Error(w, "not yet implemented", http.StatusNotImplemented)
	}
}
func handleHealth(log *slog.Logger, cfg HTTPServerConfig) func (w http.ResponseWriter, req *http.Request) {
	return func(w http.ResponseWriter, req *http.Request) {
		http.Error(w, "not yet implemented", http.StatusNotImplemented)
	}
}
func handleNetInfo(log *slog.Logger, cfg HTTPServerConfig) func (w http.ResponseWriter, req *http.Request) {
	return func(w http.ResponseWriter, req *http.Request) {
		http.Error(w, "not yet implemented", http.StatusNotImplemented)
	}
}
func handleNumUnconfirmedTxs(log *slog.Logger, cfg HTTPServerConfig) func (w http.ResponseWriter, req *http.Request) {
	return func(w http.ResponseWriter, req *http.Request) {
		http.Error(w, "not yet implemented", http.StatusNotImplemented)
	}
}
func handleStatus(log *slog.Logger, cfg HTTPServerConfig) func (w http.ResponseWriter, req *http.Request) {
	return func(w http.ResponseWriter, req *http.Request) {
		http.Error(w, "not yet implemented", http.StatusNotImplemented)
	}
}
func handleSubscribe(log *slog.Logger, cfg HTTPServerConfig) func (w http.ResponseWriter, req *http.Request) {
	return func(w http.ResponseWriter, req *http.Request) {
		http.Error(w, "not yet implemented", http.StatusNotImplemented)
	}
}
func handleTx(log *slog.Logger, cfg HTTPServerConfig) func (w http.ResponseWriter, req *http.Request) {
	return func(w http.ResponseWriter, req *http.Request) {
		http.Error(w, "not yet implemented", http.StatusNotImplemented)
	}
}
func handleTxSearch(log *slog.Logger, cfg HTTPServerConfig) func (w http.ResponseWriter, req *http.Request) {
	return func(w http.ResponseWriter, req *http.Request) {
		http.Error(w, "not yet implemented", http.StatusNotImplemented)
	}
}
func handleUnconfirmedTxs(log *slog.Logger, cfg HTTPServerConfig) func (w http.ResponseWriter, req *http.Request) {
	return func(w http.ResponseWriter, req *http.Request) {
		http.Error(w, "not yet implemented", http.StatusNotImplemented)
	}
}
func handleUnsubscribe(log *slog.Logger, cfg HTTPServerConfig) func (w http.ResponseWriter, req *http.Request) {
	return func(w http.ResponseWriter, req *http.Request) {
		http.Error(w, "not yet implemented", http.StatusNotImplemented)
	}
}
func unsubscribeAll(log *slog.Logger, cfg HTTPServerConfig) func (w http.ResponseWriter, req *http.Request) {
	return func(w http.ResponseWriter, req *http.Request) {
		http.Error(w, "not yet implemented", http.StatusNotImplemented)
	}
}
func handleValidators(log *slog.Logger, cfg HTTPServerConfig) func (w http.ResponseWriter, req *http.Request) {
	return func(w http.ResponseWriter, req *http.Request) {
		http.Error(w, "not yet implemented", http.StatusNotImplemented)
	}
}