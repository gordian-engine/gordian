package gturbine

import (
	"crypto/ed25519"
)

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
	Index uint32 // Index of this shred within the block
	Total uint32 // Total number of shreds for this block

	Data []byte // The actual shred data
}

// Validator represents a node in the network
type Validator struct {
	PubKey  ed25519.PublicKey
	Stake   uint64
	NetAddr string
}

// Layer represents a level in the Turbine tree
type Layer struct {
	Validators []Validator
	Parent     *Layer
	Children   []*Layer
}

// Tree represents the complete Turbine propagation structure
type Tree struct {
	Root   *Layer
	Fanout uint32
	Height uint32
}
