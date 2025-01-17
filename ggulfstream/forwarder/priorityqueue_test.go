package forwarder

import (
	"testing"

	"github.com/gordian-engine/gordian/ggulfstream/types"
	"github.com/stretchr/testify/assert"
)

func TestPriorityQueue(t *testing.T) {
	t.Run("orders by priority", func(t *testing.T) {
		pq := newPriorityQueue()
		proposers := []types.Proposer{
			{NodeID: "node1", Priority: 2},
			{NodeID: "node2", Priority: 1},
			{NodeID: "node3", Priority: 3},
		}

		pq.Update(proposers)
		ordered := pq.GetAll()

		assert.Equal(t, "node2", ordered[0].NodeID) // Priority 1
		assert.Equal(t, "node1", ordered[1].NodeID) // Priority 2
		assert.Equal(t, "node3", ordered[2].NodeID) // Priority 3
	})

	t.Run("orders by height when priority equal", func(t *testing.T) {
		pq := newPriorityQueue()
		proposers := []types.Proposer{
			{NodeID: "node1", Priority: 1, Height: 2},
			{NodeID: "node2", Priority: 1, Height: 1},
			{NodeID: "node3", Priority: 1, Height: 3},
		}

		pq.Update(proposers)
		ordered := pq.GetAll()

		assert.Equal(t, "node2", ordered[0].NodeID) // Height 1
		assert.Equal(t, "node1", ordered[1].NodeID) // Height 2
		assert.Equal(t, "node3", ordered[2].NodeID) // Height 3
	})

	t.Run("orders by round when priority and height equal", func(t *testing.T) {
		pq := newPriorityQueue()
		proposers := []types.Proposer{
			{NodeID: "node1", Priority: 1, Height: 1, Round: 2},
			{NodeID: "node2", Priority: 1, Height: 1, Round: 1},
			{NodeID: "node3", Priority: 1, Height: 1, Round: 3},
		}

		pq.Update(proposers)
		ordered := pq.GetAll()

		assert.Equal(t, "node2", ordered[0].NodeID) // Round 1
		assert.Equal(t, "node1", ordered[1].NodeID) // Round 2
		assert.Equal(t, "node3", ordered[2].NodeID) // Round 3
	})

	t.Run("handles update replacing existing", func(t *testing.T) {
		pq := newPriorityQueue()
		initial := []types.Proposer{
			{NodeID: "node1", Priority: 1},
			{NodeID: "node2", Priority: 2},
		}
		pq.Update(initial)

		updated := []types.Proposer{
			{NodeID: "node3", Priority: 1},
			{NodeID: "node4", Priority: 2},
		}
		pq.Update(updated)

		result := pq.GetAll()
		assert.Equal(t, 2, len(result))
		assert.Equal(t, "node3", result[0].NodeID)
		assert.Equal(t, "node4", result[1].NodeID)
	})
}
