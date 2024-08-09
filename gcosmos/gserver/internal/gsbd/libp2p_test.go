package gsbd_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"slices"
	"strings"
	"testing"

	"cosmossdk.io/core/transaction"
	authtx "cosmossdk.io/x/auth/tx"
	banktypes "cosmossdk.io/x/bank/types"
	stakingtypes "cosmossdk.io/x/staking/types"
	"cosmossdk.io/x/tx/signing"
	"github.com/cosmos/cosmos-sdk/codec"
	addresscodec "github.com/cosmos/cosmos-sdk/codec/address"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	"github.com/cosmos/cosmos-sdk/std"
	libp2phost "github.com/libp2p/go-libp2p/core/host"
	libp2pnetwork "github.com/libp2p/go-libp2p/core/network"
	libp2ppeer "github.com/libp2p/go-libp2p/core/peer"
	libp2pprotocol "github.com/libp2p/go-libp2p/core/protocol"
	"github.com/rollchains/gordian/gcosmos/gccodec"
	"github.com/rollchains/gordian/gcosmos/gserver/internal/gsbd"
	"github.com/rollchains/gordian/internal/gtest"
	"github.com/rollchains/gordian/tm/tmcodec/tmjson"
	"github.com/rollchains/gordian/tm/tmp2p/tmlibp2p/tmlibp2ptest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Generated from one of the gcosmos integration tests.
// We don't really care about the actual content, so long as it is a transaction.Tx.
const (
	txSendJSON = `{"body":{"messages":[{"@type":"/cosmos.bank.v1beta1.MsgSend","from_address":"cosmos1r5v5srda7xfth3hn2s26txvrcrntldjumt8mhl","to_address":"cosmos1znj9yaxy2yjaldx20833zwayjduutddxf0czje","amount":[{"denom":"stake","amount":"100"}]}],"memo":"","timeout_height":"0","unordered":false,"timeout_timestamp":"0001-01-01T00:00:00Z","extension_options":[],"non_critical_extension_options":[]},"auth_info":{"signer_infos":[{"public_key":{"@type":"/cosmos.crypto.secp256k1.PubKey","key":"ArpmqEz3g5rxcqE+f8n15wCMuLyhWF+PO6+zA57aPB/d"},"mode_info":{"single":{"mode":"SIGN_MODE_DIRECT"}},"sequence":"30"}],"fee":{"amount":[],"gas_limit":"200000","payer":"cosmos1r5v5srda7xfth3hn2s26txvrcrntldjumt8mhl","granter":""},"tip":null},"signatures":["llNpiex9bSOHt4ai+C5KCE7Qo/7zn3ijZGo4dhBRpLNnLtMlZvuTTYCxeNecWaPO4r5Il9L6+nNkFbANKsolFw=="]}`

	txDelegateJSON = `{"body":{"messages":[{"@type":"/cosmos.staking.v1beta1.MsgDelegate","delegator_address":"cosmos1r5v5srda7xfth3hn2s26txvrcrntldjumt8mhl","validator_address":"cosmosvaloper1qszke34txqmm83atvx9whxnm2fsh7yr9u9a2n8","amount":{"denom":"stake","amount":"5000"}}],"memo":"","timeout_height":"0","unordered":false,"timeout_timestamp":"0001-01-01T00:00:00Z","extension_options":[],"non_critical_extension_options":[]},"auth_info":{"signer_infos":[{"public_key":{"@type":"/cosmos.crypto.secp256k1.PubKey","key":"ArpmqEz3g5rxcqE+f8n15wCMuLyhWF+PO6+zA57aPB/d"},"mode_info":{"single":{"mode":"SIGN_MODE_DIRECT"}},"sequence":"30"}],"fee":{"amount":[],"gas_limit":"200000","payer":"cosmos1r5v5srda7xfth3hn2s26txvrcrntldjumt8mhl","granter":""},"tip":null},"signatures":["xCUgkUOumHPk4Uh9AuVPpkVnwOEGN3DD1lr7vv+2NVwZuSrWJwd3hYbrVsqz6qpJXwhrZHJq1gzqPoLEajHptw=="]}`
)

