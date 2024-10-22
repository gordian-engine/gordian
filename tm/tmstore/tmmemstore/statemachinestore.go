package tmmemstore

import (
	"context"
	"sync"

	"github.com/gordian-engine/gordian/tm/tmstore"
)

type StateMachineStore struct {
	mu sync.Mutex
	h  uint64
	r  uint32
}

func NewStateMachineStore() *StateMachineStore {
	return new(StateMachineStore)
}

func (s *StateMachineStore) SetStateMachineHeightRound(
	_ context.Context,
	height uint64, round uint32,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.h = height
	s.r = round
	return nil
}

func (s *StateMachineStore) StateMachineHeightRound(_ context.Context) (
	height uint64, round uint32,
	err error,
) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.h == 0 {
		return 0, 0, tmstore.ErrStoreUninitialized
	}

	return s.h, s.r, nil
}
