package ggrpc

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	appmanager "cosmossdk.io/core/app"
	"cosmossdk.io/core/event"
	banktypes "cosmossdk.io/x/bank/types"
	codes "google.golang.org/grpc/codes"
	status "google.golang.org/grpc/status"
)

// QueryTransaction implements GordianGRPCServer.
func (g *GordianGRPC) QueryTransaction(ctx context.Context, req *QueryTransactionRequest) (*TxResultResponse, error) {
	resp, ok := g.txIndex[req.TxHash]
	if !ok {
		return nil, status.Errorf(codes.NotFound, "transaction not found")
	}

	return resp, nil
}

// SubmitTransaction implements GordianGRPCServer.
func (g *GordianGRPC) SubmitTransaction(ctx context.Context, req *SubmitTransactionRequest) (*TxResultResponse, error) {
	b := req.Tx
	tx, err := g.txc.DecodeJSON(b)
	if err != nil {
		return &TxResultResponse{
			Error: fmt.Sprintf("failed to decode transaction json: %v", err),
		}, nil
	}

	res, err := g.am.ValidateTx(ctx, tx)
	if err != nil {
		// ValidateTx should only return an error at this level,
		// if it failed to get state from the store.
		g.log.Warn("Error attempting to validate transaction", "route", "submit_tx", "err", err)
		return nil, fmt.Errorf("failed to validate transaction: %w", err)
	}

	if res.Error != nil {
		// This is fine from the server's perspective, no need to log.
		return &TxResultResponse{
			Error: fmt.Sprintf("failed to validate transaction: %v", res.Error),
		}, nil
	}

	// TODO: ValidateTx only does stateful validation, not execution. This here lets us get the Events in the TxResult.
	res, _, err = g.am.Simulate(ctx, tx)
	if err != nil {
		// Simulate should only return an error at this level,
		// if it failed to get state from the store.
		g.log.Warn("Error attempting to simulate transaction", "route", "simulate_tx", "err", err)
		return nil, fmt.Errorf("failed to simulate transaction: %w", err)
	}

	// If it passed basic validation, then we can attempt to add it to the buffer.
	if err := g.txBuf.AddTx(ctx, tx); err != nil {
		// We could potentially check if it is a TxInvalidError here
		// and adjust the status code,
		// but since this is a debug endpoint, we'll ignore the type.
		return nil, fmt.Errorf("failed to add transaction to buffer: %w", err)
	}

	response := getGordianResponseFromSDKResult(res)

	txHash := tx.Hash()
	response.TxHash = strings.ToUpper(hex.EncodeToString(txHash[:]))

	g.txIndex[response.TxHash] = response

	return response, nil
}

// SimulateTransaction implements GordianGRPCServer.
func (g *GordianGRPC) SimulateTransaction(ctx context.Context, req *SubmitSimulationTransactionRequest) (*TxResultResponse, error) {
	b := req.Tx
	tx, err := g.txc.DecodeJSON(b)
	if err != nil {
		return &TxResultResponse{
			Error: fmt.Sprintf("failed to decode transaction json: %v", err),
		}, nil
	}

	res, _, err := g.am.Simulate(ctx, tx)
	if err != nil {
		// Simulate should only return an error at this level,
		// if it failed to get state from the store.
		g.log.Warn("Error attempting to simulate transaction", "route", "simulate_tx", "err", err)
		return nil, fmt.Errorf("failed to simulate transaction: %w", err)
	}

	if res.Error != nil {
		// This is fine from the server's perspective, no need to log.
		return &TxResultResponse{
			Error: fmt.Sprintf("failed to simulate transaction: %v", res.Error),
		}, nil
	}

	resp := getGordianResponseFromSDKResult(res)

	txHash := tx.Hash()
	resp.TxHash = strings.ToUpper(hex.EncodeToString(txHash[:]))

	return resp, nil
}

// PendingTransactions implements GordianGRPCServer.
func (g *GordianGRPC) PendingTransactions(ctx context.Context, req *PendingTransactionsRequest) (*PendingTransactionsResponse, error) {
	txs := g.txBuf.Buffered(ctx, nil)

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
func (g *GordianGRPC) QueryAccountBalance(ctx context.Context, req *QueryAccountBalanceRequest) (*QueryAccountBalanceResponse, error) {
	if req.Address == "" {
		return nil, fmt.Errorf("address field is required")
	}

	denom := "stake"
	if req.Denom != "" {
		denom = req.Denom
	}

	msg, err := g.am.Query(ctx, 0, &banktypes.QueryBalanceRequest{
		Address: req.Address,
		Denom:   denom,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to query account balance: %w", err)
	}

	b, err := g.cdc.MarshalJSON(msg)
	if err != nil {
		return nil, fmt.Errorf("failed to encode response: %w", err)
	}

	var val QueryAccountBalanceResponse
	if err = g.cdc.UnmarshalJSON(b, &val); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &val, nil
}

// getGordianResponseFromSDKResult converts an app manager TxResult to the gRPC proto result.
func getGordianResponseFromSDKResult(res appmanager.TxResult) *TxResultResponse {
	resp := &TxResultResponse{
		Code:      res.Code,
		Events:    convertEvent(res.Events),
		Data:      res.Data,
		Log:       res.Log,
		Info:      res.Info,
		GasWanted: res.GasWanted,
		GasUsed:   res.GasUsed,
		Codespace: res.Codespace,
	}
	if res.Error != nil {
		resp.Error = res.Error.Error()
	}
	return resp
}

// convertEvent converts from the cosmos-sdk core event type to the gRPC proto event.
func convertEvent(e []event.Event) []*Event {
	events := make([]*Event, len(e))
	for i, ev := range e {
		attr := make([]*Attribute, len(ev.Attributes))
		for j, a := range ev.Attributes {
			attr[j] = &Attribute{
				Key:   a.Key,
				Value: a.Value,
			}
		}

		events[i] = &Event{
			Type:       ev.Type,
			Attributes: attr,
		}
	}
	return events
}