func TestLibp2p_roundTrip(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// tmlibp2ptest probably isn't exactly the right network setup to use,
	// as it does extra work for consensus message setup;
	// but it does all the other heavy lifting we need,
	// and it's already written, so we are using it here.
	log := gtest.NewLogger(t)
	net, err := tmlibp2ptest.NewNetwork(ctx, log.With("sys", "net"), tmjson.MarshalCodec{
		// I'm a little surprised this doesn't fail when we omit a registry.
		// I guess it's okay since we don't attempt to marshal any consensus messages here.
	})
	require.NoError(t, err)
	defer net.Wait()
	defer cancel()

	host, err := net.Connect(ctx)
	require.NoError(t, err)

	client, err := net.Connect(ctx)
	require.NoError(t, err)

	require.NoError(t, net.Stabilize(ctx))

	provider := gsbd.NewLibp2pProviderHost(log.With("sys", "host"), host.Host().Libp2pHost())

	ir := codectypes.NewInterfaceRegistry()

	// Normally this registration would happen through depinject
	// and the runtime manager, but we don't want all that weight here.
	std.RegisterInterfaces(ir)
	banktypes.RegisterInterfaces(ir)
	stakingtypes.RegisterInterfaces(ir)

	protoCodec := codec.NewProtoCodec(ir)
	txCfg, err := authtx.NewTxConfigWithOptions(protoCodec, authtx.ConfigOptions{
		SigningOptions: &signing.Options{
			AddressCodec:          addresscodec.NewBech32Codec("cosmos"),
			ValidatorAddressCodec: addresscodec.NewBech32Codec("cosmosvaloper"),
		},
	})
	require.NoError(t, err)

	sendTx, err := txCfg.TxJSONDecoder()([]byte(txSendJSON))
	require.NoError(t, err)

	delegateTx, err := txCfg.TxJSONDecoder()([]byte(txDelegateJSON))
	require.NoError(t, err)

	t.Run("single transaction", func(t *testing.T) {
		// Provide this set of transactions as the block data for 1/0.
		txs := []transaction.Tx{sendTx}
		res, err := provider.Provide(ctx, 1, 0, txs)
		require.NoError(t, err)

		// Parse out the host address from the provide result.
		var ai libp2ppeer.AddrInfo
		require.NoError(t, json.Unmarshal([]byte(res.Addrs[0].Addr), &ai))
		c := gsbd.NewLibp2pClient(
			log.With("sys", "client"),
			client.Host().Libp2pHost(),
			gccodec.NewTxDecoder(txCfg),
		)
		gotTxs, err := c.Retrieve(ctx, ai, res.DataID, res.DataSize)
		require.NoError(t, err)
		require.Len(t, gotTxs, 1)
		RequireEqualTx(t, sendTx, gotTxs[0])
	})

	t.Run("two transactions", func(t *testing.T) {
		// Provide this set of transactions as the block data for 1/0.
		txs := []transaction.Tx{sendTx, delegateTx}
		res, err := provider.Provide(ctx, 1, 1, txs)
		require.NoError(t, err)

		// Parse out the host address from the provide result.
		var ai libp2ppeer.AddrInfo
		require.NoError(t, json.Unmarshal([]byte(res.Addrs[0].Addr), &ai))
		c := gsbd.NewLibp2pClient(
			log.With("sys", "client"),
			client.Host().Libp2pHost(),
			gccodec.NewTxDecoder(txCfg),
		)
		gotTxs, err := c.Retrieve(ctx, ai, res.DataID, res.DataSize)
		require.NoError(t, err)
		require.Len(t, gotTxs, 2)
		RequireEqualTx(t, sendTx, gotTxs[0])
		RequireEqualTx(t, delegateTx, gotTxs[1])
	})
}

