package ggrpc

import (
	"context"
	"flag"
	"fmt"
	"net"

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

var _ GordianGRPCServer = (*GordianGRPC)(nil)

type GordianGRPC struct {
	UnimplementedGordianGRPCServer

	cfg GRPCServerConfig

	done chan struct{}
}

type GRPCServerConfig struct {
	Listener net.Listener

	FinalizationStore tmstore.FinalizationStore
	MirrorStore       tmstore.MirrorStore

	CryptoRegistry *gcrypto.Registry
}

// func NewGordianGRPCServer(ctx context.Context, bs tmstore.BlockStore, ms tmstore.MirrorStore) *GordianGRPC {
func NewGordianGRPCServer(ctx context.Context, cfg GRPCServerConfig) *GordianGRPC {
	srv := &GordianGRPC{
		cfg: cfg,

		done: make(chan struct{}),
	}
	go srv.Start()
	go srv.waitForShutdown(ctx)

	return srv
}

func (g *GordianGRPC) Wait() {
	<-g.done
}

func (g *GordianGRPC) waitForShutdown(ctx context.Context) {
	select {
	case <-g.done:
		// h.serve returned on its own, nothing left to do here.
		return
	case <-ctx.Done():
		// TODO: hard close grpc server?
		close(g.done)
	}
}

func (g *GordianGRPC) Start() {
	flag.Parse()
	var opts []grpc.ServerOption
	// TODO: TLS
	grpcServer := grpc.NewServer(opts...)
	RegisterGordianGRPCServer(grpcServer, g)
	reflection.Register(grpcServer)

	grpcServer.Serve(g.cfg.Listener)
}

// /blocks/watermark
// GetBlocksWatermark implements GordianGRPCServer.
func (g *GordianGRPC) GetBlocksWatermark(ctx context.Context, req *CurrentBlockRequest) (*CurrentBlockResponse, error) {
	ms := g.cfg.MirrorStore
	vh, vr, ch, cr, err := ms.NetworkHeightRound(ctx)
	if err != nil {
		return nil, err
	}

	fmt.Printf("GetBlocksWatermark: %d %d %d %d\n", vh, vr, ch, cr)

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
