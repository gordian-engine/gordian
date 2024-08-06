package gmempool

import (
	"context"
	"fmt"
	"log/slog"

	"cosmossdk.io/core/store"
	"cosmossdk.io/core/transaction"
	"cosmossdk.io/server/v2/appmanager"
)

type TxBuffer struct {
	log *slog.Logger

	am appmanager.AppManager[transaction.Tx]

	pendingState store.ReaderMap

	txs    []transaction.Tx
	hashes map[string]struct{}
}

func NewTxBuffer(
	log *slog.Logger,
	am appmanager.AppManager[transaction.Tx],
) *TxBuffer {
	return &TxBuffer{
		log: log,

		am: am,

		hashes: make(map[string]struct{}),
	}
}

func (b *TxBuffer) AddTx(ctx context.Context, tx transaction.Tx) error {
	if b.pendingState == nil {
		panic("should not get to AddTx with b.pendingState == nil")
	}
	txRes, resultingState := b.am.SimulateWithState(ctx, b.pendingState, tx)
	if txRes.Error != nil {
		// TODO: make wrapper error type for this.
		return fmt.Errorf("failed to add transaction: %w", txRes.Error)
	}

	b.pendingState = resultingState
	b.txs = append(b.txs, tx)
	return nil
}

func (b *TxBuffer) AddedTxs(dst []transaction.Tx) []transaction.Tx {
	// Does this need to clone each transaction?
	return append(dst, b.txs...)
}

func (b *TxBuffer) ResetState(state store.ReaderMap) {
	b.pendingState = state

	clear(b.txs)
	b.txs = b.txs[:0]

	clear(b.hashes)
}
