package tmintegration

import (
	"bytes"
	"sync"
)

// BlockDataStore is the type passed from integration apps to [FactoryFunc],
// to give gossip strategies a way to retrieve local data to be broadcast
// and to store received block data.
// Similarly, the application's consensus strategy stores the local data before
// sending the proposal signal, and looks up received block data
// when considering proposed headers.
//
// You will notice that the [github.com/gordian-engine/gordian/tm/tmgossip.Strategy]
// type does not expose any details about block storage.
// In production, the driver would decide the types to use for block data storage.
// The integration tests are coupled to byte slices for block data,
// but it is possible for a production application to pass around a concrete type,
// with the gossip strategy implementation being responsible for serializing
// the value to be transmitted across the network.
type BlockDataStore interface {
	// Retrieve the local data with the given ID,
	// returning the stored data and a flag indicating if the value was present.
	// Callers must treat the returned data as immutable.
	GetData(dataID []byte) (data []byte, ok bool)

	// Store the local data with the given ID.
	// Callers may assume that no references are held to the ID or the data.
	PutData(dataID, data []byte)
}

// BlockDataMap is a [BlockDataStore] that is backed by a sychronized map.
type BlockDataMap struct {
	mu sync.Mutex
	m  map[string][]byte
}

func NewBlockDataMap() *BlockDataMap {
	return &BlockDataMap{m: map[string][]byte{}}
}

func (m *BlockDataMap) GetData(dataID []byte) (data []byte, ok bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	data, ok = m.m[string(dataID)]
	return data, ok
}

func (m *BlockDataMap) PutData(dataID, data []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.m[string(dataID)] = bytes.Clone(data)
}

// IdentityBlockDataStore is a [BlockDataStore]
// that treats the data ID as the same value as the underlying data.
type IdentityBlockDataStore struct{}

func (IdentityBlockDataStore) GetData(dataID []byte) (data []byte, ok bool) {
	return bytes.Clone(dataID), true
}

func (IdentityBlockDataStore) PutData(dataID, data []byte) {}
