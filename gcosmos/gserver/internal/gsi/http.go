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

	return r
}

func handleBlocksWatermark(log *slog.Logger, cfg HTTPServerConfig) func (w http.ResponseWriter, req *http.Request) {
	return func(w http.ResponseWriter, req *http.Request) {
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