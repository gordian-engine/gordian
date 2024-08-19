package ggrpc

import (
	"context"
	"fmt"

	banktypes "cosmossdk.io/x/bank/types"
)

func NewTxRespError(err error) *TxResultResponse {
	return &TxResultResponse{
		TxHash: "",
		Code:   919, // TODO: arb for now
		Error:  err.Error(),
	}
}

// SubmitTransaction implements GordianGRPCServer.
func (g *GordianGRPC) SubmitTransaction(ctx context.Context, req *SubmitTransactionRequest) (*TxResultResponse, error) {
	b := req.Tx
	tx, err := g.cfg.TxCodec.DecodeJSON(b)
	if err != nil {
		return NewTxRespError(err), err
	}

	res, err := g.cfg.AppManager.ValidateTx(ctx, tx)
	if err != nil {
		// ValidateTx should only return an error at this level,
		// if it failed to get state from the store.
		g.log.Warn("Error attempting to validate transaction", "route", "submit_tx", "err", err)
		return NewTxRespError(err), err
	}

	if res.Error != nil {
		// This is fine from the server's perspective, no need to log.
		return NewTxRespError(err), res.Error
	}

	// If it passed basic validation, then we can attempt to add it to the buffer.
	if err := g.cfg.TxBuf.AddTx(ctx, tx); err != nil {
		// We could potentially check if it is a TxInvalidError here
		// and adjust the status code,
		// but since this is a debug endpoint, we'll ignore the type.
		// http.Error(w, "failed to add transaction to buffer: "+err.Error(), http.StatusBadRequest)
		// return
		return NewTxRespError(err), err
	}

	txHash := tx.Hash()
	return &TxResultResponse{
		TxHash: string(txHash[:]),
		Error:  "",
		Code:   0,
	}, nil
}

// QueryAccountBalance implements GordianGRPCServer.
func (g *GordianGRPC) QueryAccountBalance(ctx context.Context, req *QueryAccountBalanceRequest) (*QueryAccountBalanceResponse, error) {
	cdc := g.cfg.Codec
	am := g.cfg.AppManager

	if req.AccountId == "" {
		return nil, fmt.Errorf("account id is required")
	}

	denom := "stake"
	if req.Denom != "" {
		denom = req.Denom
	}

	msg, err := am.Query(ctx, 0, &banktypes.QueryBalanceRequest{
		Address: req.AccountId,
		Denom:   denom,
	})
	if err != nil {
		return nil, err
	}

	b, err := cdc.MarshalJSON(msg)
	if err != nil {
		return nil, err
	}

	var val QueryAccountBalanceResponse
	if err = cdc.UnmarshalJSON(b, &val); err != nil {
		return nil, err
	}

	return &val, nil
}
