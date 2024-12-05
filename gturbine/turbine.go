package gturbine

import "github.com/gordian-engine/gordian/tm/tmconsensus"

// Config holds Turbine configuration
type Config struct {
	DataPlaneFanout uint32
	BaseFECRate     uint32
	MaxLayers       uint32
	ChunkSize       uint32
}

// Shred represents a piece of a block that can be sent over the network
type Shred struct {
	// Metadata for block reconstruction
	FullDataSize int    // Size of the full block
	BlockHash    []byte // Hash for data verification
	GroupID      string // UUID for associating shreds from the same block
	Height       uint64 // Block height for chain reference

	// Shred-specific metadata
	Index               int // Index of this shred within the block
	TotalDataShreds     int // Total number of shreds for this block
	TotalRecoveryShreds int // Total number of shreds for this block

	Data []byte // The actual shred data
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
