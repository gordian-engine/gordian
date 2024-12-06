package gtencoding

import (
	"bytes"
	"crypto/rand"
	"fmt"
	mrand "math/rand"
	"testing"
)

const (
	// NOTE: blocksize needs to be a multiple of 64 for reed solomon to work
	blockSize = 128 * 1024 * 1024 // 128MB blocks same as solana
	numTests  = 5                 // Number of iterations for randomized tests
)

func TestEncoderRealWorld(t *testing.T) {
	t.Run("solana-like configuration", func(t *testing.T) {
		enc, err := NewEncoder(32, 32)
		if err != nil {
			t.Fatal(err)
		}

		shredSize := blockSize / 32
		dataShreds := make([][]byte, 32)
		for i := range dataShreds {
			dataShreds[i] = make([]byte, shredSize)
			if _, err := rand.Read(dataShreds[i]); err != nil {
				t.Fatal(err)
			}
		}

		recoveryShreds, err := enc.GenerateRecoveryShreds(dataShreds)
		if err != nil {
			t.Fatal(err)
		}

		if len(recoveryShreds) != 32 {
			t.Fatalf("expected 32 recovery shreds, got %d", len(recoveryShreds))
		}

		allShreds := append(dataShreds, recoveryShreds...)

		scenarios := []struct {
			name          string
			numDataLost   int
			numParityLost int
			shouldRecover bool
		}{
			{"lose 16 data shreds", 16, 0, true},
			{"lose 16 data and 16 parity shreds", 16, 16, true},
			{"lose 31 data shreds", 31, 0, true},
			{"lose all parity shreds", 0, 32, true},
			{"lose 32 data shreds and 1 parity shred", 32, 1, false},
			{"lose 31 data and 2 parity shreds", 31, 2, false},
		}

		for _, sc := range scenarios {
			t.Run(sc.name, func(t *testing.T) {
				testShreds := make([][]byte, len(allShreds))
				copy(testShreds, allShreds)

				// Remove data shreds
				for i := 0; i < sc.numDataLost; i++ {
					testShreds[i] = nil
				}

				// Remove parity shreds
				for i := 0; i < sc.numParityLost; i++ {
					testShreds[32+i] = nil
				}

				err := enc.Reconstruct(testShreds)
				if sc.shouldRecover {
					if err != nil {
						t.Errorf("failed to reconstruct when it should: %v", err)
						return
					}
					// Verify reconstruction
					for i := range dataShreds {
						if !bytes.Equal(testShreds[i], dataShreds[i]) {
							t.Errorf("shard %d not properly reconstructed", i)
						}
					}
				} else if err == nil {
					t.Error("reconstruction succeeded when it should have failed")
				}
			})
		}
	})

	t.Run("random failure patterns", func(t *testing.T) {
		enc, _ := NewEncoder(32, 32)
		shredSize := blockSize / 32

		for i := 0; i < numTests; i++ {
			dataShreds := make([][]byte, 32)
			for j := range dataShreds {
				dataShreds[j] = make([]byte, shredSize)
				if _, err := rand.Read(dataShreds[j]); err != nil {
					t.Fatal(err)
				}
			}

			recoveryShreds, err := enc.GenerateRecoveryShreds(dataShreds)
			if err != nil {
				t.Fatal(err)
			}

			allShreds := append(dataShreds, recoveryShreds...)
			testShreds := make([][]byte, len(allShreds))
			copy(testShreds, allShreds)

			numToRemove := mrand.Intn(32)
			removedIndices := make(map[int]bool)
			for j := 0; j < numToRemove; j++ {
				for {
					idx := mrand.Intn(len(testShreds))
					if !removedIndices[idx] {
						testShreds[idx] = nil
						removedIndices[idx] = true
						break
					}
				}
			}

			err = enc.Reconstruct(testShreds)
			if numToRemove >= 32 {
				if err == nil {
					t.Errorf("test %d: reconstruction succeeded with %d shreds removed", i, numToRemove)
				}
			} else {
				if err != nil {
					t.Errorf("test %d: failed to reconstruct with %d shreds removed: %v", i, numToRemove, err)
					continue
				}

				for j := range dataShreds {
					if !bytes.Equal(testShreds[j], dataShreds[j]) {
						t.Errorf("test %d: shard %d not properly reconstructed", i, j)
					}
				}
			}
		}
	})

	t.Run("performance benchmarks", func(t *testing.T) {
		if testing.Short() {
			t.Skip("skipping performance test in short mode")
		}

		enc, _ := NewEncoder(32, 32)
		shredSize := blockSize / 32
		dataShreds := make([][]byte, 32)
		for i := range dataShreds {
			dataShreds[i] = make([]byte, shredSize)
			rand.Read(dataShreds[i])
		}

		recoveryShreds, err := enc.GenerateRecoveryShreds(dataShreds)
		if err != nil {
			t.Fatal(err)
		}

		allShreds := append(dataShreds, recoveryShreds...)
		lostCounts := []int{8, 16, 24, 31}

		for _, count := range lostCounts {
			t.Run(fmt.Sprintf("reconstruct_%d_lost", count), func(t *testing.T) {
				testShreds := make([][]byte, len(allShreds))
				copy(testShreds, allShreds)

				for i := 0; i < count; i++ {
					testShreds[i] = nil
				}

				if err := enc.Reconstruct(testShreds); err != nil {
					t.Fatal(err)
				}

				for i := range dataShreds {
					if !bytes.Equal(testShreds[i], dataShreds[i]) {
						t.Errorf("shard %d not properly reconstructed", i)
					}
				}
			})
		}
	})
}
