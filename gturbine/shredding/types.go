package shredding

type ShredType int32

const (
    ShredTypeData ShredType = iota
    ShredTypeRecovery

    DefaultDataShreds     = 32
    DefaultRecoveryShreds = 32
    DefaultChunkSize     = 4 * 1024 * 1024  // 4MB chunks for 128MB blocks with 32 shreds
    MaxBlockSize        = 128 * 1024 * 1024 // 128MB maximum block size
)
