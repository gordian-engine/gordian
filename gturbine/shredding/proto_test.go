package shredding

import (
	"bytes"
	"crypto/rand"
	"testing"

	"github.com/gordian-engine/gordian/gturbine"
)

func TestShredSerialization(t *testing.T) {
	tests := []struct {
		name      string
		shred     *gturbine.Shred
		shredType ShredType
		wantErr   bool
	}{
		{
			name: "basic shred",
			shred: &gturbine.Shred{
				Index:     1,
				Total:     4,
				Data:      []byte("test data"),
				BlockHash: []byte("block hash"),
				Height:    100,
			},
			shredType: ShredTypeData,
			wantErr:   false,
		},
		{
			name: "empty data",
			shred: &gturbine.Shred{
				Index:     1,
				Total:     4,
				Data:      []byte{},
				BlockHash: []byte("block hash"),
				Height:    100,
			},
			shredType: ShredTypeData,
			wantErr:   false,
		},
		{
			name: "recovery shred",
			shred: &gturbine.Shred{
				Index:     1,
				Total:     4,
				Data:      []byte("recovery data"),
				BlockHash: []byte("block hash"),
				Height:    100,
			},
			shredType: ShredTypeRecovery,
			wantErr:   false,
		},
		{
			name: "large data",
			shred: func() *gturbine.Shred {
				data := make([]byte, 1024*1024) // 1MB
				rand.Read(data)
				return &gturbine.Shred{
					Index:     1,
					Total:     4,
					Data:      data,
					BlockHash: []byte("block hash"),
					Height:    100,
				}
			}(),
			shredType: ShredTypeData,
			wantErr:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			groupID := []byte("test-group")
			
			data, err := SerializeShred(tt.shred, tt.shredType, groupID)
			if (err != nil) != tt.wantErr {
				t.Errorf("SerializeShred() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr {
				return
			}

			decoded, decodedType, decodedGroupID, err := DeserializeShred(data)
			if err != nil {
				t.Fatalf("DeserializeShred() error = %v", err)
			}

			if !compareShreds(tt.shred, decoded) {
				t.Error("decoded shred does not match original")
			}

			if decodedType != tt.shredType {
				t.Errorf("wrong shred type: got %v, want %v", decodedType, tt.shredType)
			}

			if !bytes.Equal(groupID, decodedGroupID) {
				t.Errorf("wrong group ID: got %x, want %x", decodedGroupID, groupID)
			}
		})
	}
}

func compareShreds(a, b *gturbine.Shred) bool {
	return a.Index == b.Index &&
		a.Total == b.Total &&
		bytes.Equal(a.Data, b.Data) &&
		bytes.Equal(a.BlockHash, b.BlockHash) &&
		a.Height == b.Height
}
