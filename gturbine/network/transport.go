package network

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/gordian-engine/gordian/gturbine"
	"github.com/gordian-engine/gordian/gturbine/builder"
	"github.com/gordian-engine/gordian/gturbine/shredding"
	"github.com/gordian-engine/gordian/tm/tmconsensus"
)

const (
	maxShredSize  = 64 * 1024 // 64KB max shred size
	retryAttempts = 3
	retryDelay    = time.Second * 2
)

type Transport struct {
	mu          sync.RWMutex
	conn        *net.UDPConn
	tree        *gturbine.Tree
	handler     tmconsensus.ConsensusHandler
	processor   *shredding.Processor
	pubKey      []byte
	shredGroups map[string]*shredding.ShredGroup // groupID -> ShredGroup
	ctx         context.Context
	cancel      context.CancelFunc
	done        chan struct{}
}

type Config struct {
	ListenAddr   string
	ChunkSize    uint32
	DataShards   int
	ParityShards int
	ValidatorKey []byte
}

func NewTransport(cfg Config) (*Transport, error) {
	addr, err := net.ResolveUDPAddr("udp", cfg.ListenAddr)
	if err != nil {
		return nil, fmt.Errorf("invalid address: %w", err)
	}

	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return nil, fmt.Errorf("failed to listen: %w", err)
	}

	processor, err := shredding.NewProcessor(cfg.ChunkSize, cfg.DataShards, cfg.ParityShards)
	if err != nil {
		return nil, fmt.Errorf("failed to create processor: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t := &Transport{
		conn:        conn,
		processor:   processor,
		pubKey:      cfg.ValidatorKey,
		shredGroups: make(map[string]*shredding.ShredGroup),
		ctx:         ctx,
		cancel:      cancel,
		done:        make(chan struct{}),
	}

	go t.readLoop()
	return t, nil
}

func (t *Transport) BroadcastBlock(block []byte, height uint64, slot uint64, shredIdx uint32) error {
	group, err := t.processor.ProcessBlock(block, height)
	if err != nil {
		return fmt.Errorf("failed to process block: %w", err)
	}

	t.mu.Lock()
	children := builder.GetChildren(t.tree, t.pubKey)
	t.mu.Unlock()

	// Send shreds to children with retries
	for _, shred := range group.DataShreds {
		if err := t.sendShredWithRetry(shred, children); err != nil {
			return fmt.Errorf("failed to send data shred: %w", err)
		}
	}

	for _, shred := range group.RecoveryShreds {
		if err := t.sendShredWithRetry(shred, children); err != nil {
			return fmt.Errorf("failed to send recovery shred: %w", err)
		}
	}

	return nil
}

func (t *Transport) sendShredWithRetry(shred *gturbine.Shred, targets []gturbine.Validator) error {
	data, err := shredding.SerializeShred(shred, shredding.ShredTypeData, nil)
	if err != nil {
		return err
	}

	for _, target := range targets {
		addr, err := net.ResolveUDPAddr("udp", target.NetAddr)
		if err != nil {
			continue
		}

		for i := 0; i < retryAttempts; i++ {
			_, err = t.conn.WriteToUDP(data, addr)
			if err == nil {
				break
			}
			time.Sleep(retryDelay)
		}
	}
	return nil
}

func (t *Transport) readLoop() {
	defer close(t.done)
	buf := make([]byte, maxShredSize)

	for {
		select {
		case <-t.ctx.Done():
			return
		default:
			n, _, err := t.conn.ReadFromUDP(buf)
			if err != nil {
				if t.ctx.Err() != nil {
					return
				}
				continue
			}

			shred, shredType, groupID, err := shredding.DeserializeShred(buf[:n])
			if err != nil {
				continue
			}

			t.processReceivedShred(shred, shredType, groupID)
		}
	}
}

func (t *Transport) processReceivedShred(shred *gturbine.Shred, shredType shredding.ShredType, groupID []byte) {
	t.mu.Lock()
	defer t.mu.Unlock()

	groupKey := string(groupID)
	group, exists := t.shredGroups[groupKey]
	if !exists {
		group = &shredding.ShredGroup{
			DataShreds: make([]*gturbine.Shred, shred.Total),
			GroupID:    groupID,
		}
		t.shredGroups[groupKey] = group
	}

	if shredType == shredding.ShredTypeData {
		group.DataShreds[shred.Index] = shred
	} else {
		group.RecoveryShreds = append(group.RecoveryShreds, shred)
	}

	// Check if we have all shreds
	complete := true
	for _, s := range group.DataShreds {
		if s == nil {
			complete = false
			break
		}
	}

	if complete {
		go t.handleCompleteGroup(groupKey, group)
	}
}

func (t *Transport) handleCompleteGroup(groupKey string, group *shredding.ShredGroup) {
	_, err := t.processor.ReassembleBlock(group)
	if err != nil {
		return
	}

	t.mu.Lock()
	delete(t.shredGroups, groupKey)
	handler := t.handler
	t.mu.Unlock()

	if handler != nil {
		// Convert block to ProposedHeader and pass to consensus
		// Implementation depends on block format
	}
}

// Implement tmp2p.Connection interface methods
func (t *Transport) SetConsensusHandler(_ context.Context, h tmconsensus.ConsensusHandler) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.handler = h
}

func (t *Transport) Disconnect() {
	t.cancel()
	t.conn.Close()
}

func (t *Transport) Disconnected() <-chan struct{} {
	return t.done
}

func (t *Transport) SetTree(tree *gturbine.Tree) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.tree = tree
}
