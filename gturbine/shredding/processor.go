package shredding

import (
	"crypto/rand"
	"fmt"

	"github.com/gordian-engine/gordian/gturbine"
	"github.com/gordian-engine/gordian/gturbine/erasure"
)

type Processor struct {
	shredder *Shredder
	encoder  *erasure.Encoder
}

func NewProcessor(chunkSize uint32, dataShards, parityShards int) (*Processor, error) {
	encoder, err := erasure.NewEncoder(dataShards, parityShards)
	if err != nil {
		return nil, fmt.Errorf("failed to create encoder: %w", err)
	}

	return &Processor{
		shredder: NewShredder(chunkSize),
		encoder:  encoder,
	}, nil
}
