package gtshred

type ShredType int

const (
	DataShred ShredType = iota
	RecoveryShred
)

// Shred represents a piece of a block that can be sent over the network
type Shred struct {
	// Metadata for block reconstruction
	FullDataSize int    // Size of the full block
	BlockHash    []byte // Hash for data verification
	GroupID      string // UUID for associating shreds from the same block
	Height       uint64 // Block height for chain reference

	// Shred-specific metadata
	Type                ShredType
	Index               int // Index of this shred within the block
	TotalDataShreds     int // Total number of shreds for this block
	TotalRecoveryShreds int // Total number of shreds for this block

	Data []byte // The actual shred data
}
