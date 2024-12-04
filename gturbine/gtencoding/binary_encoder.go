package gtencoding

import (
	"encoding/binary"
	"fmt"

	"github.com/google/uuid"
	"github.com/gordian-engine/gordian/gturbine"
)

const (
	int16Size   = 2
	int32Size   = 4
	int64Size   = 8
	versionSize = int16Size
	typeSize    = int16Size
	uuidSize    = 16

	fullDataSizeSize        = int64Size
	blockHashSize           = 32
	groupIDSize             = uuidSize
	heightSize              = int64Size
	indexSize               = int64Size
	totalDataShredsSize     = int64Size
	totalRecoveryShredsSize = int64Size

	prefixSize    = versionSize + typeSize + fullDataSizeSize + blockHashSize + groupIDSize + heightSize + indexSize + totalDataShredsSize + totalRecoveryShredsSize
	binaryVersion = 1
)

// BinaryShardCodec represents a codec for encoding and decoding shreds
type BinaryShardCodec struct{}

func NewBinaryShardCodec() *BinaryShardCodec {
	return &BinaryShardCodec{}
}

func (bsc *BinaryShardCodec) Encode(shred *gturbine.Shred) ([]byte, error) {
	out := make([]byte, prefixSize+len(shred.Data))

	// Write version
	binary.LittleEndian.PutUint16(out[:2], binaryVersion)

	// Write type
	binary.LittleEndian.PutUint16(out[2:4], uint16(shred.Type))

	// Write full data size
	binary.LittleEndian.PutUint64(out[4:12], uint64(shred.FullDataSize))

	// Write block hash
	copy(out[12:44], shred.BlockHash)

	uid, err := uuid.Parse(shred.GroupID)
	if err != nil {
		return nil, fmt.Errorf("failed to parse group ID: %w", err)
	}
	// Write group ID
	copy(out[44:60], uid[:])

	// Write height
	binary.LittleEndian.PutUint64(out[60:68], shred.Height)

	// Write index
	binary.LittleEndian.PutUint64(out[68:76], uint64(shred.Index))

	// Write total data shreds
	binary.LittleEndian.PutUint64(out[76:84], uint64(shred.TotalDataShreds))

	// Write total recovery shreds
	binary.LittleEndian.PutUint64(out[84:92], uint64(shred.TotalRecoveryShreds))

	// Write data
	copy(out[prefixSize:], shred.Data)

	return out, nil

}

func (bsc *BinaryShardCodec) Decode(data []byte) (*gturbine.Shred, error) {
	shred := gturbine.Shred{}

	// Read version
	version := binary.LittleEndian.Uint16(data[:2])
	if version != binaryVersion {
		return nil, fmt.Errorf("unsupported version: %d", version)
	}

	// Read type
	shred.Type = gturbine.ShredType(binary.LittleEndian.Uint16(data[2:4]))

	// Read full data size
	shred.FullDataSize = int(binary.LittleEndian.Uint64(data[4:12]))

	// Read block hash
	shred.BlockHash = make([]byte, blockHashSize)
	copy(shred.BlockHash, data[12:44])

	// Read group ID
	uid := uuid.UUID{}
	copy(uid[:], data[44:60])
	shred.GroupID = uid.String()

	// Read height
	shred.Height = binary.LittleEndian.Uint64(data[60:68])

	// Read index
	shred.Index = int(binary.LittleEndian.Uint64(data[68:76]))

	// Read total data shreds
	shred.TotalDataShreds = int(binary.LittleEndian.Uint64(data[76:84]))

	// Read total recovery shreds
	shred.TotalRecoveryShreds = int(binary.LittleEndian.Uint64(data[84:92]))

	// Read data
	shred.Data = make([]byte, len(data)-prefixSize)
	copy(shred.Data, data[prefixSize:])

	return &shred, nil
}
