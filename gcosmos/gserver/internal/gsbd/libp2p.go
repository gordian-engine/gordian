package gsbd

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"

	"cosmossdk.io/core/transaction"
	"github.com/golang/snappy"
	libp2phost "github.com/libp2p/go-libp2p/core/host"
	libp2pnetwork "github.com/libp2p/go-libp2p/core/network"
	libp2ppeer "github.com/libp2p/go-libp2p/core/peer"
	libp2pprotocol "github.com/libp2p/go-libp2p/core/protocol"
	"github.com/rollchains/gordian/gcosmos/gccodec"
)

const blockDataV1Prefix = "/gordian/blockdata/v1/"

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

	items := make([][]byte, len(pendingTxs))
	for i, tx := range pendingTxs {
		items[i] = tx.Bytes()
	}

	j, err := json.Marshal(items)
	if err != nil {
		return ProvideResult{}, fmt.Errorf(
			"failed to marshal items: %w", err,
		)
	}

	dataID := DataID(height, round, pendingTxs)

	pID := libp2pprotocol.ID(blockDataV1Prefix + dataID)
	h.host.SetStreamHandler(pID, h.makeBlockDataHandler(j))

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
		DataID:   dataID,
		DataSize: len(j),
		Addrs:    locs,
	}, nil
}

const (
	uncompressedHeader byte = 0
	snappyHeader       byte = 1
)

