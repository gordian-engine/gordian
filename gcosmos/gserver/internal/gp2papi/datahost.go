package gp2papi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/gordian-engine/gordian/gcosmos/gcstore"
	"github.com/gordian-engine/gordian/gcosmos/gserver/internal/gsbd"
	"github.com/gordian-engine/gordian/tm/tmcodec"
	"github.com/gordian-engine/gordian/tm/tmconsensus"
	"github.com/gordian-engine/gordian/tm/tmstore"
	libp2phost "github.com/libp2p/go-libp2p/core/host"
	libp2pnetwork "github.com/libp2p/go-libp2p/core/network"
	libp2pprotocol "github.com/libp2p/go-libp2p/core/protocol"
)

// DataHost provides header and block data over libp2p.
//
// It is currently coupled to JSON encoding (see the tm/tmcodec/tmjson package).
// The codec interfaces will need updated to handle wrapping the value or an error,
// in order to decouple this from JSON.
type DataHost struct {
	// We unfortunately store the life context of the DataHost as a field,
	// because there does not appear to be a way to get
	// a context associated with a particular stream.
	ctx context.Context

	log *slog.Logger

	host libp2phost.Host

	chs tmstore.CommittedHeaderStore
	bds gcstore.BlockDataStore

	codec tmcodec.MarshalCodec

	done chan struct{}
}

const (
	// Just the committed header.
	headerV1HeightPrefix = "/gcosmos/committed_headers/v1/height/"

	// The committed header with the block data.
	fullBlockV1HeightPrefix = "/gcosmos/full_blocks/v1/height/"
)

func NewDataHost(
	ctx context.Context,
	log *slog.Logger,
	host libp2phost.Host,
	chs tmstore.CommittedHeaderStore,
	bds gcstore.BlockDataStore,
	codec tmcodec.MarshalCodec,
) *DataHost {
	h := &DataHost{
		ctx:   ctx,
		log:   log,
		host:  host,
		chs:   chs,
		bds:   bds,
		codec: codec,

		done: make(chan struct{}),
	}

	go h.waitForCancellation()

	h.host.SetStreamHandlerMatch(
		libp2pprotocol.ID(headerV1HeightPrefix),
		func(id libp2pprotocol.ID) bool {
			heightS, ok := strings.CutPrefix(string(id), headerV1HeightPrefix)
			if !ok {
				return false
			}

			if len(heightS) == 0 {
				return false
			}

			for _, rn := range heightS {
				if rn < '0' || rn > '9' {
					return false
				}
			}

			return true
		},
		h.handleCommittedHeaderStream,
	)

	h.host.SetStreamHandlerMatch(
		libp2pprotocol.ID(fullBlockV1HeightPrefix),
		func(id libp2pprotocol.ID) bool {
			heightS, ok := strings.CutPrefix(string(id), fullBlockV1HeightPrefix)
			if !ok {
				return false
			}

			if len(heightS) == 0 {
				return false
			}

			for _, rn := range heightS {
				if rn < '0' || rn > '9' {
					return false
				}
			}

			return true
		},
		h.handleFullBlockStream,
	)

	return h
}

func (h *DataHost) Wait() {
	<-h.done
}

func (h *DataHost) waitForCancellation() {
	// Removing the handler probably isn't strictly necessary,
	// but this would allow us to dynamically enable the service
	// without shutting down the whole process.
	<-h.ctx.Done()
	h.host.RemoveStreamHandler(libp2pprotocol.ID(headerV1HeightPrefix))
	close(h.done)
}

func (h *DataHost) handleCommittedHeaderStream(s libp2pnetwork.Stream) {
	// TODO: this is unfortunate that we need to distinguish errors from actual results
	// and we are stuck with JSON for now.
	// There isn't necessarily a clear way to generically handle the wrapped errors;
	// I don't think we can assume analogs to json.RawMessage in other encodings.
	// Or we adjust the tmcodec package to allow an OrError variant.
	//
	// We can live with this for now in any case.

	defer s.Close()

	// We don't read any input on this path.
	_ = s.CloseRead()

	// The stream value apparently has no way to retrieve a corresponding context,
	// so we use the root context associated with the DataHost
	// and add a 1-second timeout, which ought to suffice for any reasonable client.
	ctx, cancel := context.WithTimeout(h.ctx, time.Second)
	defer cancel()

	heightS, ok := strings.CutPrefix(string(s.Protocol()), headerV1HeightPrefix)
	if !ok {
		_ = json.NewEncoder(s).Encode(JSONResult{
			Err: "invalid protocol",
		})
		return
	}

	height, err := strconv.ParseUint(heightS, 10, 64)
	if err != nil {
		_ = json.NewEncoder(s).Encode(JSONResult{
			Err: "invalid height",
		})
		return
	}

	ch, err := h.chs.LoadCommittedHeader(ctx, height)
	if err != nil {
		// TODO: There are probably certain cases we do want to expose.
		// For now, just mask it.
		var res JSONResult
		if errors.As(err, new(tmconsensus.HeightUnknownError)) {
			// Special string that the client recognizes.
			res.Err = "height unknown"
		} else {
			res.Err = "failed to load header at height"
		}
		_ = json.NewEncoder(s).Encode(res)
		return
	}

	b, err := h.codec.MarshalCommittedHeader(ch)
	if err != nil {
		h.log.Info(
			"Failed to marshal requested committed header",
			"height", height,
			"err", err,
		)
		_ = json.NewEncoder(s).Encode(JSONResult{
			Err: "internal serialization error",
		})
		return
	}

	_ = json.NewEncoder(s).Encode(JSONResult{
		Result: b,
	})
}

