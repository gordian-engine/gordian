package gtbuilder

import (
	"github.com/gordian-engine/gordian/gturbine"
)

// FindLayerPosition finds a validator's layer and index in the tree
func FindLayerPosition(tree *gturbine.Tree, valIndex uint64) (*gturbine.Layer, int) {
	if tree == nil {
		return nil, -1
	}

	layer := tree.Root
	for layer != nil {
		for i, idx := range layer.Validators {
			if idx == valIndex {
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

// GetChildren returns the validator indices that should receive forwarded shreds
func GetChildren(tree *gturbine.Tree, valIndex uint64) []uint64 {
	layer, idx := FindLayerPosition(tree, valIndex)
	if layer == nil || len(layer.Children) == 0 {
		return nil
	}

	// Same distribution logic, now returning indices
	childLayer := layer.Children[0]
	startIdx := (idx * len(childLayer.Validators)) / len(layer.Validators)
	endIdx := ((idx + 1) * len(childLayer.Validators)) / len(layer.Validators)
	if startIdx == endIdx {
		endIdx = startIdx + 1
	}
	if endIdx > len(childLayer.Validators) {
		endIdx = len(childLayer.Validators)
	}

	return childLayer.Validators[startIdx:endIdx]
}
