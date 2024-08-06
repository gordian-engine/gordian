package gccodec

import (
	"fmt"
	"reflect"

	"cosmossdk.io/core/transaction"
	"github.com/cosmos/cosmos-sdk/client"
)

var _ transaction.Codec[transaction.Tx] = (*TxDecoder[transaction.Tx])(nil)

type TxDecoder[T transaction.Tx] struct {
	txConfig client.TxConfig
}

func NewTxDecoder(txConfig client.TxConfig) TxDecoder[transaction.Tx] {
	if txConfig == nil {
		panic("BUG: NewTxDecoder txConfig is nil")
	}

	return TxDecoder[transaction.Tx]{
		txConfig: txConfig,
	}
}

// Decode implements transaction.Codec.
func (t TxDecoder[T]) Decode(bz []byte) (T, error) {
	var out T
	tx, err := t.txConfig.TxDecoder()(bz)
	if err != nil {
		return out, err
	}

	var ok bool
	out, ok = tx.(T)
	if !ok {
		return out, fmt.Errorf("failed to convert decoded type %T to %s", tx, reflect.TypeFor[T]())
	}

	return out, nil
}

// DecodeJSON implements transaction.Codec.
func (t TxDecoder[T]) DecodeJSON(bz []byte) (T, error) {
	var out T
	tx, err := t.txConfig.TxJSONDecoder()(bz)
	if err != nil {
		return out, err
	}

	var ok bool
	out, ok = tx.(T)
	if !ok {
		return out, fmt.Errorf("failed to convert decoded type %T to %s", tx, reflect.TypeFor[T]())
	}

	return out, nil
}
