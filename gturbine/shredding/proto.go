package shredding

import (
	"fmt"

	"github.com/gordian-engine/gordian/gturbine"
	"google.golang.org/protobuf/proto"
)

//go:generate protoc --go_out=. --go_opt=paths=source_relative shred.proto

func SerializeShred(s *gturbine.Shred, shredType ShredType, groupID []byte) ([]byte, error) {
	msg := &ShredMessage{
		Index:     s.Index,
		Total:     s.Total,
		Data:      s.Data,
		BlockHash: s.BlockHash,
		Height:    s.Height,
		Type:      int32(shredType),
		GroupId:   groupID,
	}

	return proto.Marshal(msg)
}

func DeserializeShred(data []byte) (*gturbine.Shred, ShredType, []byte, error) {
	msg := &ShredMessage{}
	if err := proto.Unmarshal(data, msg); err != nil {
		return nil, 0, nil, fmt.Errorf("unmarshal failed: %w", err)
	}

	shred := &gturbine.Shred{
		Index:     msg.Index,
		Total:     msg.Total,
		Data:      msg.Data,
		BlockHash: msg.BlockHash,
		Height:    msg.Height,
	}

	return shred, ShredType(msg.Type), msg.GroupId, nil
}
