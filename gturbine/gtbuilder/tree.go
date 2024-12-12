package gtbuilder

import (
	"crypto/sha256"
	"encoding/binary"

	"github.com/gordian-engine/gordian/gturbine"
)

type TreeBuilder struct {
	fanout uint32
}

func NewTreeBuilder(fanout uint32) *TreeBuilder {
	return &TreeBuilder{
		fanout: fanout,
	}
}

// BuildTree creates a new propagation tree with stake-weighted validator ordering
// It takes valIndices which is a presorted array of indices. It returns the tree
// with the indices as the values. It is up to the caller to map these indicies to
// the actual validators
func (b *TreeBuilder) BuildTree(valIndices []uint64, slot uint64, shredIndex uint32) (*gturbine.Tree, error) {
	if len(valIndices) == 0 {
		return nil, nil
	}

	// Generate deterministic seed for shuffling
	seed := b.deriveTreeSeed(slot, shredIndex, 0)

	// Fisher-Yates shuffle with deterministic seed
	for i := len(valIndices) - 1; i > 0; i-- {
		// Use seed to generate index
		j := int(binary.LittleEndian.Uint64(seed) % uint64(i+1))
		valIndices[i], valIndices[j] = valIndices[j], valIndices[i]

		// Update seed for next iteration
		h := sha256.New()
		h.Write(seed)
		seed = h.Sum(nil)
	}

	// Build layers
	tree := &gturbine.Tree{
		Fanout: b.fanout,
	}

	remaining := valIndices
	currentLayer := &gturbine.Layer{}
	tree.Root = currentLayer
	tree.Height = 1

	for len(remaining) > 0 {
		// Take up to fanout validators for current layer
		takeCount := min(len(remaining), int(b.fanout))
		currentLayer.Validators = remaining[:takeCount]
		remaining = remaining[takeCount:]

		if len(remaining) > 0 {
			// Create new layer
			newLayer := &gturbine.Layer{
				Parent: currentLayer,
			}
			currentLayer.Children = append(currentLayer.Children, newLayer)
			currentLayer = newLayer
			tree.Height++
		}
	}

	return tree, nil
}

// deriveTreeSeed generates deterministic seed for tree creation
func (b *TreeBuilder) deriveTreeSeed(slot uint64, shredIndex uint32, shredType uint8) []byte {
	h := sha256.New()

	slotBytes := make([]byte, 8)
	binary.LittleEndian.PutUint64(slotBytes, slot)
	h.Write(slotBytes)

	shredBytes := make([]byte, 4)
	binary.LittleEndian.PutUint32(shredBytes, shredIndex)
	h.Write(shredBytes)

	h.Write([]byte{shredType})

	return h.Sum(nil)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
