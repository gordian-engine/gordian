package gstress

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/mux"
	"github.com/rollchains/gordian/gcrypto"
	"github.com/rollchains/gordian/internal/glog"
	"github.com/rollchains/gordian/tm/tmconsensus"
)

// BootstrapHost is the host portion for bootstrapping a stress cluster.
//
// It is a minimal and rudimentary host intended for bootstrapping nodes into a network.
// Once bootstrapped, further work should happen over libp2p protocols.
//
// The BootstrapHost (currently) depends on a Unix socket listener for a few reasons.
// First, it is easy for an operator to pick a file path for a bootstrap host
// without any kind of conflict.
// Inspecting the filesystem at a predetermined location gives simple and quick feedback
// that a particular host is running.
// Likewise, on a clean shutdown, the socket file is removed,
// also giving quick feedback that the particular host is no longer running.
//
// Next, running on a Unix socket simplifies permissioned access;
// there is no risk of the Unix socket being mistakenly exposed on the public internet.
//
// See also [BootstrapClient], which is the intended way to access the bootstrap host.
// But it should also be possible to use curl's --unix-socket flag,
// if CLI access is required for some reason.
type BootstrapHost struct {
	log *slog.Logger

	reg gcrypto.Registry
	s   bState

	hostAddrs []string

	haltOnce sync.Once
	haltCh   chan struct{}

	wg sync.WaitGroup
}

func NewBootstrapHost(
	ctx context.Context,
	log *slog.Logger,
	serverSocketPath string,
	hostAddrs []string,
) (*BootstrapHost, error) {
	h := &BootstrapHost{
		log: log,

		s: bState{
			started: make(chan struct{}),

			app:     "echo",
			chainID: fmt.Sprintf("gstress%d", time.Now().Unix()),
		},

		hostAddrs: hostAddrs,

		haltCh: make(chan struct{}),
	}
	gcrypto.RegisterEd25519(&h.reg)

	l, err := new(net.ListenConfig).Listen(ctx, "unix", serverSocketPath)
	if err != nil {
		return nil, fmt.Errorf("error listening: %w", err)
	}

	h.log.Info("Listening", "addr", l.Addr().String())

	h.wg.Add(2)
	go h.serve(l)
	go h.closeListenerOnContextCancel(ctx, l)

	return h, nil
}

func (h *BootstrapHost) Wait() {
	h.wg.Wait()
}

func (h *BootstrapHost) serve(l net.Listener) {
	defer h.wg.Done()

	if err := http.Serve(l, h.newMux()); err != nil {
		if errors.Is(err, net.ErrClosed) {
			h.log.Info("HTTP server shutting down")
		} else {
			h.log.Info("HTTP server shutting down due to error", "err", err)
		}
	}
}

func (h *BootstrapHost) Halt() {
	h.haltOnce.Do(func() {
		close(h.haltCh)
	})
}

func (h *BootstrapHost) Halted() <-chan struct{} {
	return h.haltCh
}

// closeListenerOnContextCancel waits for a root context cancellation
// and then closes the host's listener.
// This is a workaround for the fact that an open listener does not have a way
// to stop an in-progress Accept call upon context cancellation;
// it requires an explicit Close call, which is allowed to originate on another goroutine.
func (h *BootstrapHost) closeListenerOnContextCancel(ctx context.Context, l net.Listener) {
	defer h.wg.Done()

	<-ctx.Done()

	if err := l.Close(); err != nil {
		h.log.Warn("Error closing listener", "err", err)
	}
}

