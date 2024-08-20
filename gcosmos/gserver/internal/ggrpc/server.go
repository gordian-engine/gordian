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

	cfg GRPCServerConfig

	done chan struct{}
}

type GRPCServerConfig struct {
	Listener net.Listener

	FinalizationStore tmstore.FinalizationStore
	MirrorStore       tmstore.MirrorStore

	CryptoRegistry *gcrypto.Registry

	// debug handler
	TxCodec    transaction.Codec[transaction.Tx]
	AppManager appmanager.AppManager[transaction.Tx]
	TxBuf      *gsi.SDKTxBuf
	Codec      codec.Codec
}

func NewGordianGRPCServer(ctx context.Context, log *slog.Logger, cfg GRPCServerConfig) *GordianGRPC {
	srv := &GordianGRPC{
		log:  log,
		cfg:  cfg,
		done: make(chan struct{}),
	}
	go srv.serve()
	go srv.waitForShutdown(ctx)

	return srv
}

func (g *GordianGRPC) Wait() {
	<-g.done
}

func (g *GordianGRPC) waitForShutdown(ctx context.Context) {
	select {
	case <-g.done:
		// g.serve returned on its own, nothing left to do here.
		return
	case <-ctx.Done():
		return
	}
}

func (g *GordianGRPC) serve() {
	defer close(g.done)

	var opts []grpc.ServerOption
	grpcServer := grpc.NewServer(opts...)
	RegisterGordianGRPCServer(grpcServer, g)
	reflection.Register(grpcServer)

	if err := grpcServer.Serve(g.cfg.Listener); err != nil {
		g.log.Error("GRPC server shutting down due to error", "err", err)
	}
}

// GetBlocksWatermark implements GordianGRPCServer.
func (g *GordianGRPC) GetBlocksWatermark(ctx context.Context, req *CurrentBlockRequest) (*CurrentBlockResponse, error) {
	ms := g.cfg.MirrorStore
	vh, vr, ch, cr, err := ms.NetworkHeightRound(ctx)
	if err != nil {
		return nil, err
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
	ms := g.cfg.MirrorStore
	fs := g.cfg.FinalizationStore
	reg := g.cfg.CryptoRegistry
	_, _, committingHeight, _, err := ms.NetworkHeightRound(ctx)
	if err != nil {
		return nil, err
	}

	_, _, vals, _, err := fs.LoadFinalizationByHeight(ctx, committingHeight)
	if err != nil {
		return nil, fmt.Errorf("failed to load finalization by height: %w", err)
	}

	jsonValidators := make([]*Validator, 0, len(vals))
	for i, v := range vals {
		jsonValidators[i] = &Validator{
			PubKey: reg.Marshal(v.PubKey),
			Power:  v.Power,
		}
	}

	return &GetValidatorsResponse{
		Validators: jsonValidators,
	}, nil
}
