package gtbuilder

import (
	"testing"

	"github.com/gordian-engine/gordian/gturbine"
)

func makeTestTree(count int) *gturbine.Tree {
	b := NewTreeBuilder(200)
	indices := make([]uint64, count)
	for i := 0; i < count; i++ {
		indices[i] = uint64(i)
	}
	tree, _ := b.BuildTree(indices, 1, 0)
	return tree
}

func TestHelpers(t *testing.T) {
	t.Run("find position in large tree", func(t *testing.T) {
		tree := makeTestTree(500)

		// Test finding first validator
		layer, idx := FindLayerPosition(tree, 0)
		if layer == nil {
			t.Fatal("validator not found")
		}
		if layer.Validators[idx] != 0 {
			t.Errorf("found wrong validator index: want 0, got %d", layer.Validators[idx])
		}

		// Test finding last validator
		layer, idx = FindLayerPosition(tree, 499)
		if layer == nil {
			t.Fatal("validator not found")
		}
		if layer.Validators[idx] != 499 {
			t.Errorf("found wrong validator index: want 499, got %d", layer.Validators[idx])
		}

		// Test non-existent validator
		layer, idx = FindLayerPosition(tree, 1000)
		if layer != nil || idx != -1 {
			t.Error("found non-existent validator")
		}
	})

	t.Run("children distribution in large tree", func(t *testing.T) {
		tree := makeTestTree(500)

		// Root layer validators should each get 1 child
		for _, v := range tree.Root.Validators {
			children := GetChildren(tree, v)
			expectedCount := 1 // 200 validators divided by 200 fanout
			if len(children) != expectedCount {
				t.Errorf("validator %d: expected %d children, got %d", v, expectedCount, len(children))
			}
		}

		// Middle layer validators should each get 0 or 1 child
		for _, v := range tree.Root.Children[0].Validators {
			children := GetChildren(tree, v)
			if len(children) > 1 {
				t.Errorf("middle layer validator %d has too many children: %d", v, len(children))
			}
		}

		// Last layer validators should have no children
		lastLayer := tree.Root.Children[0].Children[0]
		for _, v := range lastLayer.Validators {
			children := GetChildren(tree, v)
			if len(children) != 0 {
				t.Errorf("last layer validator %d has children when it shouldn't: %d", v, len(children))
			}
		}
	})
}
