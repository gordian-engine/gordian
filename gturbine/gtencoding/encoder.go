package gtencoding

import "github.com/gordian-engine/gordian/gturbine"

type ShardCodec interface {
	Encode(shred *gturbine.Shred) ([]byte, error)
	Decode(data []byte) (*gturbine.Shred, error)
}