func (h *DataHost) handleFullBlockStream(s libp2pnetwork.Stream) {
	// TODO: this is unfortunate that we need to distinguish errors from actual results
	// and we are stuck with JSON for now.
	// There isn't necessarily a clear way to generically handle the wrapped errors;
	// I don't think we can assume analogs to json.RawMessage in other encodings.
	// Or we adjust the tmcodec package to allow an OrError variant.
	//
	// We can live with this for now in any case.

	defer s.Close()

	// We don't read any input on this path.
	_ = s.CloseRead()

	// The stream value apparently has no way to retrieve a corresponding context,
	// so we use the root context associated with the DataHost
	// and add a 1-second timeout, which ought to suffice for any reasonable client.
	ctx, cancel := context.WithTimeout(h.ctx, time.Second)
	defer cancel()

	heightS, ok := strings.CutPrefix(string(s.Protocol()), fullBlockV1HeightPrefix)
	if !ok {
		_ = json.NewEncoder(s).Encode(JSONResult{
			Err: "invalid protocol",
		})
		return
	}

	height, err := strconv.ParseUint(heightS, 10, 64)
	if err != nil {
		_ = json.NewEncoder(s).Encode(JSONResult{
			Err: "invalid height",
		})
		return
	}

	ch, err := h.chs.LoadCommittedHeader(ctx, height)
	if err != nil {
		// TODO: There are probably certain cases we do want to expose.
		// For now, just mask it.
		var res JSONResult
		if errors.As(err, new(tmconsensus.HeightUnknownError)) {
			// Special string that the client recognizes.
			res.Err = "height unknown"
		} else {
			res.Err = "failed to load header at height"
		}
		_ = json.NewEncoder(s).Encode(res)
		return
	}

	b, err := h.codec.MarshalCommittedHeader(ch)
	if err != nil {
		h.log.Info(
			"Failed to marshal requested committed header",
			"height", height,
			"err", err,
		)
		_ = json.NewEncoder(s).Encode(JSONResult{
			Err: "internal serialization error",
		})
		return
	}

	var data []byte
	if !gsbd.IsZeroTxDataID(string(ch.Header.DataID)) {
		// Only get the block data when the data ID
		// indicates we have non-zero block data.
		// It shouldn't matter whether we load by height or ID,
		// but since the request was by block height
		// we will just use that too.
		// (We could use a sync.Pool to reduce allocations on loaded block data here.)
		id, bd, err := h.bds.LoadBlockDataByHeight(ctx, height, nil)
		if err != nil {
			h.log.Info(
				"Failed to load block data for height that does have a committed header",
				"height", height,
				"err", err,
			)
			_ = json.NewEncoder(s).Encode(JSONResult{
				Err: "failed to load block data at height",
			})
			return
		}

		if id != string(ch.Header.DataID) {
			panic(fmt.Errorf(
				"DATA CORRUPTION: at height %d, header recorded data ID %q but block data store had ID %q",
				height, ch.Header.DataID, id,
			))
		}

		data = bd
	}

	fbr := FullBlockResult{
		Header:    b,
		BlockData: data,
	}
	jfbr, err := json.Marshal(fbr)
	if err != nil {
		h.log.Info(
			"Failed to marshal requested block data",
			"height", height,
			"err", err,
		)
		_ = json.NewEncoder(s).Encode(JSONResult{
			Err: "internal serialization error",
		})
		return
	}

	_ = json.NewEncoder(s).Encode(JSONResult{
		Result: jfbr,
	})
}

// JSONResult is currently used to wrap results from gp2papi calls.
// It is likely to be superseded by something else soon.
type JSONResult struct {
	Result json.RawMessage `json:",omitempty"`
	Err    string          `json:",omitempty"`
}

// FullBlockResult is the JSON representation of a header
// and the associated block data.
//
// This will always be set as the Result field on a [JSONResult].
// Use a [tmjson.MarshalCodec] to decode the Header JSON.
//
// The BlockData field must be parsed according to the rules in the gsbd package
// (which need to be extracted somewhere else).
type FullBlockResult struct {
	Header    json.RawMessage
	BlockData []byte
}
