package gtx

import (
	"errors"

	"cosmossdk.io/core/transaction"
	"github.com/cosmos/cosmos-sdk/client"
)

var _ transaction.Codec[transaction.Tx] = &txDecoder[transaction.Tx]{}

type txDecoder[T transaction.Tx] struct {
	txConfig client.TxConfig
}

func NewTxDecoder(txConfig client.TxConfig) txDecoder[transaction.Tx] {
	if txConfig == nil {
		panic("NewTxDecoder txConfig is nil")
	}

	return txDecoder[transaction.Tx]{
		txConfig: txConfig,
	}
}

// Decode implements transaction.Codec.
func (t txDecoder[T]) Decode(bz []byte) (T, error) {
	var out T
	tx, err := t.txConfig.TxDecoder()(bz)
	if err != nil {
		return out, err
	}

	var ok bool
	out, ok = tx.(T)
	if !ok {
		return out, errors.New("unexpected Tx type")
	}

	return out, nil
}

// DecodeJSON implements transaction.Codec.
func (t txDecoder[T]) DecodeJSON(bz []byte) (T, error) {
	var out T
	tx, err := t.txConfig.TxJSONDecoder()(bz)
	if err != nil {
		return out, err
	}

	var ok bool
	out, ok = tx.(T)
	if !ok {
		return out, errors.New("unexpected Tx type")
	}

	return out, nil
}