func TestLibp2p_errors(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// tmlibp2ptest probably isn't exactly the right network setup to use,
	// as it does extra work for consensus message setup;
	// but it does all the other heavy lifting we need,
	// and it's already written, so we are using it here.
	log := gtest.NewLogger(t)
	net, err := tmlibp2ptest.NewNetwork(ctx, log.With("sys", "net"), tmjson.MarshalCodec{
		// I'm a little surprised this doesn't fail when we omit a registry.
		// I guess it's okay since we don't attempt to marshal any consensus messages here.
	})
	require.NoError(t, err)
	defer net.Wait()
	defer cancel()

	host, err := net.Connect(ctx)
	require.NoError(t, err)

	provider := gsbd.NewLibp2pProviderHost(log.With("sys", "host"), host.Host().Libp2pHost())

	ir := codectypes.NewInterfaceRegistry()

	// Normally this registration would happen through depinject
	// and the runtime manager, but we don't want all that weight here.
	std.RegisterInterfaces(ir)
	banktypes.RegisterInterfaces(ir)
	stakingtypes.RegisterInterfaces(ir)

	protoCodec := codec.NewProtoCodec(ir)
	txCfg, err := authtx.NewTxConfigWithOptions(protoCodec, authtx.ConfigOptions{
		SigningOptions: &signing.Options{
			AddressCodec:          addresscodec.NewBech32Codec("cosmos"),
			ValidatorAddressCodec: addresscodec.NewBech32Codec("cosmosvaloper"),
		},
	})
	require.NoError(t, err)

	sendTx, err := txCfg.TxJSONDecoder()([]byte(txSendJSON))
	require.NoError(t, err)

	delegateTx, err := txCfg.TxJSONDecoder()([]byte(txDelegateJSON))
	require.NoError(t, err)

	// Make a temporary client to both read from the real host
	// and provide some invalid data.
	tempClient, err := net.Connect(ctx)
	require.NoError(t, err)
	goodClient, err := net.Connect(ctx)
	require.NoError(t, err)

	// Now we need to Provide several variations of real data,
	// in order to host the wrong things.
	justSendRes, err := provider.Provide(ctx, 1, 0, []transaction.Tx{sendTx})
	require.NoError(t, err)

	justDelegateRes, err := provider.Provide(ctx, 2, 0, []transaction.Tx{delegateTx})
	require.NoError(t, net.Stabilize(ctx))

	sendThenDelegateRes, err := provider.Provide(ctx, 3, 0, []transaction.Tx{sendTx, delegateTx})
	require.NoError(t, net.Stabilize(ctx))

	delegateThenSendRes, err := provider.Provide(ctx, 4, 0, []transaction.Tx{delegateTx, sendTx})
	require.NoError(t, net.Stabilize(ctx))

	// Now we gather the real data for those cases.
	// First we need the host's address.
	// It should be the same across all the results, so just take from the first one.
	var goodHostAddrInfo libp2ppeer.AddrInfo
	require.NoError(t, json.Unmarshal([]byte(justSendRes.Addrs[0].Addr), &goodHostAddrInfo))

	// Make the temp client connect to the main host.
	require.NoError(t, tempClient.Host().Libp2pHost().Connect(ctx, goodHostAddrInfo))

	// Now open a stream for each result and collect the data.
	s, err := tempClient.Host().Libp2pHost().NewStream(
		ctx, goodHostAddrInfo.ID, libp2pprotocol.ID("/gordian/blockdata/v1/"+justSendRes.DataID),
	)
	require.NoError(t, err)
	defer s.Close()
	justSendData, err := io.ReadAll(s)
	require.NoError(t, err)
	s.Close()

	s, err = tempClient.Host().Libp2pHost().NewStream(
		ctx, goodHostAddrInfo.ID, libp2pprotocol.ID("/gordian/blockdata/v1/"+justDelegateRes.DataID),
	)
	require.NoError(t, err)
	defer s.Close()
	justDelegateData, err := io.ReadAll(s)
	require.NoError(t, err)
	s.Close()

	s, err = tempClient.Host().Libp2pHost().NewStream(
		ctx, goodHostAddrInfo.ID, libp2pprotocol.ID("/gordian/blockdata/v1/"+sendThenDelegateRes.DataID),
	)
	require.NoError(t, err)
	defer s.Close()
	sendThenDelegateData, err := io.ReadAll(s)
	require.NoError(t, err)
	s.Close()

	s, err = tempClient.Host().Libp2pHost().NewStream(
		ctx, goodHostAddrInfo.ID, libp2pprotocol.ID("/gordian/blockdata/v1/"+delegateThenSendRes.DataID),
	)
	require.NoError(t, err)
	defer s.Close()
	delegateThenSendData, err := io.ReadAll(s)
	require.NoError(t, err)
	s.Close()

	// Now we can start hosting some bad data.
	badAddrInfo := *(libp2phost.InfoFromHost(tempClient.Host().Libp2pHost()))

	require.NoError(t, goodClient.Host().Libp2pHost().Connect(ctx, badAddrInfo))
	goodProvideClient := gsbd.NewLibp2pClient(
		log.With("sys", "good_client"),
		goodClient.Host().Libp2pHost(),
		gccodec.NewTxDecoder(txCfg),
	)

	// The following subtests set up an incorrect host
	// and make assertions against a particular error.
	// The Provide/Retrieve interfaces are not returning strongly typed errors,
	// so we just assert against error message content
	// to ensure we are hitting the expected type of error.
	// It may be slightly brittle, but it's simpler than introducing
	// a whole set of error types for this narrow use case.

	t.Run("correct count=1 but wrong hash", func(t *testing.T) {
		pID := libp2pprotocol.ID("/gordian/blockdata/v1/" + justSendRes.DataID)
		tempClient.Host().Libp2pHost().SetStreamHandler(pID, func(s libp2pnetwork.Stream) {
			defer s.Close()
			_, _ = io.Copy(s, bytes.NewReader(justDelegateData))
		})

		_, err := goodProvideClient.Retrieve(ctx, badAddrInfo, justSendRes.DataID, justDelegateRes.DataSize)
		require.Error(t, err)
		require.Contains(t, err.Error(), "transactions hash")
	})

	t.Run("correct hash but wrong count", func(t *testing.T) {
		// Reaching into the data ID internals slightly.
		// This should replace the tx_count=1 to 2.
		badID := strings.Replace(justSendRes.DataID, ":1:", ":2:", 1)
		tempClient.Host().Libp2pHost().SetStreamHandler(
			"/gordian/blockdata/v1/"+libp2pprotocol.ID(badID),
			func(s libp2pnetwork.Stream) {
				// Send correct sendTx data, but the ID details the wrong count.
				defer s.Close()
				_, _ = io.Copy(s, bytes.NewReader(justSendData))
			})

		_, err := goodProvideClient.Retrieve(ctx, badAddrInfo, badID, justSendRes.DataSize)
		require.Error(t, err)
		require.Contains(t, err.Error(), "incorrect number of encoded transactions")
	})

	t.Run("all correct but wrong expected uncompressed size", func(t *testing.T) {
		tempClient.Host().Libp2pHost().SetStreamHandler(
			"/gordian/blockdata/v1/"+libp2pprotocol.ID(sendThenDelegateRes.DataID),
			func(s libp2pnetwork.Stream) {
				defer s.Close()
				_, _ = io.Copy(s, bytes.NewReader(sendThenDelegateData))
			})

		_, err := goodProvideClient.Retrieve(ctx, badAddrInfo, sendThenDelegateRes.DataID, sendThenDelegateRes.DataSize+1)
		require.Error(t, err)
		require.Contains(t, err.Error(), "decoded length")
		require.Contains(t, err.Error(), "differed from expected size")
	})

	t.Run("all correct but truncated compressed data", func(t *testing.T) {
		tempClient.Host().Libp2pHost().SetStreamHandler(
			"/gordian/blockdata/v1/"+libp2pprotocol.ID(delegateThenSendRes.DataID),
			func(s libp2pnetwork.Stream) {
				defer s.Close()
				_, _ = io.Copy(s, bytes.NewReader(delegateThenSendData[:len(delegateThenSendData)-20]))
			})

		_, err := goodProvideClient.Retrieve(ctx, badAddrInfo, delegateThenSendRes.DataID, delegateThenSendRes.DataSize)
		require.Error(t, err)
		require.Contains(t, err.Error(), "failed to read full compressed data")
	})

	t.Run("fully corrupt data", func(t *testing.T) {
		tempClient.Host().Libp2pHost().SetStreamHandler(
			"/gordian/blockdata/v1/"+libp2pprotocol.ID(delegateThenSendRes.DataID),
			func(s libp2pnetwork.Stream) {
				defer s.Close()
				corrupt := slices.Clone(delegateThenSendData)
				for i := range corrupt {
					corrupt[i] += uint8(i)
				}
				_, _ = io.Copy(s, bytes.NewReader(corrupt))
			})

		_, err := goodProvideClient.Retrieve(ctx, badAddrInfo, delegateThenSendRes.DataID, delegateThenSendRes.DataSize)
		require.Error(t, err)
		// Not asserting anything particular about this message.
	})
}

func RequireEqualTx(t *testing.T, want, got transaction.Tx) {
	t.Helper()

	// Using assert instead of require in this helper,
	// so we can see multiple messages about the diff.
	assert.Equal(t, want.Hash(), got.Hash())

	// Assuming the Bytes() output is deterministic.
	// assert.Equal(t, want.Bytes(), got.Bytes())
	wantBytes, gotBytes := want.Bytes(), got.Bytes()
	assert.Truef(
		t,
		bytes.Equal(wantBytes, gotBytes),
		"encoded bytes differed:\nwant=%x\ngot= %x",
		wantBytes, gotBytes,
	)

	wantMsgs, err := want.GetMessages()
	require.NoError(t, err)
	gotMsgs, err := got.GetMessages()
	require.NoError(t, err)

	require.Equal(t, wantMsgs, gotMsgs)

	if t.Failed() {
		// Stop the test if any assertion failed.
		t.FailNow()
	}
}
