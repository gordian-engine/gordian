package shredding

import (
	"bytes"
	"crypto/rand"
	"testing"
)

func TestShredder(t *testing.T) {
	tests := []struct {
		name      string
		block     []byte
		chunkSize uint32
		height    uint64
		wantErr   bool
	}{
		{
			name:      "empty block",
			block:     []byte{},
			chunkSize: 32,
			height:    1,
			wantErr:   true,
		},
		{
			name:      "block smaller than chunk",
			block:     []byte("small block"),
			chunkSize: 32,
			height:    1,
			wantErr:   false,
		},
		{
			name:      "block equal to chunk",
			block:     bytes.Repeat([]byte("a"), 32),
			chunkSize: 32,
			height:    1,
			wantErr:   false,
		},
		{
			name:      "block larger than chunk",
			block:     bytes.Repeat([]byte("a"), 100),
			chunkSize: 32,
			height:    1,
			wantErr:   false,
		},
		{
			name: "large block",
			block: func() []byte {
				data := make([]byte, 1024*1024) // 1MB
				rand.Read(data)
				return data
			}(),
			chunkSize: 64 * 1024, // 64KB chunks
			height:    1,
			wantErr:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			shredder := NewShredder(tt.chunkSize)

			// Test shredding
			shreds, err := shredder.ShredBlock(tt.block, tt.height)
			if (err != nil) != tt.wantErr {
				t.Errorf("ShredBlock() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.wantErr {
				return
			}

			// Validate shred properties
			expectedShreds := (len(tt.block) + int(tt.chunkSize) - 1) / int(tt.chunkSize)
			if len(shreds) != expectedShreds {
				t.Errorf("wrong number of shreds: got %d, want %d", len(shreds), expectedShreds)
			}

			for i, shred := range shreds {
				if shred.Index != uint32(i) {
					t.Errorf("wrong index for shred %d: got %d", i, shred.Index)
				}
				if shred.Total != uint32(expectedShreds) {
					t.Errorf("wrong total for shred %d: got %d, want %d", i, shred.Total, expectedShreds)
				}
				if shred.Height != tt.height {
					t.Errorf("wrong height for shred %d: got %d, want %d", i, shred.Height, tt.height)
				}
				if len(shred.Data) > int(tt.chunkSize) {
					t.Errorf("shred %d larger than chunk size: got %d, want <= %d", i, len(shred.Data), tt.chunkSize)
				}
			}

			// Test reassembly
			reassembled, err := shredder.AssembleBlock(shreds)
			if err != nil {
				t.Fatalf("AssembleBlock() error = %v", err)
			}

			if !bytes.Equal(reassembled, tt.block) {
				t.Error("reassembled block does not match original")
			}
		})
	}

	t.Run("missing shreds", func(t *testing.T) {
		shredder := NewShredder(32)
		block := bytes.Repeat([]byte("a"), 100)

		shreds, _ := shredder.ShredBlock(block, 1)
		incomplete := shreds[:len(shreds)-1] // Remove last shred

		_, err := shredder.AssembleBlock(incomplete)
		if err == nil {
			t.Error("expected error when assembling with missing shreds")
		}
	})

	t.Run("corrupt shreds", func(t *testing.T) {
		shredder := NewShredder(32)
		block := bytes.Repeat([]byte("a"), 100)

		shreds, _ := shredder.ShredBlock(block, 1)
		shreds[0].Data = []byte("corrupted")

		_, err := shredder.AssembleBlock(shreds)
		if err == nil {
			t.Error("expected error when assembling corrupted shreds")
		}
	})
}
