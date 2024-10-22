package gsbd_test

import (
	"bytes"
	"testing"

	"cosmossdk.io/core/transaction"
	"github.com/gordian-engine/gordian/gcosmos/gserver/gservertest"
	"github.com/gordian-engine/gordian/gcosmos/gserver/internal/gsbd"
	"github.com/stretchr/testify/require"
)

func TestBlockDataDecoder_uncompressed(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	var h [gservertest.HashSize]byte
	for i := range h {
		h[i] = byte(i) + 3
	}
	tx := gservertest.NewRawHashOnlyTransaction(h)
	txs := []transaction.Tx{tx}
	sz, err := gsbd.EncodeBlockData(&buf, txs)
	require.NoError(t, err)

	// Expecting uncompressed for this data.
	b := buf.Bytes()
	require.Zero(t, b[0])

	dataID := gsbd.DataID(1, 0, uint32(sz), txs)
	dec, err := gsbd.NewBlockDataDecoder(dataID, gservertest.HashOnlyTransactionDecoder{})
	require.NoError(t, err)

	gotTxs, err := dec.Decode(&buf)
	require.NoError(t, err)
	require.Len(t, gotTxs, 1)
	require.Equal(t, tx, gotTxs[0])
}

func TestBlockDataDecoder_compressed(t *testing.T) {
	t.Parallel()

	txs := make([]transaction.Tx, 10)
	for i := range txs {
		txs[i] = gservertest.NewHashOnlyTransaction(uint64(i))
	}

	var buf bytes.Buffer
	sz, err := gsbd.EncodeBlockData(&buf, txs)
	require.NoError(t, err)

	// Expecting snappy compression for this data.
	b := buf.Bytes()
	require.Equal(t, byte(1), b[0])

	dataID := gsbd.DataID(1, 0, uint32(sz), txs)
	dec, err := gsbd.NewBlockDataDecoder(dataID, gservertest.HashOnlyTransactionDecoder{})
	require.NoError(t, err)

	gotTxs, err := dec.Decode(&buf)
	require.NoError(t, err)
	require.Len(t, gotTxs, 10)
	require.Equal(t, txs, gotTxs)
}
