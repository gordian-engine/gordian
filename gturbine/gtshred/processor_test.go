package gtshred

import (
	"context"
	"testing"
	"time"
)

func TestProcessorMemoryCleanup(t *testing.T) {
	// Create processor with short cleanup interval for testing
	var cb = new(testProcessorCallback)
	cleanupInterval := 100 * time.Millisecond
	p := NewProcessor(cb, cleanupInterval)
	go p.RunBackgroundCleanup(context.Background())

	// Create a test block and shred group
	block := []byte("test block data")
	group, err := NewShredGroup(block, 1, 2, 1, 100)
	if err != nil {
		t.Fatal(err)
	}

	// Process some shreds from the group to mark it complete
	for i := 0; i < len(group.DataShreds); i++ {
		err := p.CollectShred(group.DataShreds[i])
		if err != nil {
			t.Fatal(err)
		}
	}

	// Verify block is marked as completed
	if _, exists := p.completedBlocks[string(group.BlockHash)]; !exists {
		t.Error("block should be marked as completed")
	}

	// Try to process another shred from same block
	err = p.CollectShred(group.RecoveryShreds[0])
	if err != nil {
		t.Fatal(err)
	}

	// Verify no new group was created for this block
	groupCount := len(p.groups)
	if groupCount > 1 {
		t.Errorf("expected at most 1 group, got %d", groupCount)
	}

	// Wait for cleanup
	time.Sleep(cleanupInterval * 4)

	// Verify completed block was cleaned up
	p.completedBlocksMu.RLock()
	defer p.completedBlocksMu.RUnlock()
	if _, exists := p.completedBlocks[string(group.BlockHash)]; exists {
		t.Error("completed block should have been cleaned up")
	}
}

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
