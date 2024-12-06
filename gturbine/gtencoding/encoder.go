package gtencoding

import "github.com/gordian-engine/gordian/gturbine/gtshred"

type ShardCodec interface {
	Encode(shred *gtshred.Shred) ([]byte, error)
	Decode(data []byte) (*gtshred.Shred, error)
}