// makeBlockDataHandler returns a handler to be set on the router,
// to serve a snappy-compressed version of j.
//
// Currently, the use of snappy compression is mandatory.
// We could add a v2 endpoint that allows the client to specify a preference,
// or we could possibly try multiple compression schemes
// and only serve the best one.
func (h *Libp2pHost) makeBlockDataHandler(j []byte) libp2pnetwork.StreamHandler {
	c := snappy.Encode(nil, j)
	szBuf := binary.AppendVarint(nil, int64(len(c)))
	outerLog := h.log.With("handler", "blockdata")

	if len(c) >= len(j) {
		h.log.Warn("TODO: add handler to serve uncompressed block data", "growth", len(j)-len(c))
	}

	return func(s libp2pnetwork.Stream) {
		defer s.Close()

		// We will not accept incoming writes on this stream,
		// so close for reads immediately.
		_ = s.CloseRead()

		if _, err := s.Write([]byte{snappyHeader}); err != nil {
			outerLog.Debug("Failed to write snappy header", "err", err)
			return
		}

		if _, err := io.Copy(s, bytes.NewReader(szBuf)); err != nil {
			outerLog.Debug("Failed to write size", "err", err)
			return
		}

		src := bytes.NewReader(c)
		if _, err := src.WriteTo(s); err != nil {
			outerLog.Debug("Failed to write compressed data", "err", err)
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

	decoder gccodec.TxDecoder[transaction.Tx]
}

func NewLibp2pClient(
	log *slog.Logger,
	host libp2phost.Host,
	decoder gccodec.TxDecoder[transaction.Tx],
) *Libp2pClient {
	return &Libp2pClient{log: log, h: host, decoder: decoder}
}

func (c *Libp2pClient) Retrieve(
	ctx context.Context,
	ai libp2ppeer.AddrInfo,
	dataID string,
	expDecodedSize int,
) ([]transaction.Tx, error) {
	// Parse the data ID before anything else.
	// We don't need the height or round,
	// so we could potentially justify a lighter weight parser...
	// but this isn't really in a hot path, so it's probably fine.
	_, _, nTxs, txsHash, err := ParseDataID(dataID)
	if err != nil {
		return nil, fmt.Errorf("failed to parse data ID: %w", err)
	}

	// Ensure we have a connection.
	if err := c.h.Connect(ctx, ai); err != nil {
		return nil, fmt.Errorf("failed to connect to peer: %w", err)
	}

	// Open the stream to the peer.
	s, err := c.h.NewStream(ctx, ai.ID, libp2pprotocol.ID(blockDataV1Prefix+dataID))
	if err != nil {
		return nil, fmt.Errorf("failed to open stream to peer: %w", err)
	}
	defer s.Close()

	if err := s.CloseWrite(); err != nil {
		c.log.Info("Failed to close stream for write", "err", err)
		// Okay to continue anyway?
	}

	var firstByte [1]byte
	if _, err := s.Read(firstByte[:]); err != nil {
		return nil, fmt.Errorf("failed to read first byte from stream: %w", err)
	}

	var encoded []byte
	switch firstByte[0] {
	case uncompressedHeader:
		panic("TODO")
	case snappyHeader:
		encoded, err = c.readSnappy(expDecodedSize, s)
		if err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unrecognized header byte %x", firstByte[0])
	}

	// We're done reading data, so close the connection now.
	_ = s.Close()

	return c.decode(encoded, nTxs, txsHash)
}

func (c *Libp2pClient) readSnappy(
	expDecodedSize int,
	s libp2pnetwork.Stream,
) ([]byte, error) {
	// The next "header" section following the type header,
	// is the compressed size as a uvarint.
	//
	// To read that varint, we need an io.ByteReader.
	// The Stream type does not directly implement ByteReader,
	// so per the io.ByteReader docs we use a bufio.Reader instead.
	//
	// We only buffer the maximum size of a 64 bit varint.
	// That's probably more than we need, but it's fine.
	cSizeReader := bufio.NewReaderSize(s, binary.MaxVarintLen64)
	cSize64, err := binary.ReadVarint(cSizeReader)
	if err != nil {
		return nil, fmt.Errorf("failed to read compressed size: %w", err)
	}
	cSize := int(cSize64)
	if cSize <= 0 {
		return nil, fmt.Errorf(
			"invalid compressed size: %d (%d as 64-bit) must be positive",
			cSize, cSize64,
		)
	}

	maxCSize := snappy.MaxEncodedLen(expDecodedSize)
	if cSize > maxCSize {
		return nil, fmt.Errorf(
			"invalid compressed size: %d larger than max snappy-encoded size %d of %d",
			cSize, maxCSize, expDecodedSize,
		)
	}

	// Now that we know the size of the compressed data,
	// we can read it into an appropriately sized buffer.
	// Unfortunately, we may have some of the compressed data remaining in the varint reader,
	// so we need to concatenate its remaining data with whatever is left in the stream.
	buffered, err := cSizeReader.Peek(cSizeReader.Buffered())
	if err != nil {
		return nil, fmt.Errorf("impossible: failed to peek buffered length: %w", err)
	}

	r := io.MultiReader(bytes.NewReader(buffered), s)
	cBuf := make([]byte, cSize)
	if _, err := io.ReadFull(r, cBuf); err != nil {
		return nil, fmt.Errorf("failed to read full compressed data: %w", err)
	}

	// Now, we finally have the full compressed data.
	// Before allocating the slice for the uncompressed version,
	// double check that the uncompressed size will match.
	uSize, err := snappy.DecodedLen(cBuf)
	if err != nil {
		return nil, fmt.Errorf("failed to read decoded length from compressed data: %w", err)
	}
	if uSize != expDecodedSize {
		return nil, fmt.Errorf(
			"compressed data's decoded length %d differed from expected size %d",
			uSize, expDecodedSize,
		)
	}

	// Decoding into nil will right-size the output buffer anyway.
	// This could potentially use a sync.Pool to reuse allocations,
	// but right now we don't have sufficient information to justify that.
	u, err := snappy.Decode(nil, cBuf)
	if err != nil {
		return nil, fmt.Errorf("failed to decode snappy data: %w", err)
	}

	if len(u) != expDecodedSize {
		return nil, fmt.Errorf(
			"impossible: decoded length %d differed from expected length %d",
			len(u), expDecodedSize,
		)
	}

	return u, nil
}

func (c *Libp2pClient) decode(
	encoded []byte,
	expNTxs int,
	expTxsHash [txsHashSize]byte,
) ([]transaction.Tx, error) {
	// The encoded data is currently a JSON array of byte slices.
	// Not efficient, but simple to use.
	var items [][]byte
	if err := json.Unmarshal(encoded, &items); err != nil {
		return nil, fmt.Errorf("failed to unmarshal retrieved data: %w", err)
	}
	if len(items) != expNTxs {
		return nil, fmt.Errorf(
			"unmarshalled incorrect number of encoded transactions: want %d, got %d",
			expNTxs, len(items),
		)
	}

	txs := make([]transaction.Tx, len(items))
	for i := range items {
		tx, err := c.decoder.Decode(items[i])
		if err != nil {
			return nil, fmt.Errorf("failed to decode transaction at index %d: %w", i, err)
		}

		txs[i] = tx

		// Possibly help GC by removing the reference to the decoded byte slice
		// as soon as we're done with it.
		items[i] = nil
	}

	// Finally, confirm the full transaction hash.
	// If this ends up being a hot path in CPU use,
	// we could potentially accumulate the individual transaction hashes
	// while decoding the transactions.
	// But, doing it late is probably the better defensive choice.
	gotTxsHash := TxsHash(txs)
	if gotTxsHash != expTxsHash {
		return nil, fmt.Errorf(
			"decoded transactions hash %x differed from input %x",
			gotTxsHash, expTxsHash,
		)
	}

	return txs, nil
}
