package gturbine

type Tree struct {
	Root   *Layer
	Height uint32
	Fanout uint32
}

type Layer struct {
	Validators []uint64
	Parent     *Layer
	Children   []*Layer
}

// ShredMetadata contains metadata required to reconstruct a block from its shreds
type ShredMetadata struct {
	GroupID             string
	FullDataSize        int
	BlockHash           []byte
	Height              uint64
	TotalDataShreds     int
	TotalRecoveryShreds int
}

// Shred represents a piece of a block that can be sent over the network
type Shred struct {
	// Metadata for block reconstruction
	Metadata *ShredMetadata
	// Shred-specific metadata
	Index int    // Index of this shred within the block
	Hash  []byte // Hash of the shred data

	// Shred data
	Data []byte // The actual shred data
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
