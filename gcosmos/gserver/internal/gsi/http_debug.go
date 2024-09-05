package gsi

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"

	"cosmossdk.io/core/transaction"
	"cosmossdk.io/server/v2/appmanager"
	banktypes "cosmossdk.io/x/bank/types"
	"github.com/cosmos/cosmos-sdk/codec"
	"github.com/gorilla/mux"
	"github.com/rollchains/gordian/gcosmos/gserver/internal/ggrpc"
	"github.com/rollchains/gordian/gcosmos/gserver/internal/txmanager"
)

type debugHandler struct {
	log *slog.Logger

	txCodec transaction.Codec[transaction.Tx]
	codec   codec.Codec
	gclient *ggrpc.GordianGRPC

	am appmanager.AppManager[transaction.Tx]

	txBuf *txmanager.SDKTxBuf
}

func setDebugRoutes(log *slog.Logger, cfg HTTPServerConfig, r *mux.Router) {
	h := debugHandler{
		log:     log,
		txCodec: cfg.TxCodec,
		codec:   cfg.Codec,
		am:      cfg.AppManager,
		gclient: cfg.GordianClient,

		txBuf: cfg.TxBuffer,
	}

	r.HandleFunc("/debug/submit_tx", h.HandleSubmitTx).Methods("POST")
	r.HandleFunc("/debug/simulate_tx", h.HandleSimulateTx).Methods("POST")

	r.HandleFunc("/debug/pending_txs", h.HandlePendingTxs).Methods("GET")

	r.HandleFunc("/debug/accounts/{id}/balance", h.HandleAccountBalance).Methods("GET")
}

func (h debugHandler) HandleSubmitTx(w http.ResponseWriter, req *http.Request) {
	defer req.Body.Close()

	b, err := io.ReadAll(req.Body)
	if err != nil {
		h.log.Warn("Failed to read request body", "route", "submit_tx", "err", err)
		http.Error(w, "failed to read request body", http.StatusBadRequest)
		return
	}

	// TODO: should this have a configurable timeout?
	// Probably fine to skip since this is a "debug" endpoint for now,
	// but if this gets promoted to a non-debug route,
	// it should have a timeout.
	ctx := req.Context()

	res, err := h.gclient.SubmitTransactionSync(ctx, &ggrpc.DoBroadcastTxSyncRequest{
		Tx: b,
	})
	if err != nil {
		h.log.Warn("Error attempting to submit transaction", "route", "submit_tx", "err", err)
		http.Error(w, "internal error while attempting to submit transaction", http.StatusInternalServerError)
		return
	}

	if err := json.NewEncoder(w).Encode(res); err != nil {
		h.log.Warn("Failed to encode submit_tx result", "err", err)
	}
}

func (h debugHandler) HandleSimulateTx(w http.ResponseWriter, req *http.Request) {
	defer req.Body.Close()

	b, err := io.ReadAll(req.Body)
	if err != nil {
		h.log.Warn("Failed to read request body", "route", "simulate_tx", "err", err)
		http.Error(w, "failed to read request body", http.StatusBadRequest)
		return
	}

	// TODO: should this have a configurable timeout?
	// Probably fine to skip since this is a "debug" endpoint for now,
	// but if this gets promoted to a non-debug route,
	// it should have a timeout.
	ctx := req.Context()

	res, err := h.gclient.SimulateTransaction(ctx, &ggrpc.SubmitSimulationTransactionRequest{
		Tx: b,
	})
	if err != nil {
		h.log.Warn("Error attempting to simulate transaction", "route", "simulate_tx", "err", err)
		http.Error(w, "internal error while attempting to simulate transaction", http.StatusInternalServerError)
		return
	}

	if err := json.NewEncoder(w).Encode(res); err != nil {
		h.log.Warn("Failed to encode simulate_tx result", "err", err)
	}
}

func (h debugHandler) HandlePendingTxs(w http.ResponseWriter, req *http.Request) {
	defer req.Body.Close()

	txs := h.txBuf.Buffered(req.Context(), nil)

	encodedTxs := make([]json.RawMessage, len(txs))
	for i, tx := range txs {
		b, err := json.Marshal(tx)
		if err != nil {
			http.Error(w, "failed to encode transaction: "+err.Error(), http.StatusInternalServerError)
			return
		}
		encodedTxs[i] = json.RawMessage(b)
	}

	if err := json.NewEncoder(w).Encode(encodedTxs); err != nil {
		h.log.Warn("Failed to encode pending transactions", "err", err)
	}
}

func (h debugHandler) HandleAccountBalance(w http.ResponseWriter, r *http.Request) {
	accountID := mux.Vars(r)["id"]

	msg, err := h.am.Query(r.Context(), 0, &banktypes.QueryBalanceRequest{
		Address: accountID,
		Denom:   "stake",
	})
	if err != nil {
		h.log.Warn("Failed to query account balance", "id", accountID, "err", err)
		http.Error(w, "query failed", http.StatusBadRequest)
		return
	}

	b, err := h.codec.MarshalJSON(msg)
	if err != nil {
		http.Error(w, "failed to encode response: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if _, err := io.Copy(w, bytes.NewReader(b)); err != nil {
		h.log.Warn("Failed to encode account balance response", "err", err)
	}
}
