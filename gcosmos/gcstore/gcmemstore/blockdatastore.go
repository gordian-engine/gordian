package gcmemstore

import (
	"bytes"
	"context"
	"fmt"
	"sync"

	"github.com/gordian-engine/gordian/gcosmos/gcstore"
)

type BlockDataStore struct {
	mu sync.Mutex

	dataByHeightID map[heightIDKey][]byte

	heightByID map[string]uint64
	idByHeight map[uint64]string
}

func NewBlockDataStore() *BlockDataStore {
	return &BlockDataStore{
		dataByHeightID: make(map[heightIDKey][]byte),
		heightByID:     make(map[string]uint64),
		idByHeight:     make(map[uint64]string),
	}
}

type heightIDKey struct {
	h  uint64
	id string
}

func (s *BlockDataStore) SaveBlockData(
	ctx context.Context,
	height uint64,
	dataID string,
	data []byte,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.idByHeight[height]; ok {
		return gcstore.AlreadyHaveBlockDataForHeightError{Height: height}
	}

	if _, ok := s.heightByID[dataID]; ok {
		return gcstore.AlreadyHaveBlockDataForIDError{ID: dataID}
	}

	s.heightByID[dataID] = height
	s.idByHeight[height] = dataID

	k := heightIDKey{h: height, id: dataID}
	s.dataByHeightID[k] = bytes.Clone(data)
	return nil
}

func (s *BlockDataStore) LoadBlockDataByHeight(
	ctx context.Context,
	height uint64,
	dst []byte,
) (
	dataID string, data []byte, err error,
) {
	s.mu.Lock()
	defer s.mu.Unlock()

	id, ok := s.idByHeight[height]
	if !ok {
		return "", nil, gcstore.ErrBlockDataNotFound
	}

	k := heightIDKey{h: height, id: id}
	underlying, ok := s.dataByHeightID[k]
	if !ok {
		panic(fmt.Errorf(
			"BUG: found id %q for height %d but did not find data for key",
			id, height,
		))
	}

	data = append(dst, underlying...)
	return id, data, nil
}

func (s *BlockDataStore) LoadBlockDataByID(
	ctx context.Context,
	id string,
	dst []byte,
) (
	height uint64, data []byte, err error,
) {
	s.mu.Lock()
	defer s.mu.Unlock()

	height, ok := s.heightByID[id]
	if !ok {
		return 0, nil, gcstore.ErrBlockDataNotFound
	}

	k := heightIDKey{h: height, id: id}
	underlying, ok := s.dataByHeightID[k]
	if !ok {
		panic(fmt.Errorf(
			"BUG: found height %d for id %q but did not find data for key",
			height, id,
		))
	}

	data = append(dst, underlying...)
	return height, data, nil
}
