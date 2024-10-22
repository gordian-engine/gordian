package gp2papi_test

import (
	"context"
	"testing"

	"github.com/gordian-engine/gordian/gcosmos/gcstore/gcmemstore"
	"github.com/gordian-engine/gordian/gcosmos/gserver/internal/gp2papi"
	"github.com/gordian-engine/gordian/gcosmos/gserver/internal/gsbd"
	"github.com/gordian-engine/gordian/gcrypto"
	"github.com/gordian-engine/gordian/internal/gtest"
	"github.com/gordian-engine/gordian/tm/tmcodec/tmjson"
	"github.com/gordian-engine/gordian/tm/tmp2p/tmlibp2p"
	"github.com/gordian-engine/gordian/tm/tmp2p/tmlibp2p/tmlibp2ptest"
	"github.com/gordian-engine/gordian/tm/tmstore/tmmemstore"
	"github.com/stretchr/testify/require"
)

type Fixture struct {
	BlockDataStore       *gcmemstore.BlockDataStore
	CommittedHeaderStore *tmmemstore.CommittedHeaderStore

	Cache *gsbd.RequestCache

	DataHost *gp2papi.DataHost

	P2PHostConn   *tmlibp2p.Connection
	P2PClientConn *tmlibp2p.Connection

	Codec tmjson.MarshalCodec
}

func NewFixture(t *testing.T, ctx context.Context) *Fixture {
	t.Helper()

	reg := new(gcrypto.Registry)
	gcrypto.RegisterEd25519(reg)

	codec := tmjson.MarshalCodec{CryptoRegistry: reg}

	// tmlibp2ptest probably isn't exactly the right network setup to use,
	// as it does extra work for consensus message setup;
	// but it does all the other heavy lifting we need,
	// and it's already written, so we are using it here.
	log := gtest.NewLogger(t)
	net, err := tmlibp2ptest.NewNetwork(ctx, log.With("sys", "net"), codec)
	require.NoError(t, err)
	t.Cleanup(net.Wait)

	host, err := net.Connect(ctx)
	require.NoError(t, err)

	client, err := net.Connect(ctx)
	require.NoError(t, err)

	require.NoError(t, net.Stabilize(ctx))

	chs := tmmemstore.NewCommittedHeaderStore()
	bds := gcmemstore.NewBlockDataStore()
	dh := gp2papi.NewDataHost(
		ctx, log,
		host.Host().Libp2pHost(),
		chs,
		bds,
		codec,
	)
	t.Cleanup(dh.Wait)

	return &Fixture{
		CommittedHeaderStore: chs,
		BlockDataStore:       bds,

		Cache: gsbd.NewRequestCache(),

		DataHost: dh,

		P2PHostConn:   host,
		P2PClientConn: client,

		Codec: codec,
	}
}
