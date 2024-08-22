package ggrpc

import (
	"context"
	"fmt"
	"log/slog"
	"net"

	"cosmossdk.io/core/transaction"
	"cosmossdk.io/server/v2/appmanager"
	"github.com/cosmos/cosmos-sdk/codec"
	"github.com/rollchains/gordian/gcosmos/gserver/internal/gsi"
	"github.com/rollchains/gordian/gcrypto"
	"github.com/rollchains/gordian/tm/tmstore"
	grpc "google.golang.org/grpc"
	"google.golang.org/grpc/reflection"
)

var _ GordianGRPCServer = (*GordianGRPC)(nil)

type GordianGRPC struct {
	UnimplementedGordianGRPCServer
	log *slog.Logger

	fs tmstore.FinalizationStore
	ms tmstore.MirrorStore

	reg *gcrypto.Registry

	// debug handler
	txc   transaction.Codec[transaction.Tx]
	am    appmanager.AppManager[transaction.Tx]
	txBuf *gsi.SDKTxBuf
	cdc   codec.Codec

	done chan struct{}
}

type GRPCServerConfig struct {
	Listener net.Listener

	FinalizationStore tmstore.FinalizationStore
	MirrorStore       tmstore.MirrorStore

	CryptoRegistry *gcrypto.Registry

	TxCodec    transaction.Codec[transaction.Tx]
	AppManager appmanager.AppManager[transaction.Tx]
	Codec      codec.Codec

	TxBuffer *gsi.SDKTxBuf
}

func NewGordianGRPCServer(ctx context.Context, log *slog.Logger, cfg GRPCServerConfig) *GordianGRPC {
	if cfg.Listener == nil {
		panic("BUG: listener for the grpc server is nil")
	}

	srv := &GordianGRPC{
		log: log,

		fs:    cfg.FinalizationStore,
		ms:    cfg.MirrorStore,
		reg:   cfg.CryptoRegistry,
		txc:   cfg.TxCodec,
		am:    cfg.AppManager,
		txBuf: cfg.TxBuffer,
		cdc:   cfg.Codec,

		done: make(chan struct{}),
	}

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
		VotingHeight:     vh,
		VotingRound:      vr,
		CommittingHeight: ch,
		CommittingRound:  cr,
	}, nil
}

// GetValidators implements GordianGRPCServer.
func (g *GordianGRPC) GetValidators(ctx context.Context, req *GetValidatorsRequest) (*GetValidatorsResponse, error) {
	_, _, committingHeight, _, err := g.ms.NetworkHeightRound(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get network height and round: %w", err)
	}

	_, _, vals, _, err := g.fs.LoadFinalizationByHeight(ctx, committingHeight)
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
		Validators: jsonValidators,
	}, nil
}
