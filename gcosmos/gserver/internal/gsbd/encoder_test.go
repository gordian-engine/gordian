package gsbd_test

import (
	"bytes"
	"encoding/binary"
	"testing"

	"cosmossdk.io/core/transaction"
	"github.com/gordian-engine/gordian/gcosmos/gserver/gservertest"
	"github.com/gordian-engine/gordian/gcosmos/gserver/internal/gsbd"
	"github.com/stretchr/testify/require"
)

func TestEncodeBlockData_uncompressed(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	var h [gservertest.HashSize]byte
	for i := range h {
		h[i] = byte(i) + 3
	}
	tx := gservertest.NewRawHashOnlyTransaction(h)
	txs := []transaction.Tx{tx}
	_, err := gsbd.EncodeBlockData(&buf, txs)
	require.NoError(t, err)

	// Expecting uncompressed for this data.
	b := buf.Bytes()
	require.Zero(t, b[0])

	// The size header correctly indicates the remaining data.
	rest, szLen := binary.Varint(b[1:])

	require.Equal(t, 1+szLen+int(rest), len(b))
}

func TestEncodeBlockData_compressed(t *testing.T) {
	t.Parallel()

	// Ten transactions that have mostly zero bytes in their hashes,
	// should be obviously compressed.
	txs := make([]transaction.Tx, 10)
	for i := range txs {
		txs[i] = gservertest.NewHashOnlyTransaction(uint64(i))
	}

	var buf bytes.Buffer
	_, err := gsbd.EncodeBlockData(&buf, txs)
	require.NoError(t, err)

	b := buf.Bytes()

	// Expecting snappy compressed for this data.
	require.Equal(t, byte(1), b[0])

	// The size header correctly indicates the remaining data.
	rest, szLen := binary.Varint(b[1:])

	require.Equal(t, 1+szLen+int(rest), len(b))
}

func TestEncodeBlockData_panicsOnEmptyTxs(t *testing.T) {
	t.Parallel()

	require.Panics(t, func() {
		_, _ = gsbd.EncodeBlockData(new(bytes.Buffer), nil)
	})
}