func (h *BootstrapHost) newMux() http.Handler {
	r := mux.NewRouter()

	r.HandleFunc("/seed-addrs", func(w http.ResponseWriter, req *http.Request) {
		enc := json.NewEncoder(w)
		if err := enc.Encode(h.hostAddrs); err != nil {
			h.log.Info("Failed to write seed addresses", "err", err)
		}
	}).Methods("GET")

	r.HandleFunc("/chain-id", func(w http.ResponseWriter, req *http.Request) {
		// GET: report the current chain ID.
		if _, err := io.WriteString(w, h.s.ChainID()+"\n"); err != nil {
			h.log.Info("Failed to write chain ID", "err", err)
		}
	}).Methods("GET")

	r.HandleFunc("/chain-id", func(w http.ResponseWriter, req *http.Request) {
		// POST: replace the chain ID with what the client supplied.
		b, err := io.ReadAll(req.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			h.log.Info("Failed to read new chain ID from client", "err", err)
			return
		}

		newID, ok := strings.CutSuffix(string(b), "\n")
		if !ok {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprintf(w, "new chain ID %q missing required trailing newline", newID)
			return
		}

		w.WriteHeader(http.StatusNoContent)
		h.s.SetChainID(strings.TrimSpace(string(newID)))
	}).Methods("POST")

	r.HandleFunc("/app", func(w http.ResponseWriter, req *http.Request) {
		// GET: report the current app.
		if _, err := io.WriteString(w, h.s.App()+"\n"); err != nil {
			h.log.Info("Failed to write app", "err", err)
		}
	}).Methods("GET")

	r.HandleFunc("/app", func(w http.ResponseWriter, req *http.Request) {
		// POST: replace the chain ID with what the client supplied.
		b, err := io.ReadAll(req.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			h.log.Info("Failed to read new app from client", "err", err)
			return
		}

		a, ok := strings.CutSuffix(string(b), "\n")
		if !ok {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprintf(w, "new app %q missing required trailing newline", a)
			return
		}

		switch a {
		// There are only a small set of valid apps.
		case "echo":
			// Okay.
		default:
			w.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprintf(w, "invalid app name %q", a)
			return
		}

		w.WriteHeader(http.StatusNoContent)
		h.s.SetApp(strings.TrimSpace(string(a)))
	}).Methods("POST")

	r.HandleFunc("/register-validator", h.httpRegisterValidator).Methods("POST")
	r.HandleFunc("/validators", h.httpValidators).Methods("GET")

	r.HandleFunc("/start", func(w http.ResponseWriter, req *http.Request) {
		h.s.Start()
		w.WriteHeader(http.StatusNoContent)
	}).Methods("POST")

	r.HandleFunc("/halt", func(w http.ResponseWriter, req *http.Request) {
		h.Halt()
		w.WriteHeader(http.StatusNoContent)
	})

	return r
}

func (h *BootstrapHost) httpRegisterValidator(w http.ResponseWriter, req *http.Request) {
	dec := json.NewDecoder(req.Body)
	var jv jsonValidator
	if err := dec.Decode(&jv); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		h.log.Info("Failed to decode validator", "err", err)
		return
	}

	key, err := h.reg.Unmarshal(jv.ValPubKeyBytes)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		h.log.Info("Failed to unmarshal pub key bytes", "err", err)
		return
	}

	h.s.AddValidator(tmconsensus.Validator{
		PubKey: key,
		Power:  jv.Power,
	})
	h.log.Info("Registered validator", "pubkey", glog.Hex(key.PubKeyBytes()), "key_type", reflect.TypeOf(key), "power", jv.Power)

	w.WriteHeader(http.StatusNoContent)
}

func (h *BootstrapHost) httpValidators(w http.ResponseWriter, req *http.Request) {
	vals := h.s.Validators()

	jvs := make([]jsonValidator, len(vals))
	for i, val := range vals {
		jvs[i] = jsonValidator{
			ValPubKeyBytes: h.reg.Marshal(val.PubKey),
			Power:          val.Power,
		}
	}

	if err := json.NewEncoder(w).Encode(jvs); err != nil {
		// Might fail to write this header if jvs was partially marshaled...
		w.WriteHeader(http.StatusInternalServerError)

		fmt.Fprint(w, "failed to marshal validators")
		return
	}
}

type jsonValidator struct {
	ValPubKeyBytes []byte

	// Technically should not json-marshal uint64,
	// but should be okay for this limited case.
	// Narrow enough scope to fix later if needed.
	Power uint64
}
