package gtshred

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"testing"
	"time"
)

type noopCallback struct{}

func (n *noopCallback) ProcessBlock(height uint64, blockHash []byte, block []byte) error {
	return nil
}

func BenchmarkShredProcessing(b *testing.B) {
	sizes := []struct {
		name string
		size int
	}{
		{"8MB", 8 << 20},     // 256KB chunks
		{"16MB", 16 << 20},   // 512KB chunks
		{"32MB", 32 << 20},   // 1MB chunks
		{"64MB", 64 << 20},   // 2MB chunks
		{"128MB", 128 << 20}, // 4MB chunks
	}

	for _, size := range sizes {
		b.Run(size.name, func(b *testing.B) {
			// Generate random block data
			block := make([]byte, size.size)
			_, err := rand.Read(block)
			if err != nil {
				b.Fatal(err)
			}

			// Create processor with noop callback
			hasher := sha256.New
			p := NewProcessor(&noopCallback{}, hasher, hasher, time.Minute)
			go p.RunBackgroundCleanup(context.Background())

			// Reset timer before main benchmark loop
			b.ResetTimer()

			for i := 0; i < b.N; i++ {
				// Create shred group with appropriate chunk size
				group, err := ShredBlock(block, hasher, uint64(i), 32, 32)
				if err != nil {
					b.Fatal(err)
				}

				// Process all data shreds
				for _, shred := range group.Shreds {
					if err := p.CollectShred(shred); err != nil {
						b.Fatal(err)
					}
				}

				b.StopTimer()
				// Reset processor state between iterations
				p.groups = make(map[string]*ReconstructorWithTimestamp)
				p.completedBlocks = make(map[string]time.Time)
				b.StartTimer()
			}
		})
	}
}

func BenchmarkShredReconstruction(b *testing.B) {
	// Test reconstruction with different loss patterns
	patterns := []struct {
		name     string
		lossRate float64
	}{
		{"10% Loss", 0.10},
		{"25% Loss", 0.25},
		{"40% Loss", 0.40},
	}

	// Use 32MB block with 1MB chunks
	block := make([]byte, 32<<20)

	_, err := rand.Read(block)
	if err != nil {
		b.Fatal(err)
	}

	for _, pattern := range patterns {
		b.Run(pattern.name, func(b *testing.B) {
			// Create processor
			hasher := sha256.New
			p := NewProcessor(&noopCallback{}, hasher, hasher, time.Minute)
			go p.RunBackgroundCleanup(context.Background())

			b.ResetTimer()

			for i := 0; i < b.N; i++ {
				// Create shred group with appropriate chunk size
				group, err := ShredBlock(block, hasher, uint64(i), 32, 32)
				if err != nil {
					b.Fatal(err)
				}

				// Simulate packet loss
				lossCount := int(float64(len(group.Shreds)) * pattern.lossRate)
				for j := 0; j < lossCount; j++ {
					group.Shreds[j] = nil
				}

				// Process remaining shreds
				for _, shred := range group.Shreds {
					if shred != nil {
						if err := p.CollectShred(shred); err != nil {
							b.Fatal(err)
						}
					}
				}

				b.StopTimer()
				p.groups = make(map[string]*ReconstructorWithTimestamp)
				p.completedBlocks = make(map[string]time.Time)
				b.StartTimer()
			}
		})
	}
}
