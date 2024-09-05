package ggrpc

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	sync "sync"

	"cosmossdk.io/core/transaction"
	"cosmossdk.io/server/v2/appmanager"
	coretypes "github.com/cometbft/cometbft/rpc/core/types"
	"github.com/cosmos/cosmos-sdk/codec"
	"github.com/rollchains/gordian/gcosmos/gserver/internal/txmanager"
	"github.com/rollchains/gordian/gcrypto"
	"github.com/rollchains/gordian/tm/tmstore"
	grpc "google.golang.org/grpc"
	"google.golang.org/grpc/reflection"
)

var _ GordianGRPCServer = (*GordianGRPC)(nil)

// TODO: remove me
// mustEmbedUnimplementedGordianGRPCServer implements GordianGRPCServer.
func (g *GordianGRPC) mustEmbedUnimplementedGordianGRPCServer() {
	panic("unimplemented")
}

type GordianGRPC struct {
	// UnimplementedGordianGRPCServer
	log *slog.Logger

	fs tmstore.FinalizationStore
	ms tmstore.MirrorStore
	bs tmstore.BlockStore

	reg *gcrypto.Registry

	// debug handler
	txc   transaction.Codec[transaction.Tx]
	am    appmanager.AppManager[transaction.Tx]
	txBuf *txmanager.SDKTxBuf
	cdc   codec.Codec

	txIdx     map[string]*TxResultResponse
	txIdxLock sync.Mutex

	done chan struct{}
}

type GRPCServerConfig struct {
	Listener net.Listener

	FinalizationStore tmstore.FinalizationStore
	MirrorStore       tmstore.MirrorStore
	BlockStore        tmstore.BlockStore

	CryptoRegistry *gcrypto.Registry

	TxCodec    transaction.Codec[transaction.Tx]
	AppManager appmanager.AppManager[transaction.Tx]
	Codec      codec.Codec

	TxBuffer *txmanager.SDKTxBuf
}

func NewGordianGRPCServer(ctx context.Context, log *slog.Logger, cfg GRPCServerConfig) *GordianGRPC {
	if cfg.Listener == nil {
		panic("BUG: listener for the grpc server is nil")
	}

	srv := &GordianGRPC{
		log: log,

		fs: cfg.FinalizationStore,
		ms: cfg.MirrorStore,
		bs: cfg.BlockStore,

		reg:   cfg.CryptoRegistry,
		txc:   cfg.TxCodec,
		am:    cfg.AppManager,
		txBuf: cfg.TxBuffer,
		cdc:   cfg.Codec,

		done: make(chan struct{}),
	}
	srv.txIdx = make(map[string]*TxResultResponse)

	var opts []grpc.ServerOption
	// TODO: configure grpc options (like TLS)
	gs := grpc.NewServer(opts...)

	go srv.serve(cfg.Listener, gs)
	go srv.waitForShutdown(ctx, gs)

	return srv
}

func (g *GordianGRPC) Wait() {
	<-g.done
}

func (g *GordianGRPC) waitForShutdown(ctx context.Context, gs *grpc.Server) {
	select {
	case <-g.done:
		// g.serve returned on its own, nothing left to do here.
		return
	case <-ctx.Done():
		if gs != nil {
			gs.Stop()
		}
	}
}

func (g *GordianGRPC) serve(ln net.Listener, gs *grpc.Server) {
	defer close(g.done)

	RegisterGordianGRPCServer(gs, g)
	reflection.Register(gs)

	if err := gs.Serve(ln); err != nil {
		if err != grpc.ErrServerStopped {
			g.log.Error("GRPC server stopped with error", "err", err)
		}
	}
}

// GetBlocksWatermark implements GordianGRPCServer.
func (g *GordianGRPC) GetBlocksWatermark(ctx context.Context, req *CurrentBlockRequest) (*CurrentBlockResponse, error) {
	vh, vr, ch, cr, err := g.ms.NetworkHeightRound(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get network height and round: %w", err)
	}

	return &CurrentBlockResponse{
		VotingHeight:     Pointy(vh),
		VotingRound:      Pointy(vr),
		CommittingHeight: Pointy(ch),
		CommittingRound:  Pointy(cr),
	}, nil
}

// GetValidators implements GordianGRPCServer.
func (g *GordianGRPC) GetValidators(ctx context.Context, req *GetValidatorsRequest) (*GetValidatorsResponse, error) {
	height := req.Height
	if height == 0 {
		_, _, committingHeight, _, err := g.ms.NetworkHeightRound(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to get network height and round: %w", err)
		}
		height = committingHeight
	}

	_, _, vals, _, err := g.fs.LoadFinalizationByHeight(ctx, height)
	if err != nil {
		return nil, fmt.Errorf("failed to load finalization by height: %w", err)
	}

	jsonValidators := make([]*Validator, len(vals))
	for i, v := range vals {
		jsonValidators[i] = &Validator{
			EncodedPubKey: g.reg.Marshal(v.PubKey),
			Power:         v.Power,
		}
	}

	return &GetValidatorsResponse{
		FinalizationHeight: Pointy(height),
		Validators:         jsonValidators,
	}, nil
}

