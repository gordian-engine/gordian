package gtx

import (
	"cosmossdk.io/core/transaction"
	"github.com/cosmos/cosmos-sdk/client"
)

// txDecoder adapts client.TxConfig to the transaction.Codec type.
// This is a copy of "temporarytxDecoder" from simapp code.
type txDecoder struct {
	TxConfig client.TxConfig
}

func NewTxDecoder(txConfig client.TxConfig) txDecoder {
	return txDecoder{TxConfig: txConfig}
}

// Decode implements transaction.Codec.
func (t txDecoder) Decode(bz []byte) (transaction.Tx, error) {
	return t.TxConfig.TxDecoder()(bz)
}

// DecodeJSON implements transaction.Codec.
func (t txDecoder) DecodeJSON(bz []byte) (transaction.Tx, error) {
	return t.TxConfig.TxJSONDecoder()(bz)
}
