package ggrpc

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"

	"github.com/rollchains/gordian/tm/tmstore"
	grpc "google.golang.org/grpc"
	"google.golang.org/grpc/reflection"
)

var (
	// 	tls        = flag.Bool("tls", false, "Connection uses TLS if true, else plain TCP")
	// 	certFile   = flag.String("cert_file", "", "The TLS cert file")
	// 	keyFile    = flag.String("key_file", "", "The TLS key file")
	// 	jsonDBFile = flag.String("json_db_file", "", "A json file containing a list of features")
	port = flag.Int("port", 9092, "The server gRPC port")
)

var _ GordianGRPCServer = (*GordianGRPC)(nil)

type GordianGRPC struct {
	UnimplementedGordianGRPCServer

	// BlockStore  tmstore.BlockStore
	MirrorStore tmstore.MirrorStore

	done chan struct{}
}

func NewGordianGRPCServer(ctx context.Context, bs tmstore.BlockStore, ms tmstore.MirrorStore) *GordianGRPC {
	srv := &GordianGRPC{
		// BlockStore:  bs,GordianGRPC
		MirrorStore: ms,

		done: make(chan struct{}),
	}
	go srv.Start()
	go srv.waitForShutdown(ctx)

	return srv
}

func (h *GordianGRPC) Wait() {
	<-h.done
}

func (h *GordianGRPC) waitForShutdown(ctx context.Context) {
	select {
	case <-h.done:
		// h.serve returned on its own, nothing left to do here.
		return
	case <-ctx.Done():
		// TODO: hard close grpc server?
	}
}

func (g *GordianGRPC) Start() {
	flag.Parse()
	lis, err := net.Listen("tcp", fmt.Sprintf("localhost:%d", *port))
	if err != nil {
		log.Fatalf("failed to listen: %v", err)
	}
	var opts []grpc.ServerOption
	// TODO: TLS
	grpcServer := grpc.NewServer(opts...)
	RegisterGordianGRPCServer(grpcServer, g)
	reflection.Register(grpcServer)

	grpcServer.Serve(lis)
}

// /blocks/watermark
// GetBlocksWatermark implements GordianGRPCServer.
func (g *GordianGRPC) GetBlocksWatermark(ctx context.Context, req *CurrentBlockRequest) (*CurrentBlockResponse, error) {
	vh, vr, ch, cr, err := g.MirrorStore.NetworkHeightRound(ctx)
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
