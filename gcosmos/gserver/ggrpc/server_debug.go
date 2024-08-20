package ggrpc

import (
	"context"
	"encoding/json"
	"fmt"

	banktypes "cosmossdk.io/x/bank/types"
)

func NewTxRespError(err error) (*TxResultResponse, error) {
	return &TxResultResponse{
		Error: err.Error(),
	}, err
}

// SubmitTransaction implements GordianGRPCServer.
func (g *GordianGRPCServer) SubmitTransaction(ctx context.Context, req *SubmitTransactionRequest) (*TxResultResponse, error) {
	b := req.Tx
	tx, err := g.cfg.TxCodec.DecodeJSON(b)
	if err != nil {
		return NewTxRespError(err)
	}

	res, err := g.cfg.AppManager.ValidateTx(ctx, tx)
	if err != nil {
		// ValidateTx should only return an error at this level,
		// if it failed to get state from the store.
		g.log.Warn("Error attempting to validate transaction", "route", "submit_tx", "err", err)
		return NewTxRespError(err)
	}

	if res.Error != nil {
		// This is fine from the server's perspective, no need to log.
		return NewTxRespError(res.Error)
	}

	// If it passed basic validation, then we can attempt to add it to the buffer.
	if err := g.cfg.TxBuf.AddTx(ctx, tx); err != nil {
		// We could potentially check if it is a TxInvalidError here
		// and adjust the status code,
		// but since this is a debug endpoint, we'll ignore the type.
		return NewTxRespError(err)
	}

	j, err := json.Marshal(res)
	if err != nil {
		return NewTxRespError(err)
	}

	var resp TxResultResponse
	if err = json.Unmarshal(j, &resp); err != nil {
		return NewTxRespError(err)
	}

	return &resp, nil
}

// SimulateTransaction implements GordianGRPCServer.
func (g *GordianGRPCServer) SimulateTransaction(ctx context.Context, req *SubmitSimulationTransactionRequest) (*TxResultResponse, error) {
	b := req.Tx
	tx, err := g.cfg.TxCodec.DecodeJSON(b)
	if err != nil {
		return NewTxRespError(err)
	}

	res, _, err := g.cfg.AppManager.Simulate(ctx, tx)
	if err != nil {
		// Simulate should only return an error at this level,
		// if it failed to get state from the store.
		g.log.Warn("Error attempting to simulate transaction", "route", "simulate_tx", "err", err)
		return NewTxRespError(err)
	}

	if res.Error != nil {
		// This is fine from the server's perspective, no need to log.
		return NewTxRespError(res.Error)
	}

	j, err := json.Marshal(res)
	if err != nil {
		return NewTxRespError(err)
	}

	var resp TxResultResponse
	if err = json.Unmarshal(j, &resp); err != nil {
		return NewTxRespError(err)
	}

	return &resp, nil
}

// PendingTransactions implements GordianGRPCServer.
func (g *GordianGRPCServer) PendingTransactions(ctx context.Context, req *PendingTransactionsRequest) (*PendingTransactionsResponse, error) {
	txs := g.cfg.TxBuf.Buffered(ctx, nil)

	encodedTxs := make([][]byte, len(txs))
	for i, tx := range txs {
		b, err := json.Marshal(tx)
		if err != nil {
			return nil, fmt.Errorf("failed to encode transaction: %w", err)
		}
		encodedTxs[i] = json.RawMessage(b)
	}

	return &PendingTransactionsResponse{
		Txs: encodedTxs,
	}, nil
}

// QueryAccountBalance implements GordianGRPCServer.
func (g *GordianGRPCServer) QueryAccountBalance(ctx context.Context, req *QueryAccountBalanceRequest) (*QueryAccountBalanceResponse, error) {
	cdc := g.cfg.Codec
	am := g.cfg.AppManager

	if req.Address == "" {
		return nil, fmt.Errorf("BUG: address field is required")
	}

	denom := "stake"
	if req.Denom != "" {
		denom = req.Denom
	}

	msg, err := am.Query(ctx, 0, &banktypes.QueryBalanceRequest{
		Address: req.Address,
		Denom:   denom,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to query account balance: %w", err)
	}

	b, err := cdc.MarshalJSON(msg)
	if err != nil {
		return nil, fmt.Errorf("failed to encode response: %w", err)
	}

	var val QueryAccountBalanceResponse
	if err = cdc.UnmarshalJSON(b, &val); err != nil {
		return nil, err
	}

	return &val, nil
}
