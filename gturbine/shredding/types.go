package shredding

// ShredType indicates whether a shred contains data or recovery information
type ShredType int32

const (
    ShredTypeData ShredType = iota
    ShredTypeRecovery
)
