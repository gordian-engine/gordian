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

// Shred represents a piece of a block
type Shred struct {
	Index     uint32
	Total     uint32  
	Data      []byte
	BlockHash []byte
	Height    uint64
}

// Validator represents a node in the network
type Validator struct {
	PubKey    ed25519.PublicKey
	Stake     uint64
	NetAddr   string
}

// Layer represents a level in the Turbine tree
type Layer struct {
	Validators []Validator
	Parent     *Layer
	Children   []*Layer
}

// Tree represents the complete Turbine propagation structure
type Tree struct {
	Root      *Layer
	Fanout    uint32
	Height    uint32
}
