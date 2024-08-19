package gsi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"

	"github.com/gorilla/mux"
	"github.com/rollchains/gordian/gcosmos/gserver/internal/ggrpc"
)

type HTTPServer struct {
	done chan struct{}
}

type HTTPServerConfig struct {
	Listener net.Listener

	GordianGRPC ggrpc.GordianGRPCClient
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
	// TODO:
	// r.HandleFunc("/validators", handleValidators(log, cfg)).Methods("GET")

	// setDebugRoutes(log, cfg, r)

	// setCompatRoutes(log, cfg, r)

	return r
}

func handleBlocksWatermark(log *slog.Logger, cfg HTTPServerConfig) func(w http.ResponseWriter, req *http.Request) {
	return func(w http.ResponseWriter, req *http.Request) {

		currentBlock, err := cfg.GordianGRPC.GetBlocksWatermark(req.Context(), &ggrpc.CurrentBlockRequest{})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		if err := json.NewEncoder(w).Encode(currentBlock); err != nil {
			log.Warn("Failed to marshal current block", "err", err)
			return
		}

	}
}

// TODO:
// func handleValidators(log *slog.Logger, cfg HTTPServerConfig) func(w http.ResponseWriter, req *http.Request) {
// 	ms := cfg.MirrorStore
// 	fs := cfg.FinalizationStore
// 	reg := cfg.CryptoRegistry
// 	return func(w http.ResponseWriter, req *http.Request) {
// 		_, _, committingHeight, _, err := ms.NetworkHeightRound(req.Context())
// 		if err != nil {
// 			http.Error(
// 				w,
// 				fmt.Sprintf("failed to get committing height: %v", err),
// 				http.StatusInternalServerError,
// 			)
// 			return
// 		}

// 		_, _, vals, _, err := fs.LoadFinalizationByHeight(req.Context(), committingHeight)
// 		if err != nil {
// 			http.Error(
// 				w,
// 				fmt.Sprintf("failed to load finalization: %v", err),
// 				http.StatusInternalServerError,
// 			)
// 			return
// 		}

// 		// Now we have the validators at the committing height.
// 		type jsonValidator struct {
// 			PubKey []byte
// 			Power  uint64
// 		}
// 		var resp struct {
// 			FinalizationHeight uint64
// 			Validators         []jsonValidator
// 		}

// 		resp.FinalizationHeight = committingHeight
// 		resp.Validators = make([]jsonValidator, len(vals))
// 		for i, v := range vals {
// 			resp.Validators[i].Power = v.Power
// 			resp.Validators[i].PubKey = reg.Marshal(v.PubKey)
// 		}

// 		if err := json.NewEncoder(w).Encode(resp); err != nil {
// 			log.Warn("Failed to marshal validators response", "err", err)
// 			return
// 		}
// 	}
// }
