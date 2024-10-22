package tmmemstore

import (
	"context"
	"sync"

	"github.com/gordian-engine/gordian/tm/tmstore"
)

// MirrorStore is an in-memory implementation of [tmstore.MirrorStore].
type MirrorStore struct {
	mu sync.RWMutex

	votingHeight uint64
	votingRound  uint32

	committingHeight uint64
	committingRound  uint32
}

func NewMirrorStore() *MirrorStore {
	return new(MirrorStore)
}

func (s *MirrorStore) SetNetworkHeightRound(
	_ context.Context,
	votingHeight uint64, votingRound uint32,
	committingHeight uint64, committingRound uint32,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// TODO: should this validate that we only move forward?
	// And should it validate that committingHeight == votingHeight-1?

	s.votingHeight = votingHeight
	s.votingRound = votingRound
	s.committingHeight = committingHeight
	s.committingRound = committingRound

	return nil
}

func (s *MirrorStore) NetworkHeightRound(_ context.Context) (
	votingHeight uint64, votingRound uint32,
	committingHeight uint64, committingRound uint32,
	err error,
) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.votingHeight == 0 {
		return 0, 0, 0, 0, tmstore.ErrStoreUninitialized
	}

	return s.votingHeight, s.votingRound,
		s.committingHeight, s.committingRound, nil
}
