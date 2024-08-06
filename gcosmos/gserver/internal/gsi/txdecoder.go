package gsi

import (
	"cosmossdk.io/core/transaction"
	"github.com/cosmos/cosmos-sdk/client"
)

// txDecoder adapts client.TxConfig to the transaction.Codec type.
// This is roughly a copy of "temporaryTxDecoder" from simapp code.
type txDecoder[T transaction.Tx] struct {
	txConfig client.TxConfig
}

// Decode implements transaction.Codec.
func (t txDecoder[T]) Decode(bz []byte) (T, error) {
	tx, err := t.txConfig.TxDecoder()(bz)
	if err != nil {
		var out T
		return out, err
	}
	return tx.(T), nil
}

// DecodeJSON implements transaction.Codec.
func (t txDecoder[T]) DecodeJSON(bz []byte) (T, error) {
	tx, err := t.txConfig.TxJSONDecoder()(bz)
	if err != nil {
		var out T
		return out, err
	}
	return tx.(T), nil
}
