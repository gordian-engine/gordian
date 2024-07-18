package gsi_test

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"testing"

	"github.com/rollchains/gordian/gcosmos/gserver/internal/gsi"
	"github.com/rollchains/gordian/internal/gtest"
	"github.com/rollchains/gordian/tm/tmstore/tmmemstore"
	"github.com/stretchr/testify/require"
)

func TestHTTPServer_Blocks_Watermark(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ln, err := (new(net.ListenConfig)).Listen(ctx, "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := "http://" + ln.Addr().String() + "/blocks/watermark"

	ms := tmmemstore.NewMirrorStore()

	h, err := gsi.NewHTTPServer(ctx, gtest.NewLogger(t), gsi.HTTPServerConfig{
		Listener:    ln,
		MirrorStore: ms,
		Libp2pHost:  nil,
		Libp2pconn:  nil,
	})
	require.NoError(t, err)

	defer h.Wait()
	defer cancel()

	t.Run("error when mirror store is uninitialized", func(t *testing.T) {
		resp, err := http.Get(addr)
		require.NoError(t, err)
		defer resp.Body.Close()

		require.Equal(t, http.StatusInternalServerError, resp.StatusCode)
	})

	t.Run("returns value of initially set network height round", func(t *testing.T) {
		require.NoError(t, ms.SetNetworkHeightRound(ctx, 2, 0, 1, 1))

		resp, err := http.Get(addr)
		require.NoError(t, err)
		defer resp.Body.Close()

		require.Equal(t, http.StatusOK, resp.StatusCode)

		var m map[string]uint
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&m))

		exp := map[string]uint{
			"VotingHeight": 2,
			"VotingRound":  0,

			"CommittingHeight": 1,
			"CommittingRound":  1,
		}

		require.Equal(t, exp, m)
	})

	t.Run("returns value of updated network height round", func(t *testing.T) {
		require.NoError(t, ms.SetNetworkHeightRound(ctx, 3, 4, 2, 0))

		resp, err := http.Get(addr)
		require.NoError(t, err)
		defer resp.Body.Close()

		require.Equal(t, http.StatusOK, resp.StatusCode)

		var m map[string]uint
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&m))

		exp := map[string]uint{
			"VotingHeight": 3,
			"VotingRound":  4,

			"CommittingHeight": 2,
			"CommittingRound":  0,
		}

		require.Equal(t, exp, m)
	})
}
