package gtshred

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"testing"
	"time"
)

const (
	DefaultChunkSize      = 64 * 1024    // 64KB
	DefaultDataShreds     = 16           // Number of data shreds
	DefaultRecoveryShreds = 4            // Number of recovery shreds
	TestHeight            = uint64(1000) // Test block height
)

func makeRandomBlock(size int) []byte {
	block := make([]byte, size)
	if _, err := rand.Read(block); err != nil {
		panic(err)
	}
	return block
}

func corrupt(data []byte) {
	if len(data) > 0 {
		// Flip some bits in the middle of the data
		mid := len(data) / 2
		data[mid] ^= 0xFF
		if len(data) > mid+1 {
			data[mid+1] ^= 0xFF
		}
	}
}

type testCase struct {
	name      string
	blockSize int
	corrupt   []int // indices of shreds to corrupt and then mark as missing
	remove    []int // indices of shreds to remove
	expectErr bool
}

type testProcessorCallback struct {
	count     int
	blockHash []byte
	data      []byte
}

func (cb *testProcessorCallback) ProcessBlock(height uint64, blockHash []byte, block []byte) error {
	cb.count++
	cb.data = block
	cb.blockHash = blockHash
	return nil
}

func TestProcessorShredding(t *testing.T) {
	tests := []testCase{
		{
			name:      "even block size",
			blockSize: DefaultChunkSize * DefaultDataShreds,
		},
		{
			name:      "uneven block size",
			blockSize: DefaultChunkSize*DefaultDataShreds - 1000,
		},
		{
			name:      "oversized block",
			blockSize: DefaultChunkSize*DefaultDataShreds + 1,
			expectErr: true,
		},
		{
			name:      "minimum block size",
			blockSize: 1,
		},
		{
			name:      "empty block",
			blockSize: 0,
			expectErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var cb = new(testProcessorCallback)

			p := NewProcessor(cb, time.Minute)
			go p.RunBackgroundCleanup(context.Background())

			block := makeRandomBlock(tc.blockSize)
			group, err := NewShredGroup(block, TestHeight, DefaultDataShreds, DefaultRecoveryShreds, DefaultChunkSize)

			if tc.expectErr {
				if err == nil {
					t.Error("expected error but got none")
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			// Verify all shreds are properly sized
			for i := range group.DataShreds {
				if len(group.DataShreds[i].Data) != int(DefaultChunkSize) {
					t.Errorf("data shred %d wrong size: got %d want %d",
						i, len(group.DataShreds[i].Data), DefaultChunkSize)
				}
			}

			// Collect threshold shreds into processor

			// collect all data shreds except the last 4, so that recovery shreds are necessary to reassemble
			for i := 0; i < DefaultDataShreds-4; i++ {
				p.CollectShred(group.DataShreds[i])
			}

			// collect all recovery shreds
			for i := 0; i < DefaultRecoveryShreds; i++ {
				p.CollectShred(group.RecoveryShreds[i])
			}

			if p.cb.(*testProcessorCallback).count != 1 {
				t.Error("expected ProcessBlock to be called once")
			}

			blockHash := sha256.Sum256(block)

			if !bytes.Equal(blockHash[:], cb.blockHash) {
				t.Errorf("block hash mismatch: got %v want %v", cb.blockHash, group.BlockHash)
			}

			if !bytes.Equal(block, cb.data) {
				t.Errorf("reassembled block doesn't match original: got len %d want len %d",
					len(cb.data), len(block))
			}

		})
	}
}

// func TestProcessorRecovery(t *testing.T) {
// 	tests := []testCase{
// 		{
// 			name:      "recover with missing data shreds",
// 			blockSize: DefaultChunkSize * (DefaultDataShreds - 1),
// 			remove:    []int{0, 1}, // Remove first two data shreds
// 		},
// 		{
// 			name:      "recover with corrupted data shreds",
// 			blockSize: DefaultChunkSize * DefaultDataShreds,
// 			corrupt:   []int{0, 1}, // Corrupt first two data shreds
// 		},
// 		{
// 			name:      "too many missing shreds",
// 			blockSize: DefaultChunkSize * DefaultDataShreds,
// 			remove:    []int{0, 1, 2, 3, 4, 5}, // Remove more than recoverable
// 			expectErr: true,
// 		},
// 		{
// 			name:      "mixed corruption and missing",
// 			blockSize: DefaultChunkSize * DefaultDataShreds,
// 			corrupt:   []int{0},
// 			remove:    []int{1},
// 		},
// 		{
// 			name:      "boundary size block with last shred corrupted",
// 			blockSize: DefaultChunkSize*DefaultDataShreds - 1,
// 			corrupt:   []int{DefaultDataShreds - 1}, // Corrupt last shred
// 		},
// 	}

// 	var cb = new(testProcessorCallback)

// 	for _, tc := range tests {
// 		t.Run(tc.name, func(t *testing.T) {
// 			p, err := NewProcessor(DefaultChunkSize, DefaultDataShreds, DefaultRecoveryShreds)
// 			if err != nil {
// 				t.Fatal(err)
// 			}

// 			block := makeRandomBlock(tc.blockSize)
// 			group, err := p.ProcessBlock(block, TestHeight)
// 			if err != nil {
// 				t.Fatal(err)
// 			}

// 			// Apply corruptions - corrupted shreds are immediately marked as nil
// 			for _, idx := range tc.corrupt {
// 				if idx < len(group.DataShreds) && group.DataShreds[idx] != nil {
// 					// First corrupt the data
// 					corrupt(group.DataShreds[idx].Data)
// 					// Then mark it as missing since it's corrupted
// 					group.DataShreds[idx] = nil
// 				}
// 			}

// 			// Remove shreds
// 			for _, idx := range tc.remove {
// 				if idx < len(group.DataShreds) {
// 					group.DataShreds[idx] = nil
// 				}
// 			}

// 			// Try reassembly
// 			reassembled, err := p.ReassembleBlock(group)

// 			if tc.expectErr {
// 				if err == nil {
// 					t.Error("expected error but got none")
// 				}
// 				return
// 			}

// 			if err != nil {
// 				t.Fatalf("unexpected error: %v", err)
// 			}

// 			if !bytes.Equal(block, reassembled) {
// 				t.Errorf("reassembled block doesn't match original: got len %d want len %d",
// 					len(reassembled), len(block))
// 			}
// 		})
// 	}
// }

// func TestProcessorEdgeCases(t *testing.T) {
// 	t.Run("nil group", func(t *testing.T) {
// 		p, _ := NewProcessor(DefaultChunkSize, DefaultDataShreds, DefaultRecoveryShreds)
// 		_, err := p.ReassembleBlock(nil)
// 		if err == nil {
// 			t.Error("expected error for nil group")
// 		}
// 	})

// 	t.Run("mismatched heights", func(t *testing.T) {
// 		p, _ := NewProcessor(DefaultChunkSize, DefaultDataShreds, DefaultRecoveryShreds)
// 		block := makeRandomBlock(DefaultChunkSize)
// 		group, _ := p.ProcessBlock(block, TestHeight)

// 		// Modify a shred height
// 		group.DataShreds[0].Height = TestHeight + 1

// 		_, err := p.ReassembleBlock(group)
// 		if err == nil {
// 			t.Error("expected error for mismatched heights")
// 		}
// 	})

// 	t.Run("invalid chunk size", func(t *testing.T) {
// 		_, err := NewProcessor(0, DefaultDataShreds, DefaultRecoveryShreds)
// 		if err == nil {
// 			t.Error("expected error for chunk size 0")
// 		}

// 		_, err = NewProcessor(maxChunkSize+1, DefaultDataShreds, DefaultRecoveryShreds)
// 		if err == nil {
// 			t.Error("expected error for chunk size > max")
// 		}
// 	})
// }

// func BenchmarkProcessor(b *testing.B) {
// 	sizes := []int{
// 		1024,             // 1KB
// 		1024 * 1024,      // 1MB
// 		10 * 1024 * 1024, // 10MB
// 	}

// 	for _, size := range sizes {
// 		b.Run(b.Name(), func(b *testing.B) {
// 			p, err := NewProcessor(DefaultChunkSize, DefaultDataShreds, DefaultRecoveryShreds)
// 			if err != nil {
// 				b.Fatal(err)
// 			}

// 			block := makeRandomBlock(size)
// 			b.ResetTimer()

// 			for i := 0; i < b.N; i++ {
// 				group, err := p.ProcessBlock(block, TestHeight)
// 				if err != nil {
// 					b.Fatal(err)
// 				}

// 				_, err = p.ReassembleBlock(group)
// 				if err != nil {
// 					b.Fatal(err)
// 				}
// 			}

// 			b.SetBytes(int64(size))
// 		})
// 	}
// }
