package builder

import (
	"github.com/gordian-engine/gordian/gturbine"
)

// FindLayerPosition finds a validator's layer and index in the tree
func FindLayerPosition(tree *gturbine.Tree, pubKey []byte) (*gturbine.Layer, int) {
	if tree == nil {
		return nil, -1
	}

	layer := tree.Root
	for layer != nil {
		for i, v := range layer.Validators {
			if string(v.PubKey) == string(pubKey) {
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
func GetChildren(tree *gturbine.Tree, pubKey []byte) []gturbine.Validator {
	layer, idx := FindLayerPosition(tree, pubKey)
	if layer == nil || len(layer.Children) == 0 {
		return nil
	}

	// Calculate which validators in next layer this validator is responsible for
	childLayer := layer.Children[0]
	startIdx := (idx * len(childLayer.Validators)) / len(layer.Validators)
	endIdx := ((idx + 1) * len(childLayer.Validators)) / len(layer.Validators)

	return childLayer.Validators[startIdx:endIdx]
}
