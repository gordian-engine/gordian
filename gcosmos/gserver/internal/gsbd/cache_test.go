package gsbd_test

import (
	"testing"

	"github.com/gordian-engine/gordian/gcosmos/gserver/gservertest"
	"github.com/gordian-engine/gordian/gcosmos/gserver/internal/gsbd"
	"github.com/gordian-engine/gordian/internal/gtest"
	"github.com/stretchr/testify/require"
)

func TestRequestCache_SetInFlight(t *testing.T) {
	t.Parallel()

	c := gsbd.NewRequestCache()

	ready := make(chan struct{})
	bdr := gsbd.BlockDataRequest{
		Ready: ready,
	}
	c.SetInFlight("id", &bdr)

	got, ok := c.Get("id")
	require.True(t, ok)

	// Not really well formatted, but fine for this test
	// since the cache has no insight into these values.
	bdr.Transactions = append(bdr.Transactions, gservertest.NewHashOnlyTransaction(1))
	bdr.EncodedTransactions = bdr.Transactions[0].Bytes()

	close(ready)

	_ = gtest.ReceiveSoon(t, got.Ready)

	require.Equal(t, bdr.Transactions, got.Transactions)
	require.Equal(t, bdr.EncodedTransactions, got.EncodedTransactions)
}
