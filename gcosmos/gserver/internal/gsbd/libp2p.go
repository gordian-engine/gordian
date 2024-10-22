package gsbd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"

	"cosmossdk.io/core/transaction"
	libp2phost "github.com/libp2p/go-libp2p/core/host"
	libp2pnetwork "github.com/libp2p/go-libp2p/core/network"
	libp2ppeer "github.com/libp2p/go-libp2p/core/peer"
	libp2pprotocol "github.com/libp2p/go-libp2p/core/protocol"
)

// This prefix is used by the proposer hosting block data.
// "Full nodes" hosting block data should use the full_block endpoint declared elsewhere.
const ProposedBlockDataV1Prefix = "/gordian/proposed_blockdata/v1/"

type Libp2pHost struct {
	log *slog.Logger

	host libp2phost.Host
}

func NewLibp2pProviderHost(
	log *slog.Logger,
	host libp2phost.Host,
) *Libp2pHost {
	return &Libp2pHost{
		log:  log,
		host: host,
	}
}

func (h *Libp2pHost) Provide(
	ctx context.Context,
	height uint64, round uint32,
	pendingTxs []transaction.Tx,
) (ProvideResult, error) {
	if len(pendingTxs) == 0 {
		panic(errors.New(
			"BUG: do not call Provide without at least one transaction",
		))
	}

	var buf bytes.Buffer
	sz, err := EncodeBlockData(&buf, pendingTxs)
	if err != nil {
		return ProvideResult{}, fmt.Errorf(
			"failed to encode block data: %w", err,
		)
	}
	encoded := buf.Bytes()

	dataID := DataID(height, round, uint32(sz), pendingTxs)

	pID := libp2pprotocol.ID(ProposedBlockDataV1Prefix + dataID)
	h.host.SetStreamHandler(pID, h.makeBlockDataHandler(encoded))

	// TODO: we need a way to prune old handlers.
	// Currently we just leak a handler every time we propose a block.

	ai := libp2phost.InfoFromHost(h.host)
	jai, err := json.Marshal(ai)
	if err != nil {
		return ProvideResult{}, fmt.Errorf("failed to marshal AddrInfo %v: %w", ai, err)
	}
	locs := []Location{
		{
			Scheme: Libp2pScheme,
			Addr:   string(jai),
		},
	}

	return ProvideResult{
		DataID:  dataID,
		Addrs:   locs,
		Encoded: encoded,
	}, nil
}

const (
	uncompressedHeader byte = 0
	snappyHeader       byte = 1
)

// makeBlockDataHandler returns a handler to be set on the router,
// to serve the pre-encoded data returned from [EncodeBlockData].
func (h *Libp2pHost) makeBlockDataHandler(encodedData []byte) libp2pnetwork.StreamHandler {
	outerLog := h.log.With("handler", "blockdata")
	return func(s libp2pnetwork.Stream) {
		defer s.Close()

		// We will not accept incoming writes on this stream,
		// so close for reads immediately.
		_ = s.CloseRead()

		if _, err := io.Copy(s, bytes.NewReader(encodedData)); err != nil {
			outerLog.Debug("Failed to copy encoded data to stream", "err", err)
			return
		}

		// Success.
	}
}

type Libp2pClient struct {
	// While we had fine-grained values in the Libp2pHost,
	// we need to hold on to the Host directly here in the client
	// because there is no apparent other interface to Connect
	// to a remote peer by its AddrInfo.

	log *slog.Logger

	h libp2phost.Host

	decoder transaction.Codec[transaction.Tx]
}

func NewLibp2pClient(
	log *slog.Logger,
	host libp2phost.Host,
	decoder transaction.Codec[transaction.Tx],
) *Libp2pClient {
	return &Libp2pClient{log: log, h: host, decoder: decoder}
}

func (c *Libp2pClient) Retrieve(
	ctx context.Context,
	ai libp2ppeer.AddrInfo,
	dataID string,
) ([]transaction.Tx, error) {
	dec, err := NewBlockDataDecoder(dataID, c.decoder)
	if err != nil {
		return nil, fmt.Errorf("failed to make block data decoder: %w", err)
	}

	// Ensure we have a connection.
	if err := c.h.Connect(ctx, ai); err != nil {
		return nil, fmt.Errorf("failed to connect to peer: %w", err)
	}

	// Open the stream to the peer.
	s, err := c.h.NewStream(ctx, ai.ID, libp2pprotocol.ID(ProposedBlockDataV1Prefix+dataID))
	if err != nil {
		return nil, fmt.Errorf("failed to open stream to peer: %w", err)
	}
	defer s.Close()

	if err := s.CloseWrite(); err != nil {
		c.log.Info("Failed to close stream for write", "err", err)
		// Okay to continue anyway?
	}

	txs, err := dec.Decode(s)
	if err != nil {
		return nil, fmt.Errorf("failed to decode block data: %w", err)
	}

	return txs, nil
}