// GetBlock implements GordianGRPCServer.
// TODO: rename to GetHeader (return more info in the future, for now just time is required)
func (g *GordianGRPC) GetBlock(ctx context.Context, req *GetBlockRequest) (*GetBlockResponse, error) {
	b, err := g.bs.LoadBlock(ctx, req.Height)
	if err != nil {
		return nil, fmt.Errorf("failed to load block: %w", err)
	}

	a, err := txmanager.BlockAnnotationFromBytes(b.Block.Annotations.Driver)
	if err != nil {
		return nil, fmt.Errorf("failed to parse block annotation: %w", err)
	}

	// TODO: height 1 always returns a very wrong time at genesis. (1980s)
	blockTime, err := a.BlockTimeAsTime()
	if err != nil {
		return nil, fmt.Errorf("failed to parse block time: %w", err)
	}

	return &GetBlockResponse{
		Time: uint64(blockTime.Unix()),
	}, nil
}

// GetStatus implements GordianGRPCServer.
func (g *GordianGRPC) GetStatus(ctx context.Context, req *GetStatusRequest) (*GetStatusResponse, error) {
	_, _, ch, _, err := g.ms.NetworkHeightRound(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get network height and round: %w", err)
	}

	b, err := g.bs.LoadBlock(context.Background(), ch-1)
	if err != nil {
		return nil, fmt.Errorf("failed to load block: %w", err)
	}

	// TODO: check if we are catching up
	return &GetStatusResponse{
		CatchingUp:        false,
		LatestBlockHeight: b.Block.Height,
	}, nil
}

// GetBlockResults implements GordianGRPCServer.
func (g *GordianGRPC) GetBlockResults(ctx context.Context, req *GetBlockResultsRequest) (*BlockResults, error) {
	// TODO: how to read this from the appmanager / store?
	// Or do we need to index within gordian?
	return &BlockResults{
		Height:              req.Height,
		TxsResults:          []*TxResultResponse{},
		FinalizeBlockEvents: []*Event{},
		AppHash:             "",
	}, fmt.Errorf("not implemented")
}

// GetABCIQuery implements GordianGRPCServer.
func (g *GordianGRPC) GetABCIQuery(context.Context, *GetABCIQueryRequest) (*GetABCIQueryResponse, error) {
	panic("unimplemented")
}

// GetTxSearch implements GordianGRPCServer.
func (g *GordianGRPC) GetTxSearch(context.Context, *GetTxSearchRequest) (*TxResultResponseList, error) {
	// TODO: this is slow anyways in the relayer, for now just returning nil
	// panic("unimplemented")
	return nil, nil
}

// DoBroadcastTxAsync implements GordianGRPCServer.
func (g *GordianGRPC) DoBroadcastTxAsync(context.Context, *DoBroadcastTxAsyncRequest) (*TxResultResponse, error) {
	panic("unimplemented")
}

// SubmitTransactionSync implements GordianGRPCServer.
func (g *GordianGRPC) SubmitTransactionSync(ctx context.Context, req *DoBroadcastTxSyncRequest) (*TxResultResponse, error) {
	tx, err := g.txc.DecodeJSON(req.Tx)
	if err != nil {
		return nil, fmt.Errorf("failed to decode transaction: %w", err)
	}

	// TODO: add configurable timeout

	res, err := g.am.ValidateTx(ctx, tx)
	if err != nil {
		// ValidateTx should only return an error at this level,
		// if it failed to get state from the store.
		return &TxResultResponse{
			Error: "failed to validate transaction" + err.Error(),
		}, fmt.Errorf("error attempting to validate transaction: %w", err)
	}

	if res.Error != nil {
		// This is fine from the server's perspective, no need to log.
		return &TxResultResponse{
			Code: res.Code,
			Log:  res.Error.Error(),
		}, nil
	}

	// TODO: this gives us Tx events
	res, _, simErr := g.am.Simulate(ctx, tx)
	if simErr != nil {
		return nil, fmt.Errorf("failed to simulate transaction: %w", err)
	}

	// If it passed basic validation, then we can attempt to add it to the buffer.
	if err := g.txBuf.AddTx(ctx, tx); err != nil {
		// We could potentially check if it is a TxInvalidError here
		// and adjust the status code,
		// but since this is a debug endpoint, we'll ignore the type.
		return &TxResultResponse{
			Code: res.Code,
			Log:  err.Error(),
		}, nil
	}

	h := tx.Hash()
	hStr := fmt.Sprintf("%X", h)

	txResp := &TxResultResponse{
		Code:      res.Code,
		Data:      res.Data,
		Log:       res.Log,
		Codespace: res.Codespace,
		TxHash:    hStr,
		Events:    convertEvent(res.Events),
		Info:      res.Info,
		GasWanted: res.GasWanted,
		GasUsed:   res.GasUsed,
	}
	if res.Error != nil {
		txResp.Error = res.Error.Error()
	}

	g.txIdxLock.Lock()
	g.txIdx[hStr] = txResp
	g.txIdxLock.Unlock()

	return txResp, nil
}

// GetABCIQueryWithOptions implements GordianGRPCServer.
func (g *GordianGRPC) GetABCIQueryWithOptions(context.Context, *GetABCIQueryWithOptsRequest) (*coretypes.ResultABCIQuery, error) {
	panic("unimplemented")
}

// GetBlockSearch implements GordianGRPCServer.
func (g *GordianGRPC) GetBlockSearch(context.Context, *GetBlockSearchRequest) (*coretypes.ResultBlockSearch, error) {
	panic("unimplemented")
}

// GetCommit implements GordianGRPCServer.
func (g *GordianGRPC) GetCommit(context.Context, *GetCommitRequest) (*coretypes.ResultCommit, error) {
	panic("unimplemented")
}
