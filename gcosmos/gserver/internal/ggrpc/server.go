package ggrpc

import (
	"context"
	"flag"
	"log/slog"
	"net"

	"cosmossdk.io/core/transaction"
	"cosmossdk.io/server/v2/appmanager"
	"github.com/cosmos/cosmos-sdk/codec"
	"github.com/rollchains/gordian/gcosmos/gserver/internal/txmanager"
	"github.com/rollchains/gordian/gcrypto"
	"github.com/rollchains/gordian/tm/tmstore"
	grpc "google.golang.org/grpc"
	"google.golang.org/grpc/reflection"
)

var (
// tls        = flag.Bool("tls", false, "Connection uses TLS if true, else plain TCP")
// certFile   = flag.String("cert_file", "", "The TLS cert file")
// keyFile    = flag.String("key_file", "", "The TLS key file")
)

var _ GordianGRPCServiceServer = (*GordianGRPCServer)(nil)

type GordianGRPCServer struct {
	UnimplementedGordianGRPCServiceServer

	cfg GRPCServerConfig
	log *slog.Logger

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
	TxBuf      *txmanager.SDKTxBuf
	Codec      codec.Codec
}

func NewGordianGRPCServer(ctx context.Context, log *slog.Logger, cfg GRPCServerConfig) *GordianGRPCServer {
	srv := &GordianGRPCServer{
		cfg:  cfg,
		log:  log,
		done: make(chan struct{}),
	}
	go srv.Start()
	go srv.waitForShutdown(ctx)

	return srv
}

func (g *GordianGRPCServer) Wait() {
	<-g.done
}

func (g *GordianGRPCServer) waitForShutdown(ctx context.Context) {
	select {
	case <-g.done:
		// g.serve returned on its own, nothing left to do here.
		return
	case <-ctx.Done():
		close(g.done)
	}
}

func (g *GordianGRPCServer) Start() {
	flag.Parse()
	var opts []grpc.ServerOption
	// TODO: TLS
	grpcServer := grpc.NewServer(opts...)
	RegisterGordianGRPCServiceServer(grpcServer, g)
	reflection.Register(grpcServer)

	grpcServer.Serve(g.cfg.Listener)
}

// GetBlocksWatermark implements GordianGRPCServer.
func (g *GordianGRPCServer) GetBlocksWatermark(ctx context.Context, req *GetBlocksWatermarkRequest) (*GetBlocksWatermarkResponse, error) {
	ms := g.cfg.MirrorStore
	vh, vr, ch, cr, err := ms.NetworkHeightRound(ctx)
	if err != nil {
		return nil, err
	}

	return &GetBlocksWatermarkResponse{
		VotingHeight:     vh,
		VotingRound:      vr,
		CommittingHeight: ch,
		CommittingRound:  cr,
	}, nil
}

// GetValidators implements GordianGRPCServer.
func (g *GordianGRPCServer) GetValidators(ctx context.Context, req *GetValidatorsRequest) (*GetValidatorsResponse, error) {
	ms := g.cfg.MirrorStore
	fs := g.cfg.FinalizationStore
	reg := g.cfg.CryptoRegistry
	_, _, committingHeight, _, err := ms.NetworkHeightRound(ctx)
	if err != nil {
		return nil, err
	}

	_, _, vals, _, err := fs.LoadFinalizationByHeight(ctx, committingHeight)
	if err != nil {
		return nil, err
	}

	jsonValidators := make([]*Validator, 0, len(vals))
	for _, v := range vals {
		jsonValidators = append(jsonValidators, &Validator{
			PubKey: reg.Marshal(v.PubKey),
			Power:  v.Power,
		})
	}

	return &GetValidatorsResponse{
		Validators: jsonValidators,
	}, nil
}
