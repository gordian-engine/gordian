package builder

import (
	"crypto/ed25519"
	"testing"

	"github.com/gordian-engine/gordian/gturbine"
)

func TestHelpers(t *testing.T) {
	makeTree := func() *gturbine.Tree {
		b := NewTreeBuilder(2)
		validators := []gturbine.Validator{
			{PubKey: ed25519.PublicKey{1}, Stake: 100},
			{PubKey: ed25519.PublicKey{2}, Stake: 200},
			{PubKey: ed25519.PublicKey{3}, Stake: 300},
			{PubKey: ed25519.PublicKey{4}, Stake: 400},
			{PubKey: ed25519.PublicKey{5}, Stake: 500},
		}
		tree, _ := b.BuildTree(validators, 1, 0)
		return tree
	}

	t.Run("find position", func(t *testing.T) {
		tree := makeTree()
		
		layer, idx := FindLayerPosition(tree, []byte{1})
		if layer == nil {
			t.Fatal("validator not found")
		}
		if idx == -1 {
			t.Error("invalid index returned")
		}

		// Test unknown validator
		layer, idx = FindLayerPosition(tree, []byte{99})
		if layer != nil || idx != -1 {
			t.Error("found non-existent validator")
		}
	})

	t.Run("get children", func(t *testing.T) {
		tree := makeTree()

		// Root validator should have children
		children := GetChildren(tree, tree.Root.Validators[0].PubKey)
		if len(children) == 0 {
			t.Error("root validator should have children")
		}

		// Last layer validator should have no children
		lastLayer := tree.Root.Children[0].Children[0]
		leafChildren := GetChildren(tree, lastLayer.Validators[0].PubKey)
		if len(leafChildren) != 0 {
			t.Error("leaf validator should have no children")
		}

		// Check distribution
		rootChildren := GetChildren(tree, tree.Root.Validators[0].PubKey)
		if len(rootChildren) != 1 {
			t.Errorf("expected 1 child for first root validator, got %d", len(rootChildren))
		}
	})
}
