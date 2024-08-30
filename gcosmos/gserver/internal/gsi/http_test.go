package gsi_test

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"testing"

	"github.com/rollchains/gordian/gcosmos/gserver/internal/ggrpc"
	"github.com/rollchains/gordian/gcosmos/gserver/internal/gsi"
	"github.com/rollchains/gordian/gcrypto"
	"github.com/rollchains/gordian/internal/gtest"
	"github.com/rollchains/gordian/tm/tmconsensus"
	"github.com/rollchains/gordian/tm/tmconsensus/tmconsensustest"
	"github.com/rollchains/gordian/tm/tmstore/tmmemstore"
	"github.com/stretchr/testify/require"
)

func setupGrpcServer(t *testing.T, ctx context.Context, gsc ggrpc.GRPCServerConfig) *ggrpc.GordianGRPC {
	gln, err := (new(net.ListenConfig)).Listen(ctx, "tcp", "127.0.0.1:0")
	require.NoError(t, err)

	gsc.Listener = gln

	return ggrpc.NewGordianGRPCServer(ctx, gtest.NewLogger(t), gsc)
}

func TestHTTPServer_Blocks_Watermark(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ln, err := (new(net.ListenConfig)).Listen(ctx, "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := "http://" + ln.Addr().String() + "/blocks/watermark"

	ms := tmmemstore.NewMirrorStore()
	gordianGrpc := setupGrpcServer(t, ctx, ggrpc.GRPCServerConfig{
		MirrorStore: ms,
	})

	h := gsi.NewHTTPServer(ctx, gtest.NewLogger(t), gsi.HTTPServerConfig{
		Listener:      ln,
		GordianClient: gordianGrpc,
		Libp2pHost:    nil,
		Libp2pconn:    nil,
	})
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
			"voting_height": 2,
			"voting_round":  0,

			"committing_height": 1,
			"committing_round":  1,
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
			"voting_height": 3,
			"voting_round":  4,

			"committing_height": 2,
			"committing_round":  0,
		}

		require.Equal(t, exp, m)
	})
}

func TestHTTPServer_Validators(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ln, err := (new(net.ListenConfig)).Listen(ctx, "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := "http://" + ln.Addr().String() + "/validators"

	ms := tmmemstore.NewMirrorStore()
	fs := tmmemstore.NewFinalizationStore()
	reg := new(gcrypto.Registry)
	gcrypto.RegisterEd25519(reg)

	gordianGrpc := setupGrpcServer(t, ctx, ggrpc.GRPCServerConfig{
		FinalizationStore: fs,
		MirrorStore:       ms,
		CryptoRegistry:    reg,
	})

	h := gsi.NewHTTPServer(ctx, gtest.NewLogger(t), gsi.HTTPServerConfig{
		Listener: ln,

		GordianClient: gordianGrpc,
	})
	defer h.Wait()
	defer cancel()

	// Voting at 3/0, Committing at 2/0.
	require.NoError(t, ms.SetNetworkHeightRound(ctx, 3, 0, 2, 0))

	vals := tmconsensustest.DeterministicValidatorsEd25519(3).Vals()

	// Save the finalization at the committing height.
	// Besides the height, the only other thing we care about for this endpoint
	// is the validator slice.
	require.NoError(t, fs.SaveFinalization(
		ctx,
		2, 0,
		"block_hash",
		vals,
		"app_state_hash",
	))

	resp, err := http.Get(addr)
	require.NoError(t, err)
	defer resp.Body.Close()

	var output ggrpc.GetValidatorsResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&output))

	// Finalization height matches.
	require.Equal(t, uint64(2), *output.FinalizationHeight)

	// Validators match too.
	outVals := make([]tmconsensus.Validator, len(output.Validators))
	for i, v := range output.Validators {
		k, err := reg.Unmarshal(v.EncodedPubKey)
		require.NoErrorf(t, err, "failure marshaling output validator at index %d", i)
		outVals[i] = tmconsensus.Validator{
			Power:  v.Power,
			PubKey: k,
		}
	}

	require.True(t, tmconsensus.ValidatorSlicesEqual(vals, outVals))
}
