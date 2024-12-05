package gtshredding

// import (
// 	"bytes"
// 	"crypto/rand"
// 	"math"
// 	mrand "math/rand"
// 	"testing"
// )

// func TestProcessor(t *testing.T) {
// 	t.Run("basic shred and reassemble", func(t *testing.T) {
// 		// Use 32:32 config
// 		var cb = new(testProcessorCallback)
// 		processor, err := NewProcessor(cb))
// 		if err != nil {
// 			t.Fatal(err)
// 		}

// 		// Calculate a valid block size based on configuration
// 		blockSize := int(DefaultChunkSize) * 32 // 32 data shreds
// 		block := make([]byte, blockSize)
// 		if _, err := rand.Read(block); err != nil {
// 			t.Fatal(err)
// 		}

// 		group, err := processor.ProcessBlock(block, 1)
// 		if err != nil {
// 			t.Fatal(err)
// 		}

// 		if group == nil {
// 			t.Fatal("expected non-nil group")
// 		}

// 		if len(group.DataShreds) != 32 {
// 			t.Errorf("expected %d data shreds, got %d", 32, len(group.DataShreds))
// 		}

// 		reassembled, err := processor.ReassembleBlock(group)
// 		if err != nil {
// 			t.Fatal(err)
// 		}

// 		if !bytes.Equal(block, reassembled) {
// 			t.Error("reassembled block does not match original")
// 		}
// 	})

// 	t.Run("block size constraints", func(t *testing.T) {
// 		processor, _ := NewProcessor(DefaultChunkSize, 32, 32)

// 		// Test block exactly at configured max size
// 		maxSize := int(DefaultChunkSize) * 32
// 		block := make([]byte, maxSize)
// 		if _, err := rand.Read(block); err != nil {
// 			t.Fatal(err)
// 		}

// 		if _, err := processor.ProcessBlock(block, 1); err != nil {
// 			t.Errorf("failed to process max size block: %v", err)
// 		}

// 		// Test oversized block
// 		block = make([]byte, maxSize+1)
// 		if _, err := rand.Read(block); err != nil {
// 			t.Fatal(err)
// 		}

// 		if _, err := processor.ProcessBlock(block, 1); err == nil {
// 			t.Error("expected error for oversized block")
// 		}
// 	})

// 	t.Run("packet loss scenarios", func(t *testing.T) {
// 		scenarios := []struct {
// 			name          string
// 			lossRate      float64
// 			shouldRecover bool
// 		}{
// 			{"15% loss", 0.15, true},
// 			{"45% loss", 0.45, true},  // Now recoverable with 32:32 configuration
// 			{"60% loss", 0.60, false}, // Still too many losses to recover from
// 		}

// 		for _, sc := range scenarios {
// 			t.Run(sc.name, func(t *testing.T) {
// 				processor, _ := NewProcessor(DefaultChunkSize, 32, 32)

// 				// Use max configured block size
// 				blockSize := int(DefaultChunkSize) * 32
// 				block := make([]byte, blockSize)
// 				if _, err := rand.Read(block); err != nil {
// 					t.Fatal(err)
// 				}

// 				group, err := processor.ProcessBlock(block, 1)
// 				if err != nil {
// 					t.Fatal(err)
// 				}

// 				// Calculate shreds to drop
// 				totalShreds := len(group.DataShreds) + len(group.RecoveryShreds)
// 				dropCount := int(math.Round(float64(totalShreds) * sc.lossRate))

// 				// Track which shreds we've dropped
// 				dropped := make(map[int]bool)
// 				for i := 0; i < dropCount; i++ {
// 					var idx int
// 					// Keep trying until we find an undropped shred
// 					for {
// 						idx = mrand.Intn(totalShreds)
// 						if !dropped[idx] {
// 							dropped[idx] = true
// 							break
// 						}
// 					}

// 					// Drop the shred
// 					if idx < len(group.DataShreds) {
// 						group.DataShreds[idx] = nil
// 					} else {
// 						group.RecoveryShreds[idx-len(group.DataShreds)] = nil
// 					}
// 				}

// 				// Attempt reassembly
// 				reassembled, err := processor.ReassembleBlock(group)
// 				if sc.shouldRecover {
// 					if err != nil {
// 						t.Errorf("expected recovery to succeed, got: %v", err)
// 					} else if !bytes.Equal(block, reassembled) {
// 						t.Error("reassembled block does not match original")
// 					}
// 				} else if err == nil {
// 					t.Error("expected recovery to fail")
// 				}
// 			})
// 		}
// 	})

// 	t.Run("varying block sizes", func(t *testing.T) {
// 		processor, _ := NewProcessor(DefaultChunkSize, 32, 32)
// 		maxSize := int(DefaultChunkSize) * 32

// 		testSizes := []int{
// 			DefaultChunkSize,              // One chunk
// 			maxSize/2,                     // Half max size
// 			maxSize - DefaultChunkSize,    // One chunk less than max
// 			maxSize,                       // Exact max size
// 		}

// 		for _, size := range testSizes {
// 			block := make([]byte, size)
// 			if _, err := rand.Read(block); err != nil {
// 				t.Fatal(err)
// 			}

// 			group, err := processor.ProcessBlock(block, 1)
// 			if err != nil {
// 				t.Errorf("failed to process block of size %d: %v", size, err)
// 				continue
// 			}

// 			reassembled, err := processor.ReassembleBlock(group)
// 			if err != nil {
// 				t.Errorf("failed to reassemble block of size %d: %v", size, err)
// 				continue
// 			}

// 			if !bytes.Equal(block, reassembled) {
// 				t.Errorf("mismatch for block size %d", size)
// 			}
// 		}
// 	})
// }
