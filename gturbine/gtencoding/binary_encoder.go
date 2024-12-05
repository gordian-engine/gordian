package gtencoding

import (
	"encoding/binary"
	"fmt"

	"github.com/google/uuid"
	"github.com/gordian-engine/gordian/gturbine"
)

const (
	intSize       = 8
	uuidSize      = 16
	blockHashSize = 32
	prefixSize    = intSize*5 + uuidSize + blockHashSize
)

// BinaryShardCodec represents a codec for encoding and decoding shreds
type BinaryShardCodec struct{}

func (bsc *BinaryShardCodec) Encode(shred gturbine.Shred) ([]byte, error) {
	out := make([]byte, prefixSize+len(shred.Data))

	// Write full data size
	binary.LittleEndian.PutUint64(out[:8], uint64(shred.FullDataSize))

	// Write block hash
	copy(out[8:40], shred.BlockHash)

	uid, err := uuid.Parse(shred.GroupID)
	if err != nil {
		return nil, fmt.Errorf("failed to parse group ID: %w", err)
	}
	// Write group ID
	copy(out[40:56], uid[:])

	// Write height
	binary.LittleEndian.PutUint64(out[56:64], shred.Height)

	// Write index
	binary.LittleEndian.PutUint64(out[64:72], uint64(shred.Index))

	// Write total data shreds
	binary.LittleEndian.PutUint64(out[72:80], uint64(shred.TotalDataShreds))

	// Write total recovery shreds
	binary.LittleEndian.PutUint64(out[80:88], uint64(shred.TotalRecoveryShreds))

	// Write data
	copy(out[prefixSize:], shred.Data)

	return out, nil

}

func (bsc *BinaryShardCodec) Decode(data []byte) (gturbine.Shred, error) {
	shred := gturbine.Shred{}

	// Read full data size
	shred.FullDataSize = int(binary.LittleEndian.Uint64(data[:8]))

	// Read block hash
	shred.BlockHash = make([]byte, blockHashSize)
	copy(shred.BlockHash, data[8:40])

	// Read group ID
	uid := uuid.UUID{}
	copy(uid[:], data[40:56])
	shred.GroupID = uid.String()

	// Read height
	shred.Height = binary.LittleEndian.Uint64(data[56:64])

	// Read index
	shred.Index = int(binary.LittleEndian.Uint64(data[64:72]))

	// Read total data shreds
	shred.TotalDataShreds = int(binary.LittleEndian.Uint64(data[72:80]))

	// Read total recovery shreds
	shred.TotalRecoveryShreds = int(binary.LittleEndian.Uint64(data[80:88]))

	// Read data
	shred.Data = make([]byte, len(data)-prefixSize)
	copy(shred.Data, data[prefixSize:])

	return shred, nil
}
