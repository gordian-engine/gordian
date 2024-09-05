package gsi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strconv"

	"cosmossdk.io/core/transaction"
	"cosmossdk.io/server/v2/appmanager"
	"github.com/cosmos/cosmos-sdk/codec"
	"github.com/gorilla/mux"
	"github.com/rollchains/gordian/gcosmos/gserver/internal/ggrpc"
	"github.com/rollchains/gordian/gcosmos/gserver/internal/txmanager"
	"github.com/rollchains/gordian/tm/tmp2p/tmlibp2p"
)

type HTTPServer struct {
	done chan struct{}
}

type HTTPServerConfig struct {
	Listener net.Listener

	// FinalizationStore tmstore.FinalizationStore
	// MirrorStore       tmstore.MirrorStore

	// GordianClient ggrpc.GordianGRPCClient
	GordianClient *ggrpc.GordianGRPC

	// CryptoRegistry *gcrypto.Registry

	Libp2pHost *tmlibp2p.Host
	Libp2pconn *tmlibp2p.Connection

	AppManager appmanager.AppManager[transaction.Tx]
	TxCodec    transaction.Codec[transaction.Tx]
	Codec      codec.Codec

	TxBuffer *txmanager.SDKTxBuf
}

func NewHTTPServer(ctx context.Context, log *slog.Logger, cfg HTTPServerConfig) *HTTPServer {
	srv := &http.Server{
		Handler: newMux(log, cfg),

		BaseContext: func(net.Listener) context.Context {
			return ctx
		},
	}

	if cfg.GordianClient == nil {
		panic("BUG: NewHTTPServer GordianClient is nil")
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
	r.HandleFunc("/validators", handleValidators(log, cfg)).Methods("GET")
	r.HandleFunc("/block/{id}", handleBlock(log, cfg)).Methods("GET")
	r.HandleFunc("/status", handleStatus(log, cfg)).Methods("GET")
	r.HandleFunc("/tx/{hash}", hashTxQuery(log, cfg)).Methods("GET")

	setDebugRoutes(log, cfg, r)

	// setCompatRoutes(log, cfg, r)

	return r
}

func handleBlocksWatermark(log *slog.Logger, cfg HTTPServerConfig) func(w http.ResponseWriter, req *http.Request) {
	return func(w http.ResponseWriter, req *http.Request) {
		resp, err := cfg.GordianClient.GetBlocksWatermark(req.Context(), &ggrpc.CurrentBlockRequest{})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		if err := json.NewEncoder(w).Encode(resp); err != nil {
			log.Warn("Failed to marshal current block", "err", err)
			return
		}
	}
}

func handleValidators(log *slog.Logger, cfg HTTPServerConfig) func(w http.ResponseWriter, req *http.Request) {
	return func(w http.ResponseWriter, req *http.Request) {
		heightStr := req.URL.Query().Get("height")
		height := uint64(0)
		if heightStr != "" {
			var err error
			height, err = strconv.ParseUint(heightStr, 10, 64)
			if err != nil {
				http.Error(w, "invalid height", http.StatusBadRequest)
				return
			}
		}

		resp, err := cfg.GordianClient.GetValidators(req.Context(), &ggrpc.GetValidatorsRequest{
			Height: height,
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		if err := json.NewEncoder(w).Encode(resp); err != nil {
			log.Warn("Failed to marshal validators response", "err", err)
			return
		}
	}
}

func handleBlock(log *slog.Logger, cfg HTTPServerConfig) func(w http.ResponseWriter, req *http.Request) {
	return func(w http.ResponseWriter, req *http.Request) {
		vars := mux.Vars(req)
		id := vars["id"]

		height, err := strconv.ParseUint(id, 10, 64)
		if err != nil {
			http.Error(w, "invalid block height", http.StatusBadRequest)
			return
		}

		resp, err := cfg.GordianClient.GetBlock(req.Context(), &ggrpc.GetBlockRequest{Height: height})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		if err := json.NewEncoder(w).Encode(resp); err != nil {
			log.Warn("Failed to marshal block response", "err", err)
			return
		}
	}
}

func handleStatus(log *slog.Logger, cfg HTTPServerConfig) func(w http.ResponseWriter, req *http.Request) {
	return func(w http.ResponseWriter, req *http.Request) {
		resp, err := cfg.GordianClient.GetStatus(req.Context(), &ggrpc.GetStatusRequest{})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		if err := json.NewEncoder(w).Encode(resp); err != nil {
			log.Warn("Failed to marshal status response", "err", err)
			return
		}
	}
}

func hashTxQuery(log *slog.Logger, cfg HTTPServerConfig) func(w http.ResponseWriter, req *http.Request) {
	return func(w http.ResponseWriter, req *http.Request) {
		vars := mux.Vars(req)
		hash := vars["hash"]

		if len(hash) > 2 && hash[0] == '0' && hash[1] == 'x' {
			hash = hash[2:]
		}

		resp, err := cfg.GordianClient.QueryTransaction(req.Context(), &ggrpc.QueryTransactionRequest{TxHash: hash})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		if err := json.NewEncoder(w).Encode(resp); err != nil {
			log.Warn("Failed to marshal tx response", "err", err)
			return
		}
	}
}
