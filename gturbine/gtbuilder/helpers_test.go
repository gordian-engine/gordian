package gtbuilder

import (
	"bytes"
	"crypto/ed25519"
	"testing"

	"github.com/gordian-engine/gordian/gcrypto"
	"github.com/gordian-engine/gordian/gturbine"
	"github.com/gordian-engine/gordian/tm/tmconsensus"
)

func makeTestTree(validatorCount int) *gturbine.Tree {
	b := NewTreeBuilder(200)
	validators := make([]tmconsensus.Validator, validatorCount)

	for i := 0; i < validatorCount; i++ {
		pubKey := ed25519.PublicKey(make([]byte, ed25519.PublicKeySize))
		pubKey[0] = byte(i % 255)
		pubKey[1] = byte(i / 255)
		validators[i] = tmconsensus.Validator{
			PubKey: gcrypto.Ed25519PubKey(pubKey),
			Power:  uint64((validatorCount - i) * 100),
		}
	}

	tree, _ := b.BuildTree(validators, 1, 0)
	return tree
}

func TestHelpers(t *testing.T) {
	t.Run("find position in large tree", func(t *testing.T) {
		tree := makeTestTree(500)

		// Test finding root validator
		searchKey := make([]byte, ed25519.PublicKeySize)
		searchKey[0] = byte(0 % 255)
		searchKey[1] = byte(0 / 255)
		layer, idx := FindLayerPosition(tree, searchKey)
		if layer == nil {
			t.Fatal("validator not found")
		}

		// Verify the found validator has our search key
		foundKey := layer.Validators[idx].PubKey.PubKeyBytes()
		if !bytes.Equal(searchKey, foundKey) {
			t.Errorf("found wrong validator: want %v, got %v", searchKey, foundKey)
		}

		// Test finding last layer validator
		searchKey[0] = byte(499 % 255)
		searchKey[1] = byte(499 / 255)
		layer, idx = FindLayerPosition(tree, searchKey)
		if layer == nil {
			t.Fatal("validator not found")
		}
		foundKey = layer.Validators[idx].PubKey.PubKeyBytes()
		if !bytes.Equal(searchKey, foundKey) {
			t.Errorf("found wrong validator: want %v, got %v", searchKey, foundKey)
		}

		// Test non-existent validator
		badKey := make([]byte, ed25519.PublicKeySize)
		badKey[0] = 255
		badKey[1] = 255
		layer, idx = FindLayerPosition(tree, badKey)
		if layer != nil || idx != -1 {
			t.Error("found non-existent validator")
		}
	})

	t.Run("children distribution in large tree", func(t *testing.T) {
		tree := makeTestTree(500)

		// Root layer validators should each get 1 child
		for i, v := range tree.Root.Validators {
			children := GetChildren(tree, v.PubKey.PubKeyBytes())
			expectedCount := 1 // 200 validators divided by 200 fanout
			if len(children) != expectedCount {
				t.Errorf("validator %d: expected %d children, got %d", i, expectedCount, len(children))
			}
		}

		// Middle layer validators should each get 0 or 1 child
		for i, v := range tree.Root.Children[0].Validators {
			children := GetChildren(tree, v.PubKey.PubKeyBytes())
			if len(children) > 1 {
				t.Errorf("middle layer validator %d has too many children: %d", i, len(children))
			}
		}

		// Last layer validators should have no children
		lastLayer := tree.Root.Children[0].Children[0]
		for i, v := range lastLayer.Validators {
			children := GetChildren(tree, v.PubKey.PubKeyBytes())
			if len(children) != 0 {
				t.Errorf("last layer validator %d has children when it shouldn't: %d", i, len(children))
			}
		}
	})
}
