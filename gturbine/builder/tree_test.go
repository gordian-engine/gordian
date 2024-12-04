package builder

import (
	"crypto/ed25519"
	"fmt"
	"testing"

	"github.com/gordian-engine/gordian/gturbine"
)

func TestTreeBuilder(t *testing.T) {
	makeValidators := func(count int) []gturbine.Validator {
		vals := make([]gturbine.Validator, count)
		for i := 0; i < count; i++ {
			vals[i] = gturbine.Validator{
				PubKey:  ed25519.PublicKey{byte(i + 1)},
				Stake:   uint64((i + 1) * 100),
				NetAddr: fmt.Sprintf("validator-%d", i),
			}
		}
		return vals
	}

	t.Run("empty validator set", func(t *testing.T) {
		b := NewTreeBuilder(2)
		tree, err := b.BuildTree(nil, 1, 0)
		if err != nil {
			t.Fatal(err)
		}
		if tree != nil {
			t.Error("expected nil tree for empty validator set")
		}
	})

	t.Run("single validator", func(t *testing.T) {
		b := NewTreeBuilder(2)
		validators := makeValidators(1)

		tree, err := b.BuildTree(validators, 1, 0)
		if err != nil {
			t.Fatal(err)
		}

		if tree.Height != 1 {
			t.Errorf("expected height 1, got %d", tree.Height)
		}
		if len(tree.Root.Validators) != 1 {
			t.Errorf("expected 1 validator in root, got %d", len(tree.Root.Validators))
		}
		if len(tree.Root.Children) != 0 {
			t.Error("root should have no children")
		}
	})

	t.Run("multi-layer tree", func(t *testing.T) {
		b := NewTreeBuilder(2)
		validators := makeValidators(5)

		tree, err := b.BuildTree(validators, 1, 0)
		if err != nil {
			t.Fatal(err)
		}

		// Check tree structure
		if tree.Height != 3 {
			t.Errorf("expected height 3, got %d", tree.Height)
		}

		// Root layer should have fanout validators
		if len(tree.Root.Validators) != 2 {
			t.Errorf("expected 2 validators in root, got %d", len(tree.Root.Validators))
		}

		// All non-leaf layers should have fanout validators
		if len(tree.Root.Children[0].Validators) != 2 {
			t.Errorf("expected 2 validators in middle layer, got %d", len(tree.Root.Children[0].Validators))
		}

		// Last layer should have remaining validator
		lastLayer := tree.Root.Children[0].Children[0]
		if len(lastLayer.Validators) != 1 {
			t.Errorf("expected 1 validator in last layer, got %d", len(lastLayer.Validators))
		}
	})

	t.Run("determinism", func(t *testing.T) {
		b := NewTreeBuilder(2)
		validators := makeValidators(5)

		tree1, _ := b.BuildTree(validators, 1, 0)
		tree2, _ := b.BuildTree(validators, 1, 0)

		// Same inputs should produce identical trees
		if !compareValidators(tree1.Root.Validators, tree2.Root.Validators) {
			t.Error("trees not deterministic for same inputs")
		}

		// Different slots should produce different trees
		tree3, _ := b.BuildTree(validators, 2, 0)
		if compareValidators(tree1.Root.Validators, tree3.Root.Validators) {
			t.Error("expected different trees for different slots")
		}
	})

	t.Run("stake ordering", func(t *testing.T) {
		b := NewTreeBuilder(3)
		validators := []gturbine.Validator{
			{PubKey: ed25519.PublicKey{1}, Stake: 100},
			{PubKey: ed25519.PublicKey{2}, Stake: 500},
			{PubKey: ed25519.PublicKey{3}, Stake: 300},
		}

		tree, _ := b.BuildTree(validators, 1, 0)

		// After stake sorting and deterministic shuffling, we should maintain relative position
		// of high stake validators in earlier layers
		highStakeCount := 0
		for _, v := range tree.Root.Validators {
			if v.Stake >= 300 {
				highStakeCount++
			}
		}

		if highStakeCount < 1 {
			t.Error("expected high stake validators in root layer")
		}
	})
}

func compareValidators(a, b []gturbine.Validator) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Stake != b[i].Stake || string(a[i].PubKey) != string(b[i].PubKey) {
			return false
		}
	}
	return true
}
