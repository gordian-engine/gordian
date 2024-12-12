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
	uuidSize    = 16

	fullDataSizeSize        = int64Size
	blockHashSize           = 32
	groupIDSize             = uuidSize
	heightSize              = int64Size
	indexSize               = int64Size
	totalDataShredsSize     = int64Size
	totalRecoveryShredsSize = int64Size
	shredHashSize           = 32

	prefixSize    = versionSize + fullDataSizeSize + blockHashSize + groupIDSize + heightSize + indexSize + totalDataShredsSize + totalRecoveryShredsSize + shredHashSize
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

	m := shred.Metadata

	// Write full data size
	binary.LittleEndian.PutUint64(out[2:10], uint64(m.FullDataSize))

	// Write block hash
	copy(out[10:42], m.BlockHash)

	uid, err := uuid.Parse(m.GroupID)
	if err != nil {
		return nil, fmt.Errorf("failed to parse group ID: %w", err)
	}
	// Write group ID
	copy(out[42:58], uid[:])

	// Write height
	binary.LittleEndian.PutUint64(out[58:66], m.Height)

	// Write index
	binary.LittleEndian.PutUint64(out[66:74], uint64(shred.Index))

	// Write total data shreds
	binary.LittleEndian.PutUint64(out[74:82], uint64(m.TotalDataShreds))

	// Write total recovery shreds
	binary.LittleEndian.PutUint64(out[82:90], uint64(m.TotalRecoveryShreds))

	// Write hash
	copy(out[90:122], shred.Hash)

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

	m := new(gturbine.ShredMetadata)

	// Read full data size
	m.FullDataSize = int(binary.LittleEndian.Uint64(data[2:10]))

	// Read block hash
	m.BlockHash = make([]byte, blockHashSize)
	copy(m.BlockHash, data[10:42])

	// Read group ID
	uid := uuid.UUID{}
	copy(uid[:], data[42:58])
	m.GroupID = uid.String()

	// Read height
	m.Height = binary.LittleEndian.Uint64(data[58:66])

	// Read index
	shred.Index = int(binary.LittleEndian.Uint64(data[66:74]))

	// Read total data shreds
	m.TotalDataShreds = int(binary.LittleEndian.Uint64(data[74:82]))

	// Read total recovery shreds
	m.TotalRecoveryShreds = int(binary.LittleEndian.Uint64(data[82:90]))

	// Read hash
	shred.Hash = make([]byte, shredHashSize)
	copy(shred.Hash, data[90:122])

	// Read data
	shred.Data = make([]byte, len(data)-prefixSize)
	copy(shred.Data, data[prefixSize:])

	shred.Metadata = m

	return &shred, nil
}
