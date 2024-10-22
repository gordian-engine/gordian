package gsi_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"cosmossdk.io/core/transaction"
	"github.com/gordian-engine/gordian/gcosmos/gserver/gservertest"
	"github.com/gordian-engine/gordian/gcosmos/gserver/internal/gsbd"
	"github.com/gordian-engine/gordian/gcosmos/gserver/internal/gsi"
	"github.com/gordian-engine/gordian/gcrypto"
	"github.com/gordian-engine/gordian/internal/gtest"
	"github.com/gordian-engine/gordian/tm/tmcodec/tmjson"
	"github.com/gordian-engine/gordian/tm/tmp2p/tmlibp2p"
	"github.com/gordian-engine/gordian/tm/tmp2p/tmlibp2p/tmlibp2ptest"
	"github.com/stretchr/testify/require"
)

func TestPBDRetriever_completedRequest(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pfx := NewPBDFixture(t, ctx)

	ph := gsbd.NewLibp2pProviderHost(pfx.Log.With("sys", "provider_host"), pfx.P2PHostConn.Host().Libp2pHost())

	tx := gservertest.NewHashOnlyTransaction(10)
	txs := []transaction.Tx{tx}
	res, err := ph.Provide(ctx, 1, 0, txs)
	require.NoError(t, err)

	r := gsi.NewPBDRetriever(ctx, pfx.Log.With("sys", "pbd_retriever"), gsi.PBDRetrieverConfig{
		RequestCache: pfx.Cache,
		Decoder:      gservertest.HashOnlyTransactionDecoder{},
		Host:         pfx.P2PClientConn.Host().Libp2pHost(),
		NWorkers:     2,
	})

	j, err := json.Marshal(gsi.ProposalDriverAnnotation{
		Locations: res.Addrs,
	})
	require.NoError(t, err)
	require.NoError(t, r.Retrieve(ctx, res.DataID, j))

	// The request cache has an entry for this data ID.
	bdr, ok := pfx.Cache.Get(res.DataID)
	require.True(t, ok)

	// We are retrieving from a host on the same machine
	// so it should complete very quickly.
	_ = gtest.ReceiveSoon(t, bdr.Ready)
}

type PBDFixture struct {
	Log *slog.Logger

	Cache *gsbd.RequestCache

	P2PHostConn   *tmlibp2p.Connection
	P2PClientConn *tmlibp2p.Connection
}

func NewPBDFixture(t *testing.T, ctx context.Context) *PBDFixture {
	t.Helper()

	// tmlibp2ptest probably isn't exactly the right network setup to use,
	// as it does extra work for consensus message setup;
	// but it does all the other heavy lifting we need,
	// and it's already written, so we are using it here.
	log := gtest.NewLogger(t)

	reg := new(gcrypto.Registry)
	gcrypto.RegisterEd25519(reg)

	codec := tmjson.MarshalCodec{CryptoRegistry: reg}
	net, err := tmlibp2ptest.NewNetwork(ctx, log.With("sys", "net"), codec)
	require.NoError(t, err)
	t.Cleanup(net.Wait)

	host, err := net.Connect(ctx)
	require.NoError(t, err)

	client, err := net.Connect(ctx)
	require.NoError(t, err)

	require.NoError(t, net.Stabilize(ctx))

	return &PBDFixture{
		Log: log,

		Cache: gsbd.NewRequestCache(),

		P2PHostConn:   host,
		P2PClientConn: client,
	}
}
