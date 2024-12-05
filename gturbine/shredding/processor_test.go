package shredding

import (
	"bytes"
	"crypto/rand"
	"math"
	mrand "math/rand"
	"testing"
)

func TestProcessor(t *testing.T) {
	t.Run("basic shred and reassemble", func(t *testing.T) {
		processor, err := NewProcessor(DefaultChunkSize, DefaultDataShreds, DefaultRecoveryShreds)
		if err != nil {
			t.Fatal(err)
		}

		block := make([]byte, DefaultChunkSize*4) // 256KB test block
		rand.Read(block)
		group, err := processor.ProcessBlock(block, 1)
		if err != nil {
			t.Fatal(err)
		}

		if group == nil {
			t.Fatal("expected non-nil group")
		}

		if len(group.DataShreds) != DefaultDataShreds {
			t.Errorf("expected %d data shreds, got %d", DefaultDataShreds, len(group.DataShreds))
		}

		reassembled, err := processor.ReassembleBlock(group)
		if err != nil {
			t.Fatal(err)
		}

		if !bytes.Equal(block, reassembled) {
			t.Error("reassembled block does not match original")
		}
	})

	t.Run("packet loss scenarios", func(t *testing.T) {
		scenarios := []struct {
			name          string
			lossRate      float64
			shouldRecover bool
		}{
			{"15% loss", 0.15, true},
			{"45% loss", 0.45, true},
			{"60% loss", 0.60, false},
		}

		for _, sc := range scenarios {
			t.Run(sc.name, func(t *testing.T) {
				processor, _ := NewProcessor(DefaultChunkSize, DefaultDataShreds, DefaultRecoveryShreds)

				// Create 4MB block
				block := make([]byte, DefaultChunkSize*32)
				rand.Read(block)

				group, err := processor.ProcessBlock(block, 1)
				if err != nil {
					t.Fatal(err)
				}

				// Calculate shreds to drop
				totalShreds := len(group.DataShreds) + len(group.RecoveryShreds)
				dropCount := int(math.Round(float64(totalShreds) * sc.lossRate))

				// Track which shreds we've dropped
				dropped := make(map[int]bool)
				for i := 0; i < dropCount; i++ {
					var idx int
					// Keep trying until we find an undropped shred
					for {
						idx = mrand.Intn(totalShreds)
						if !dropped[idx] {
							dropped[idx] = true
							break
						}
					}

					// Drop the shred
					if idx < len(group.DataShreds) {
						group.DataShreds[idx] = nil
					} else {
						group.RecoveryShreds[idx-len(group.DataShreds)] = nil
					}
				}

				// Attempt reassembly
				reassembled, err := processor.ReassembleBlock(group)
				if sc.shouldRecover {
					if err != nil {
						t.Errorf("expected recovery to succeed, got: %v", err)
					} else if !bytes.Equal(block, reassembled) {
						t.Error("reassembled block does not match original")
					}
				} else if err == nil {
					t.Error("expected recovery to fail")
				}
			})
		}
	})
}
