package gtbuilder

import (
	"bytes"
	"crypto/ed25519"
	"testing"

	"github.com/gordian-engine/gordian/gcrypto"
	"github.com/gordian-engine/gordian/tm/tmconsensus"
)

func TestTreeBuilder(t *testing.T) {
	makeValidators := func(count int) []tmconsensus.Validator {
		vals := make([]tmconsensus.Validator, count)
		for i := 0; i < count; i++ {
			pubKey := ed25519.PublicKey(make([]byte, ed25519.PublicKeySize))
			pubKey[0] = byte(i % 255)
			pubKey[1] = byte(i / 255)
			vals[i] = tmconsensus.Validator{
				PubKey: gcrypto.Ed25519PubKey(pubKey),
				Power:  uint64((count - i) * 100), // Higher powers first
			}
		}
		return vals
	}

	t.Run("production size tree - 500 validators", func(t *testing.T) {
		b := NewTreeBuilder(200) // Production fanout
		validators := makeValidators(500)
		tree, err := b.BuildTree(validators, 1, 0)
		if err != nil {
			t.Fatal(err)
		}

		// Should have 3 layers with 200 fanout
		if tree.Height != 3 {
			t.Errorf("expected height 3 for 500 validators, got %d", tree.Height)
		}

		// Root layer should be full
		if len(tree.Root.Validators) != 200 {
			t.Errorf("expected 200 validators in root, got %d", len(tree.Root.Validators))
		}

		// Second layer should be full
		if len(tree.Root.Children[0].Validators) != 200 {
			t.Errorf("expected 200 validators in second layer, got %d", len(tree.Root.Children[0].Validators))
		}

		// Last layer should have remaining 100 validators
		lastLayer := tree.Root.Children[0].Children[0]
		if len(lastLayer.Validators) != 100 {
			t.Errorf("expected 100 validators in last layer, got %d", len(lastLayer.Validators))
		}

		// High power validators should be in earlier layers
		averagePowerRoot := averageLayerPower(tree.Root.Validators)
		averagePowerLast := averageLayerPower(lastLayer.Validators)
		if averagePowerRoot <= averagePowerLast {
			t.Error("expected higher average power in root layer")
		}
	})

	t.Run("large tree determinism", func(t *testing.T) {
		b := NewTreeBuilder(200)
		validators := makeValidators(500)

		tree1, _ := b.BuildTree(validators, 1, 0)
		tree2, _ := b.BuildTree(validators, 1, 0)

		// Same inputs = same tree
		if !compareValidators(tree1.Root.Validators, tree2.Root.Validators) {
			t.Error("trees not deterministic for same inputs")
		}

		// Different slots = different trees
		tree3, _ := b.BuildTree(validators, 2, 0)
		if compareValidators(tree1.Root.Validators, tree3.Root.Validators) {
			t.Error("expected different trees for different slots")
		}
	})

	t.Run("large tree children distribution", func(t *testing.T) {
		b := NewTreeBuilder(200)
		validators := makeValidators(500)
		tree, _ := b.BuildTree(validators, 1, 0)

		// Check root validator's children
		rootChildren := GetChildren(tree, tree.Root.Validators[0].PubKey.PubKeyBytes())
		expectedChildCount := 1 // Should get 1/200th of next layer
		if len(rootChildren) != expectedChildCount {
			t.Errorf("expected %d children for root validator, got %d", expectedChildCount, len(rootChildren))
		}

		// Verify all middle layer validators get children
		for _, v := range tree.Root.Children[0].Validators {
			children := GetChildren(tree, v.PubKey.PubKeyBytes())
			if len(children) == 0 {
				t.Error("middle layer validator has no children")
			}
		}
	})
}

func averageLayerPower(validators []tmconsensus.Validator) uint64 {
	if len(validators) == 0 {
		return 0
	}
	var total uint64
	for _, v := range validators {
		total += v.Power
	}
	return total / uint64(len(validators))
}

func compareValidators(a, b []tmconsensus.Validator) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Power != b[i].Power || !bytes.Equal(a[i].PubKey.PubKeyBytes(), b[i].PubKey.PubKeyBytes()) {
			return false
		}
	}
	return true
}
