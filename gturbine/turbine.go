package gturbine

import "github.com/gordian-engine/gordian/tm/tmconsensus"

// Config holds Turbine configuration
type Config struct {
	DataPlaneFanout uint32
	BaseFECRate     uint32
	MaxLayers       uint32
	ChunkSize       uint32
}

type Tree struct {
	Root   *Layer
	Height uint32
	Fanout uint32
}

type Layer struct {
	Validators []tmconsensus.Validator
	Parent     *Layer
	Children   []*Layer
}

// GetLayerByHeight returns layer at given height (0-based)
func (t *Tree) GetLayerByHeight(height uint32) *Layer {
	if height >= t.Height {
		return nil
	}

	current := t.Root
	for i := uint32(0); i < height; i++ {
		if len(current.Children) == 0 {
			return nil
		}
		current = current.Children[0]
	}
	return current
}
