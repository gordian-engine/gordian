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
				FullDataSize:        1000,
				BlockHash:           bytes.Repeat([]byte{1}, 32),
				GroupID:             uuid.New().String(),
				Height:              12345,
				Index:               5,
				TotalDataShreds:     10,
				TotalRecoveryShreds: 2,
				Data:                []byte("test data"),
			},
			wantErr: false,
		},
		{
			name: "empty data",
			shred: &gturbine.Shred{
				FullDataSize:        0,
				BlockHash:           bytes.Repeat([]byte{2}, 32),
				GroupID:             uuid.New().String(),
				Height:              67890,
				Index:               0,
				TotalDataShreds:     1,
				TotalRecoveryShreds: 0,
				Data:                []byte{},
			},
			wantErr: false,
		},
		{
			name: "large data",
			shred: &gturbine.Shred{
				FullDataSize:        1000000,
				BlockHash:           bytes.Repeat([]byte{3}, 32),
				GroupID:             uuid.New().String(),
				Height:              999999,
				Index:               50,
				TotalDataShreds:     100,
				TotalRecoveryShreds: 20,
				Data:                bytes.Repeat([]byte("large data"), 1000),
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

			// Verify all fields match
			if decoded.FullDataSize != tt.shred.FullDataSize {
				t.Errorf("FullDataSize mismatch: got %v, want %v", decoded.FullDataSize, tt.shred.FullDataSize)
			}

			if !bytes.Equal(decoded.BlockHash, tt.shred.BlockHash) {
				t.Errorf("BlockHash mismatch: got %v, want %v", decoded.BlockHash, tt.shred.BlockHash)
			}

			if decoded.GroupID != tt.shred.GroupID {
				t.Errorf("GroupID mismatch: got %v, want %v", decoded.GroupID, tt.shred.GroupID)
			}

			if decoded.Height != tt.shred.Height {
				t.Errorf("Height mismatch: got %v, want %v", decoded.Height, tt.shred.Height)
			}

			if decoded.Index != tt.shred.Index {
				t.Errorf("Index mismatch: got %v, want %v", decoded.Index, tt.shred.Index)
			}

			if decoded.TotalDataShreds != tt.shred.TotalDataShreds {
				t.Errorf("TotalDataShreds mismatch: got %v, want %v", decoded.TotalDataShreds, tt.shred.TotalDataShreds)
			}

			if decoded.TotalRecoveryShreds != tt.shred.TotalRecoveryShreds {
				t.Errorf("TotalRecoveryShreds mismatch: got %v, want %v", decoded.TotalRecoveryShreds, tt.shred.TotalRecoveryShreds)
			}

			if !bytes.Equal(decoded.Data, tt.shred.Data) {
				t.Errorf("Data mismatch: got %v, want %v", decoded.Data, tt.shred.Data)
			}
		})
	}
}

func TestBinaryShardCodec_InvalidGroupID(t *testing.T) {
	codec := &BinaryShardCodec{}
	shred := &gturbine.Shred{
		GroupID: "invalid-uuid",
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
		FullDataSize:        1000,
		BlockHash:           bytes.Repeat([]byte{1}, 32),
		GroupID:             uuid.New().String(),
		Height:              12345,
		Index:               5,
		TotalDataShreds:     10,
		TotalRecoveryShreds: 2,
		Data:                []byte("test data"),
	}

	encoded, err := codec.Encode(shred)
	if err != nil {
		t.Fatalf("Failed to encode shred: %v", err)
	}

	if len(encoded) != prefixSize+len(shred.Data) {
		t.Errorf("Encoded data size mismatch: got %v, want %v", len(encoded), prefixSize+len(shred.Data))
	}
}
