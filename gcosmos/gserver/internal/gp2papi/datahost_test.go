package gp2papi_test

import (
	"context"
	"encoding/json"
	"io"
	"testing"

	"github.com/gordian-engine/gordian/gcosmos/gserver/internal/gp2papi"
	"github.com/gordian-engine/gordian/gcosmos/gserver/internal/gsbd"
	"github.com/gordian-engine/gordian/tm/tmconsensus"
	"github.com/gordian-engine/gordian/tm/tmconsensus/tmconsensustest"
	libp2phost "github.com/libp2p/go-libp2p/core/host"
	libp2pprotocol "github.com/libp2p/go-libp2p/core/protocol"
	"github.com/stretchr/testify/require"
)

func TestDataHost_serveCommittedHeader(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dhfx := NewFixture(t, ctx)

	fx := tmconsensustest.NewStandardFixture(4)
	ph1 := fx.NextProposedHeader([]byte("app_data_1"), 0)
	fx.SignProposal(ctx, &ph1, 0)

	precommitProofs := fx.PrecommitProofMap(ctx, 1, 0, map[string][]int{
		string(ph1.Header.Hash): {0, 1, 2},
		"":                      {3},
	})
	fx.CommitBlock(ph1.Header, []byte("app_state_1"), 0, precommitProofs)
	nextPH := fx.NextProposedHeader([]byte("whatever"), 0)

	require.NoError(t, dhfx.CommittedHeaderStore.SaveCommittedHeader(ctx, tmconsensus.CommittedHeader{
		Header: ph1.Header,
		Proof:  nextPH.Header.PrevCommitProof,
	}))

	hostInfo := libp2phost.InfoFromHost(dhfx.P2PHostConn.Host().Libp2pHost())
	require.NoError(t, dhfx.P2PClientConn.Host().Libp2pHost().Connect(ctx, *hostInfo))

	s, err := dhfx.P2PClientConn.Host().Libp2pHost().NewStream(ctx, hostInfo.ID, libp2pprotocol.ID(
		"/gcosmos/committed_headers/v1/height/1",
	))
	require.NoError(t, err)
	defer s.Close()

	b, err := io.ReadAll(s)
	require.NoError(t, err)

	var jr gp2papi.JSONResult
	require.NoError(t, json.Unmarshal(b, &jr))
	require.Empty(t, jr.Err)

	var ch tmconsensus.CommittedHeader
	require.NoError(t, dhfx.Codec.UnmarshalCommittedHeader(jr.Result, &ch))

	require.Equal(t, tmconsensus.CommittedHeader{
		Header: ph1.Header,
		Proof:  nextPH.Header.PrevCommitProof,
	}, ch)

	// And if we try to get a header that hasn't been written, it errors.
	s, err = dhfx.P2PClientConn.Host().Libp2pHost().NewStream(ctx, hostInfo.ID, libp2pprotocol.ID(
		"/gcosmos/committed_headers/v1/height/30",
	))
	require.NoError(t, err)
	defer s.Close()

	b, err = io.ReadAll(s)
	require.NoError(t, err)
	jr = gp2papi.JSONResult{}
	require.NoError(t, json.Unmarshal(b, &jr))
	require.Empty(t, jr.Result)
	require.NotEmpty(t, jr.Err)
}

