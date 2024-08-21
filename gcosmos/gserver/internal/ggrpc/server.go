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

func NewGordianGRPCServer(ctx context.Context, log *slog.Logger,
	ln net.Listener,

	fs tmstore.FinalizationStore,
	ms tmstore.MirrorStore,
	reg *gcrypto.Registry,

	txc transaction.Codec[transaction.Tx],
	am appmanager.AppManager[transaction.Tx],
	txb *gsi.SDKTxBuf,
	cdc codec.Codec,
) *GordianGRPC {
	if ln == nil {
		panic("BUG: listener for the grpc server is nil")
	}

	srv := &GordianGRPC{
		log: log,

		fs:    fs,
		ms:    ms,
		reg:   reg,
		txc:   txc,
		am:    am,
		txBuf: txb,
		cdc:   cdc,

		done: make(chan struct{}),
	}

	var opts []grpc.ServerOption
	// TODO: configure grpc options (like TLS)
	gc := grpc.NewServer(opts...)

	go srv.serve(ln, gc)
	go srv.waitForShutdown(ctx, gc)

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
	if gs == nil {
		panic("BUG: grpc server is nil")
	}
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
	ms := g.ms
	vh, vr, ch, cr, err := ms.NetworkHeightRound(ctx)
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
	ms := g.ms
	fs := g.fs
	reg := g.reg
	_, _, committingHeight, _, err := ms.NetworkHeightRound(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get network height and round: %w", err)
	}

	_, _, vals, _, err := fs.LoadFinalizationByHeight(ctx, committingHeight)
	if err != nil {
		return nil, fmt.Errorf("failed to load finalization by height: %w", err)
	}

	jsonValidators := make([]*Validator, len(vals))
	for i, v := range vals {
		jsonValidators[i] = &Validator{
			EncodedPubKey: reg.Marshal(v.PubKey),
			Power:         v.Power,
		}
	}

	return &GetValidatorsResponse{
		Validators: jsonValidators,
	}, nil
}
