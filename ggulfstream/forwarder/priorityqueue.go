package forwarder

import (
	"container/heap"
	"sync"

	"github.com/gordian-engine/gordian/ggulfstream/types"
)

// priorityQueue implements a thread-safe priority queue of proposers
type priorityQueue struct {
	mu    sync.RWMutex
	items proposerHeap
}

// newPriorityQueue creates a new priority queue
func newPriorityQueue() *priorityQueue {
	pq := &priorityQueue{
		items: make(proposerHeap, 0),
	}
	heap.Init(&pq.items)
	return pq
}

// Update replaces the entire queue with new proposers
func (pq *priorityQueue) Update(proposers []types.Proposer) {
	pq.mu.Lock()
	defer pq.mu.Unlock()

	// Clear existing queue
	pq.items = pq.items[:0]

	// Add new proposers
	for _, p := range proposers {
		heap.Push(&pq.items, p)
	}
}

// GetAll returns a copy of all proposers in priority order
func (pq *priorityQueue) GetAll() []types.Proposer {
	pq.mu.RLock()
	defer pq.mu.RUnlock()

	result := make([]types.Proposer, len(pq.items))
	for i := range pq.items {
		result[i] = pq.items[i]
	}
	return result
}

// proposerHeap implements heap.Interface
type proposerHeap []types.Proposer

func (h proposerHeap) Len() int { return len(h) }

func (h proposerHeap) Less(i, j int) bool {
	// First by priority
	if h[i].Priority != h[j].Priority {
		return h[i].Priority < h[j].Priority
	}
	// Then by height
	if h[i].Height != h[j].Height {
		return h[i].Height < h[j].Height
	}
	// Finally by round
	return h[i].Round < h[j].Round
}

func (h proposerHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
}

func (h *proposerHeap) Push(x interface{}) {
	*h = append(*h, x.(types.Proposer))
}

func (h *proposerHeap) Pop() interface{} {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[0 : n-1]
	return x
}