func TestDataHost_fullBlock(t *testing.T) {
	t.Run("with non-zero block data", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		dhfx := NewFixture(t, ctx)

		fx := tmconsensustest.NewStandardFixture(4)
		ph1 := fx.NextProposedHeader([]byte("app_data_1"), 0)
		fx.SignProposal(ctx, &ph1, 0)

		precommitProofs := fx.PrecommitProofMap(ctx, 1, 0, map[string][]int{
			string(ph1.Header.Hash): {0, 1, 2},
			"":                      {3},
		})
		fx.CommitBlock(ph1.Header, []byte("app_state_1"), 0, precommitProofs)
		nextPH := fx.NextProposedHeader([]byte("whatever"), 0)

		require.NoError(t, dhfx.CommittedHeaderStore.SaveCommittedHeader(ctx, tmconsensus.CommittedHeader{
			Header: ph1.Header,
			Proof:  nextPH.Header.PrevCommitProof,
		}))

		// There is no host-side validation on the block data,
		// so we can just store an arbitrary string.
		require.NoError(t, dhfx.BlockDataStore.SaveBlockData(
			ctx,
			ph1.Header.Height, string(ph1.Header.DataID),
			[]byte("some block data"),
		))

		// With both a committed header and block data,
		// we can get a valid result.
		hostInfo := libp2phost.InfoFromHost(dhfx.P2PHostConn.Host().Libp2pHost())
		require.NoError(t, dhfx.P2PClientConn.Host().Libp2pHost().Connect(ctx, *hostInfo))

		s, err := dhfx.P2PClientConn.Host().Libp2pHost().NewStream(ctx, hostInfo.ID, libp2pprotocol.ID(
			"/gcosmos/full_blocks/v1/height/1",
		))
		require.NoError(t, err)
		defer s.Close()

		b, err := io.ReadAll(s)
		require.NoError(t, err)

		var jr gp2papi.JSONResult
		require.NoError(t, json.Unmarshal(b, &jr))
		require.Empty(t, jr.Err)

		var fbr gp2papi.FullBlockResult
		require.NoError(t, json.Unmarshal(jr.Result, &fbr))

		require.Equal(t, "some block data", string(fbr.BlockData))
	})

	t.Run("with zero block data", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		dhfx := NewFixture(t, ctx)

		fx := tmconsensustest.NewStandardFixture(4)
		dataID := gsbd.DataID(1, 0, 0, nil) // Zero data ID
		ph1 := fx.NextProposedHeader([]byte(dataID), 0)
		fx.SignProposal(ctx, &ph1, 0)

		precommitProofs := fx.PrecommitProofMap(ctx, 1, 0, map[string][]int{
			string(ph1.Header.Hash): {0, 1, 2},
			"":                      {3},
		})
		fx.CommitBlock(ph1.Header, []byte("app_state_1"), 0, precommitProofs)
		nextPH := fx.NextProposedHeader([]byte("whatever"), 0)

		require.NoError(t, dhfx.CommittedHeaderStore.SaveCommittedHeader(ctx, tmconsensus.CommittedHeader{
			Header: ph1.Header,
			Proof:  nextPH.Header.PrevCommitProof,
		}))

		// Unlike the previous test, we do not store block data here.
		// The zero data ID gets special treatment in not attempting
		// to load from the block data store.
		hostInfo := libp2phost.InfoFromHost(dhfx.P2PHostConn.Host().Libp2pHost())
		require.NoError(t, dhfx.P2PClientConn.Host().Libp2pHost().Connect(ctx, *hostInfo))

		s, err := dhfx.P2PClientConn.Host().Libp2pHost().NewStream(ctx, hostInfo.ID, libp2pprotocol.ID(
			"/gcosmos/full_blocks/v1/height/1",
		))
		require.NoError(t, err)
		defer s.Close()

		b, err := io.ReadAll(s)
		require.NoError(t, err)

		var jr gp2papi.JSONResult
		require.NoError(t, json.Unmarshal(b, &jr))
		require.Empty(t, jr.Err)

		var fbr gp2papi.FullBlockResult
		require.NoError(t, json.Unmarshal(jr.Result, &fbr))

		require.Nil(t, fbr.BlockData)
	})

	t.Run("missing committed header at height", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		dhfx := NewFixture(t, ctx)

		// Nothing added to the header or block data stores.

		hostInfo := libp2phost.InfoFromHost(dhfx.P2PHostConn.Host().Libp2pHost())
		require.NoError(t, dhfx.P2PClientConn.Host().Libp2pHost().Connect(ctx, *hostInfo))

		s, err := dhfx.P2PClientConn.Host().Libp2pHost().NewStream(ctx, hostInfo.ID, libp2pprotocol.ID(
			"/gcosmos/full_blocks/v1/height/1",
		))
		require.NoError(t, err)
		defer s.Close()

		b, err := io.ReadAll(s)
		require.NoError(t, err)

		var jr gp2papi.JSONResult
		require.NoError(t, json.Unmarshal(b, &jr))
		require.Empty(t, jr.Result)
		require.NotEmpty(t, jr.Err)
		require.Contains(t, jr.Err, "height unknown")
	})
}
