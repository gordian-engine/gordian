package shredding

type ShredType int32

const (
    ShredTypeData ShredType = iota
    ShredTypeRecovery

    DefaultDataShreds   = 32
    DefaultRecoveryShreds = 32
    DefaultChunkSize   = 64 * 1024  // 64KB
)
