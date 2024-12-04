package gtbuilder

import (
	"testing"
)

func TestTreeBuilder(t *testing.T) {
	makeIndices := func(count int) []uint64 {
		indices := make([]uint64, count)
		for i := 0; i < count; i++ {
			indices[i] = uint64(i)
		}
		return indices
	}

	t.Run("production size tree - 500 validators", func(t *testing.T) {
		b := NewTreeBuilder(200)
		indices := makeIndices(500)
		tree, err := b.BuildTree(indices, 1, 0)
		if err != nil {
			t.Fatal(err)
		}

		if tree.Height != 3 {
			t.Errorf("expected height 3 for 500 validators, got %d", tree.Height)
		}

		if len(tree.Root.Validators) != 200 {
			t.Errorf("expected 200 validators in root, got %d", len(tree.Root.Validators))
		}

		if len(tree.Root.Children[0].Validators) != 200 {
			t.Errorf("expected 200 validators in second layer, got %d", len(tree.Root.Children[0].Validators))
		}

		lastLayer := tree.Root.Children[0].Children[0]
		if len(lastLayer.Validators) != 100 {
			t.Errorf("expected 100 validators in last layer, got %d", len(lastLayer.Validators))
		}
	})

	t.Run("determinism", func(t *testing.T) {
		b := NewTreeBuilder(200)

		tree1, _ := b.BuildTree(makeIndices(500), 1, 0)
		tree2, _ := b.BuildTree(makeIndices(500), 1, 0)
		tree3, _ := b.BuildTree(makeIndices(500), 2, 0)

		if !compareValidators(tree1.Root.Validators, tree2.Root.Validators) {
			t.Error("trees not deterministic for same inputs")
		}

		if compareValidators(tree1.Root.Validators, tree3.Root.Validators) {
			t.Error("expected different trees for different slots")
		}
	})

	t.Run("children distribution", func(t *testing.T) {
		b := NewTreeBuilder(200)
		indices := makeIndices(500)
		tree, _ := b.BuildTree(indices, 1, 0)

		children := GetChildren(tree, tree.Root.Validators[0])
		expectedCount := 1
		if len(children) != expectedCount {
			t.Errorf("expected %d children for root validator, got %d", expectedCount, len(children))
		}

		for _, v := range tree.Root.Children[0].Validators {
			children := GetChildren(tree, v)
			if len(children) > 1 {
				t.Error("middle layer validator has too many children")
			}
		}
	})
}

func compareValidators(a, b []uint64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
