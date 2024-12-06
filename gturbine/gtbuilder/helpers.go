package gtbuilder

import (
	"bytes"

	"github.com/gordian-engine/gordian/gturbine"
	"github.com/gordian-engine/gordian/tm/tmconsensus"
)

// FindLayerPosition finds a validator's layer and index in the tree
func FindLayerPosition(tree *gturbine.Tree, pubKey []byte) (*gturbine.Layer, int) {
	if tree == nil {
		return nil, -1
	}

	layer := tree.Root
	for layer != nil {
		for i, v := range layer.Validators {
			if bytes.Equal(v.PubKey.PubKeyBytes(), pubKey) {
				return layer, i
			}
		}
		if len(layer.Children) > 0 {
			layer = layer.Children[0]
		} else {
			layer = nil
		}
	}
	return nil, -1
}

// GetChildren returns the validators that should receive forwarded shreds
func GetChildren(tree *gturbine.Tree, pubKey []byte) []tmconsensus.Validator {
	layer, idx := FindLayerPosition(tree, pubKey)
	if layer == nil || len(layer.Children) == 0 {
		return nil
	}

	// Calculate which validators in next layer this validator is responsible for
	childLayer := layer.Children[0]
	startIdx := (idx * len(childLayer.Validators)) / len(layer.Validators)
	endIdx := ((idx + 1) * len(childLayer.Validators)) / len(layer.Validators)
	if startIdx == endIdx {
		endIdx = startIdx + 1
	}

	// Ensure endIdx doesn't exceed slice bounds
	if endIdx > len(childLayer.Validators) {
		endIdx = len(childLayer.Validators)
	}

	return childLayer.Validators[startIdx:endIdx]
}
