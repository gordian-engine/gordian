package gtshred

import (
	"context"
	"crypto/rand"
	"testing"
	"time"
)

type noopCallback struct{}

func (n *noopCallback) ProcessBlock(height uint64, blockHash []byte, block []byte) error {
	return nil
}

func BenchmarkShredProcessing(b *testing.B) {
	sizes := []struct {
		name      string
		size      int
		chunkSize uint32
	}{
		{"8MB", 8 << 20, 1 << 18},     // 256KB chunks
		{"16MB", 16 << 20, 1 << 19},   // 512KB chunks
		{"32MB", 32 << 20, 1 << 20},   // 1MB chunks
		{"64MB", 64 << 20, 1 << 21},   // 2MB chunks
		{"128MB", 128 << 20, 1 << 22}, // 4MB chunks
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
			p := NewProcessor(&noopCallback{}, time.Minute)
			go p.RunBackgroundCleanup(context.Background())

			// Reset timer before main benchmark loop
			b.ResetTimer()

			for i := 0; i < b.N; i++ {
				// Create shred group with appropriate chunk size
				group, err := NewShredGroup(block, uint64(i), 32, 32, size.chunkSize)
				if err != nil {
					b.Fatal(err)
				}

				// Process all data shreds
				for _, shred := range group.DataShreds {
					if err := p.CollectShred(shred); err != nil {
						b.Fatal(err)
					}
				}

				b.StopTimer()
				// Reset processor state between iterations
				p.groups = make(map[string]*ShredGroupWithTimestamp)
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
	chunkSize := uint32(1 << 20) // 1MB chunks

	_, err := rand.Read(block)
	if err != nil {
		b.Fatal(err)
	}

	for _, pattern := range patterns {
		b.Run(pattern.name, func(b *testing.B) {
			// Create processor
			p := NewProcessor(&noopCallback{}, time.Minute)
			go p.RunBackgroundCleanup(context.Background())

			b.ResetTimer()

			for i := 0; i < b.N; i++ {
				// Create shred group with appropriate chunk size
				group, err := NewShredGroup(block, uint64(i), 32, 32, chunkSize)
				if err != nil {
					b.Fatal(err)
				}

				// Simulate packet loss
				lossCount := int(float64(len(group.DataShreds)) * pattern.lossRate)
				for j := 0; j < lossCount; j++ {
					group.DataShreds[j] = nil
				}

				// Process remaining shreds
				for _, shred := range group.DataShreds {
					if shred != nil {
						if err := p.CollectShred(shred); err != nil {
							b.Fatal(err)
						}
					}
				}

				// Process recovery shreds
				for _, shred := range group.RecoveryShreds {
					if err := p.CollectShred(shred); err != nil {
						b.Fatal(err)
					}
				}

				b.StopTimer()
				p.groups = make(map[string]*ShredGroupWithTimestamp)
				p.completedBlocks = make(map[string]time.Time)
				b.StartTimer()
			}
		})
	}
}
