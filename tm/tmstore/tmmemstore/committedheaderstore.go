package tmmemstore

import (
	"context"
	"sync"

	"github.com/gordian-engine/gordian/tm/tmconsensus"
)

type CommittedHeaderStore struct {
	mu sync.RWMutex

	chs map[uint64]tmconsensus.CommittedHeader
}

func NewCommittedHeaderStore() *CommittedHeaderStore {
	return &CommittedHeaderStore{
		chs: make(map[uint64]tmconsensus.CommittedHeader),
	}
}

func (s *CommittedHeaderStore) SaveCommittedHeader(_ context.Context, ch tmconsensus.CommittedHeader) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.chs[ch.Header.Height] = ch

	return nil
}

func (s *CommittedHeaderStore) LoadCommittedHeader(_ context.Context, height uint64) (tmconsensus.CommittedHeader, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	ch, ok := s.chs[height]
	if !ok {
		return tmconsensus.CommittedHeader{}, tmconsensus.HeightUnknownError{Want: height}
	}

	return ch, nil
}
