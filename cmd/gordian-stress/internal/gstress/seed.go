package gstress

import (
	"encoding/gob"
	"log/slog"
	"net/rpc"
	"sync"

	libp2phost "github.com/libp2p/go-libp2p/core/host"
	libp2pnetwork "github.com/libp2p/go-libp2p/core/network"
	libp2ppeer "github.com/libp2p/go-libp2p/core/peer"
	"github.com/rollchains/gordian/gcrypto"
	"github.com/rollchains/gordian/tm/tmconsensus"
	"github.com/rollchains/gordian/tm/tmengine"
)

// SeedService is the libp2p stream handler for the seed service,
// which coordinates the central seed and any number of validator or mirror instances.
type SeedService struct {
	log *slog.Logger

	h  libp2phost.Host
	bh *BootstrapHost

	mu    sync.Mutex
	stats map[string]tmengine.Metrics
}

// Protocol ID for seed host service.
// Since this service is not intended to service process restarts,
// we don't need a numeric version with it.
const SeedServiceProtocolID = "/gordian-stress/seed"

const seedServiceName = "gordian-stress:seed"

// NewSeedService registers the SeedService on h,
// providing data stored in bh.
//
// There are currently no useful exported methods on SeedService,
// although that may change in the future.
func NewSeedService(log *slog.Logger, h libp2phost.Host, bh *BootstrapHost) *SeedService {
	s := &SeedService{log: log, h: h, bh: bh}
	h.SetStreamHandler(SeedServiceProtocolID, s.handler)
	return s
}

func (s *SeedService) handler(stream libp2pnetwork.Stream) {
	rs := rpc.NewServer()
	if err := rs.Register(&SeedRPC{
		bh:  s.bh,
		s:   s,
		pID: stream.Conn().RemotePeer(),
	}); err != nil {
		s.log.Info("Failed to register SeedRPC", "err", err)
		_ = stream.Reset()
		return
	}

	if err := stream.Scope().SetService(seedServiceName); err != nil {
		s.log.Info("Failed to set service name", "err", err)
		_ = stream.Reset()
		return
	}

	// Maybe add the stream reserve memory call here?
	// Although for the short life of the seed service,
	// that may not be necessary.

	// Block in the handler serving all RPC requests to the stream.
	rs.ServeConn(stream)
	_ = stream.Reset()
}

// CopyMetrics copies s's metrics into dst.
// It is the caller's responsibility to clear dst before calling CopyMetrics.
func (s *SeedService) CopyMetrics(dst map[string]tmengine.Metrics) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for k, v := range s.stats {
		dst[k] = v
	}
}

type RPCGenesisRequest struct{}

type RPCGenesisResponse struct {
	App        string
	ChainID    string
	Validators []tmconsensus.Validator

	// TODO: initial app state eventually.
}

func init() {
	// net/rpc defaults to using gob under the hood.
	// The validators have a gcrypto.PubKey value, not a concrete Ed25519PubKey.
	// To avoid a runtiem panic, we need to register the ed25519 key with gob.
	// This approach may not scale beyond one key type.
	// If we run into that issue, it will probably be worth figuring out how to integrate
	// a gcrypto.Registry to handle marshalling;
	// and perhaps at that point we move beyond net/rpc.
	var k gcrypto.PubKey = gcrypto.Ed25519PubKey{}
	gob.Register(k)
}

type SeedRPC struct {
	bh *BootstrapHost
	s  *SeedService

	pID libp2ppeer.ID
}

// Genesis is the RPC method for getting genesis data from the seed service.
// This method blocks until the bootstrap host has received a Start call,
// in order to prevent incomplete genesis data being sent.
func (rpc *SeedRPC) Genesis(args RPCGenesisRequest, resp *RPCGenesisResponse) error {
	<-rpc.bh.s.started

	*resp = RPCGenesisResponse{
		App:        rpc.bh.s.App(),
		ChainID:    rpc.bh.s.ChainID(),
		Validators: rpc.bh.s.Validators(),
	}
	return nil
}

type RPCHaltRequest struct{}
type RPCHaltResponse struct{}

// AwaitHalt is the RPC method for a client to wait for a halt instruction from the host.
func (rpc *SeedRPC) AwaitHalt(args RPCHaltRequest, resp *RPCHaltResponse) error {
	<-rpc.bh.haltCh
	return nil
}

type RPCPublishMetricsRequest struct {
	Metrics tmengine.Metrics
}
type RPCPublishMetricsResponse struct{}

func (rpc *SeedRPC) PublishMetrics(args RPCPublishMetricsRequest, resp *RPCPublishMetricsResponse) error {
	rpc.s.mu.Lock()
	defer rpc.s.mu.Unlock()
	if rpc.s.stats == nil {
		rpc.s.stats = make(map[string]tmengine.Metrics)
	}
	rpc.s.stats[rpc.pID.String()] = args.Metrics
	return nil
}
