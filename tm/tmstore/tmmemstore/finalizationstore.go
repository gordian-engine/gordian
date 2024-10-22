package tmmemstore

import (
	"context"
	"sync"

	"github.com/gordian-engine/gordian/tm/tmconsensus"
	"github.com/gordian-engine/gordian/tm/tmstore"
)

type FinalizationStore struct {
	mu sync.RWMutex

	byHeight map[uint64]fin
}

type fin struct {
	H            uint64
	R            uint32
	BlockHash    string
	ValSet       tmconsensus.ValidatorSet
	AppStateHash string
}

func NewFinalizationStore() *FinalizationStore {
	return &FinalizationStore{
		byHeight: make(map[uint64]fin),
	}
}

func (s *FinalizationStore) SaveFinalization(
	ctx context.Context,
	height uint64, round uint32,
	blockHash string,
	valSet tmconsensus.ValidatorSet,
	appStateHash string,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.byHeight[height]; ok {
		return tmstore.FinalizationOverwriteError{Height: height}
	}

	s.byHeight[height] = fin{
		H: height, R: round,
		BlockHash:    blockHash,
		ValSet:       valSet,
		AppStateHash: appStateHash,
	}

	return nil
}

func (s *FinalizationStore) LoadFinalizationByHeight(ctx context.Context, height uint64) (
	round uint32,
	blockHash string,
	valSet tmconsensus.ValidatorSet,
	appStateHash string,
	err error,
) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	fin, ok := s.byHeight[height]
	if !ok {
		return 0, "", tmconsensus.ValidatorSet{}, "", tmconsensus.HeightUnknownError{Want: height}
	}

	return fin.R, fin.BlockHash, fin.ValSet, fin.AppStateHash, nil
}
