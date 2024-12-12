package gtencoding

import (
	"bytes"
	"testing"

	"github.com/google/uuid"
	"github.com/gordian-engine/gordian/gturbine"
)

func TestBinaryShardCodec_EncodeDecode(t *testing.T) {
	tests := []struct {
		name    string
		shred   *gturbine.Shred
		wantErr bool
	}{
		{
			name: "basic encode/decode",
			shred: &gturbine.Shred{
				Metadata: &gturbine.ShredMetadata{
					FullDataSize:        1000,
					BlockHash:           bytes.Repeat([]byte{1}, 32),
					GroupID:             uuid.New().String(),
					Height:              12345,
					TotalDataShreds:     10,
					TotalRecoveryShreds: 2,
				},
				Index: 5,
				Data:  []byte("test data"),
				Hash:  bytes.Repeat([]byte{2}, 32),
			},
			wantErr: false,
		},
		{
			name: "empty data",
			shred: &gturbine.Shred{
				Metadata: &gturbine.ShredMetadata{
					FullDataSize:        0,
					BlockHash:           bytes.Repeat([]byte{2}, 32),
					GroupID:             uuid.New().String(),
					Height:              67890,
					TotalDataShreds:     1,
					TotalRecoveryShreds: 0,
				},
				Index: 0,
				Data:  []byte{},
				Hash:  bytes.Repeat([]byte{2}, 32),
			},
			wantErr: false,
		},
		{
			name: "large data",
			shred: &gturbine.Shred{
				Metadata: &gturbine.ShredMetadata{
					FullDataSize:        1000000,
					BlockHash:           bytes.Repeat([]byte{3}, 32),
					GroupID:             uuid.New().String(),
					Height:              999999,
					TotalDataShreds:     100,
					TotalRecoveryShreds: 20,
				},
				Index: 50,
				Data:  bytes.Repeat([]byte("large data"), 1000),
				Hash:  bytes.Repeat([]byte{2}, 32),
			},
			wantErr: false,
		},
	}

	codec := &BinaryShardCodec{}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Test encoding
			encoded, err := codec.Encode(tt.shred)
			if (err != nil) != tt.wantErr {
				t.Errorf("BinaryShardCodec.Encode() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			if tt.wantErr {
				return
			}

			// Test decoding
			decoded, err := codec.Decode(encoded)
			if err != nil {
				t.Errorf("BinaryShardCodec.Decode() error = %v", err)
				return
			}

			sm := tt.shred.Metadata

			dm := decoded.Metadata

			// Verify all fields match
			if dm.FullDataSize != sm.FullDataSize {
				t.Errorf("FullDataSize mismatch: got %v, want %v", dm.FullDataSize, sm.FullDataSize)
			}

			if !bytes.Equal(dm.BlockHash, sm.BlockHash) {
				t.Errorf("BlockHash mismatch: got %v, want %v", dm.BlockHash, sm.BlockHash)
			}

			if dm.GroupID != sm.GroupID {
				t.Errorf("GroupID mismatch: got %v, want %v", dm.GroupID, sm.GroupID)
			}

			if dm.Height != sm.Height {
				t.Errorf("Height mismatch: got %v, want %v", dm.Height, sm.Height)
			}

			if decoded.Index != tt.shred.Index {
				t.Errorf("Index mismatch: got %v, want %v", decoded.Index, tt.shred.Index)
			}

			if dm.TotalDataShreds != sm.TotalDataShreds {
				t.Errorf("TotalDataShreds mismatch: got %v, want %v", dm.TotalDataShreds, sm.TotalDataShreds)
			}

			if dm.TotalRecoveryShreds != sm.TotalRecoveryShreds {
				t.Errorf("TotalRecoveryShreds mismatch: got %v, want %v", dm.TotalRecoveryShreds, sm.TotalRecoveryShreds)
			}

			if !bytes.Equal(decoded.Data, tt.shred.Data) {
				t.Errorf("Data mismatch: got %v, want %v", decoded.Data, tt.shred.Data)
			}

			if !bytes.Equal(decoded.Hash, tt.shred.Hash) {
				t.Errorf("Hash mismatch: got %v, want %v", decoded.Hash, tt.shred.Hash)
			}
		})
	}
}

func TestBinaryShardCodec_InvalidGroupID(t *testing.T) {
	codec := &BinaryShardCodec{}
	shred := &gturbine.Shred{
		Metadata: &gturbine.ShredMetadata{
			GroupID: "invalid-uuid",
		},
		// Other fields can be empty for this test
	}

	_, err := codec.Encode(shred)
	if err == nil {
		t.Error("Expected error when encoding invalid GroupID, got nil")
	}
}

func TestBinaryShardCodec_DataSizes(t *testing.T) {
	codec := &BinaryShardCodec{}
	shred := &gturbine.Shred{
		Metadata: &gturbine.ShredMetadata{
			FullDataSize:        1000,
			BlockHash:           bytes.Repeat([]byte{1}, 32),
			GroupID:             uuid.New().String(),
			Height:              12345,
			TotalDataShreds:     10,
			TotalRecoveryShreds: 2,
		},
		Index: 5,
		Data:  []byte("test data"),
	}

	encoded, err := codec.Encode(shred)
	if err != nil {
		t.Fatalf("Failed to encode shred: %v", err)
	}

	if len(encoded) != prefixSize+len(shred.Data) {
		t.Errorf("Encoded data size mismatch: got %v, want %v", len(encoded), prefixSize+len(shred.Data))
	}
}
