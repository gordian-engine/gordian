package gtencoding

import (
	"crypto/rand"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/gordian-engine/gordian/gturbine"
)

// Benchmark sizes - all multiples of 64 bytes, ranging from 8MB to 128MB
type benchSize struct {
	size int
	name string
}

var benchSizes = []benchSize{
	{name: "128MB", size: 128 * 1024 * 1024},
	{name: "64MB", size: 64 * 1024 * 1024},
	{name: "32MB", size: 32 * 1024 * 1024},
	{name: "16MB", size: 16 * 1024 * 1024},
	{name: "8MB", size: 8 * 1024 * 1024},
}

// BenchmarkEncode tests binary encoding performance at various block sizes
func BenchmarkEncode(b *testing.B) {
	for _, size := range benchSizes {
		b.Run(fmt.Sprintf("%s", size.name), func(b *testing.B) {
			data := make([]byte, size.size)
			rand.Read(data)

			shred := &gturbine.Shred{
				Type:                gturbine.DataShred,
				FullDataSize:        size.size,
				BlockHash:           make([]byte, 32),
				GroupID:             uuid.New().String(),
				Height:              1,
				Index:               0,
				TotalDataShreds:     16,
				TotalRecoveryShreds: 4,
				Data:                data,
			}

			codec := NewBinaryShardCodec()

			b.ResetTimer()
			b.SetBytes(int64(size.size))

			for i := 0; i < b.N; i++ {
				_, err := codec.Encode(shred)
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkDecode tests binary decoding performance at various block sizes
func BenchmarkDecode(b *testing.B) {
	for _, size := range benchSizes {
		b.Run(fmt.Sprintf("%s", size.name), func(b *testing.B) {
			data := make([]byte, size.size)
			rand.Read(data)

			shred := &gturbine.Shred{
				Type:                gturbine.DataShred,
				FullDataSize:        size.size,
				BlockHash:           make([]byte, 32),
				GroupID:             uuid.New().String(),
				Height:              1,
				Index:               0,
				TotalDataShreds:     16,
				TotalRecoveryShreds: 4,
				Data:                data,
			}

			codec := NewBinaryShardCodec()
			encoded, _ := codec.Encode(shred)

			b.ResetTimer()
			b.SetBytes(int64(size.size))

			for i := 0; i < b.N; i++ {
				_, err := codec.Decode(encoded)
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkErasureEncoding tests Reed-Solomon encoding with different configurations
func BenchmarkErasureEncoding(b *testing.B) {
	configs := []struct {
		data     int
		recovery int
	}{
		{4, 2},  // 33% overhead
		{8, 4},  // 33% overhead
		{16, 4}, // 20% overhead
		{32, 8}, // 20% overhead
	}

	for _, size := range benchSizes {
		for _, cfg := range configs {
			shardSize := size.size / cfg.data
			name := fmt.Sprintf("%s-%dd-%dr", size.name, cfg.data, cfg.recovery)

			b.Run(name, func(b *testing.B) {
				enc, err := NewEncoder(cfg.data, cfg.recovery)
				if err != nil {
					b.Fatal(err)
				}

				shreds := make([][]byte, cfg.data)
				for i := range shreds {
					shreds[i] = make([]byte, shardSize)
					rand.Read(shreds[i])
				}

				b.ResetTimer()
				b.SetBytes(int64(size.size))

				for i := 0; i < b.N; i++ {
					_, err := enc.GenerateRecoveryShreds(shreds)
					if err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

// BenchmarkErasureReconstruction tests Reed-Solomon reconstruction with different configurations
func BenchmarkErasureReconstruction(b *testing.B) {
	configs := []struct {
		data     int
		recovery int
	}{
		{4, 2},  // 33% overhead
		{8, 4},  // 33% overhead
		{16, 4}, // 20% overhead
		{32, 8}, // 20% overhead
	}

	for _, size := range benchSizes {
		for _, cfg := range configs {
			shardSize := size.size / cfg.data
			name := fmt.Sprintf("%s-%dd-%dr", size.name, cfg.data, cfg.recovery)

			b.Run(name, func(b *testing.B) {
				enc, err := NewEncoder(cfg.data, cfg.recovery)
				if err != nil {
					b.Fatal(err)
				}

				// Generate test data
				shreds := make([][]byte, cfg.data)
				for i := range shreds {
					shreds[i] = make([]byte, shardSize)
					rand.Read(shreds[i])
				}

				// Generate recovery shreds
				recoveryShreds, err := enc.GenerateRecoveryShreds(shreds)
				if err != nil {
					b.Fatal(err)
				}

				// Combine all shreds
				allShreds := append(shreds, recoveryShreds...)

				// Simulate worst case - lose maximum recoverable shards
				for i := 0; i < cfg.recovery; i++ {
					allShreds[i] = nil
				}

				b.ResetTimer()
				b.SetBytes(int64(size.size))

				for i := 0; i < b.N; i++ {
					// Make a copy since Reconstruct modifies the slice
					testShreds := make([][]byte, len(allShreds))
					copy(testShreds, allShreds)

					err := enc.Reconstruct(testShreds)
					if err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

// BenchmarkErasureVerification tests Reed-Solomon verification with different configurations
func BenchmarkErasureVerification(b *testing.B) {
	configs := []struct {
		data     int
		recovery int
	}{
		{4, 2},  // 33% overhead
		{8, 4},  // 33% overhead
		{16, 4}, // 20% overhead
		{32, 8}, // 20% overhead
	}

	for _, size := range benchSizes {
		for _, cfg := range configs {
			shardSize := size.size / cfg.data
			name := fmt.Sprintf("%s-%dd-%dr", size.name, cfg.data, cfg.recovery)

			b.Run(name, func(b *testing.B) {
				enc, err := NewEncoder(cfg.data, cfg.recovery)
				if err != nil {
					b.Fatal(err)
				}

				// Generate test data
				shreds := make([][]byte, cfg.data)
				for i := range shreds {
					shreds[i] = make([]byte, shardSize)
					rand.Read(shreds[i])
				}

				// Generate recovery shreds
				recoveryShreds, err := enc.GenerateRecoveryShreds(shreds)
				if err != nil {
					b.Fatal(err)
				}

				// Combine all shreds
				allShreds := append(shreds, recoveryShreds...)

				b.ResetTimer()
				b.SetBytes(int64(size.size))

				for i := 0; i < b.N; i++ {
					ok, err := enc.Verify(allShreds)
					if err != nil {
						b.Fatal(err)
					}
					if !ok {
						b.Fatal("verification failed")
					}
				}
			})
		}
	}
}

// BenchmarkFullPipeline tests the complete encoding process
func BenchmarkFullPipeline(b *testing.B) {
	configs := []struct {
		data     int
		recovery int
	}{
		{16, 4}, // Typical configuration
	}

	for _, size := range benchSizes {
		for _, cfg := range configs {
			name := fmt.Sprintf("%s-%dd-%dr", size.name, cfg.data, cfg.recovery)

			b.Run(name, func(b *testing.B) {
				binaryCodec := NewBinaryShardCodec()
				erasureEnc, err := NewEncoder(cfg.data, cfg.recovery)
				if err != nil {
					b.Fatal(err)
				}

				data := make([]byte, size.size)
				rand.Read(data)

				b.ResetTimer()
				b.SetBytes(int64(size.size))

				for i := 0; i < b.N; i++ {
					shreds := make([][]byte, cfg.data)
					shredSize := size.size / cfg.data

					for j := 0; j < cfg.data; j++ {
						shred := &gturbine.Shred{
							Type:                gturbine.DataShred,
							FullDataSize:        size.size,
							BlockHash:           make([]byte, 32),
							GroupID:             uuid.New().String(),
							Height:              1,
							Index:               j,
							TotalDataShreds:     cfg.data,
							TotalRecoveryShreds: cfg.recovery,
							Data:                data[j*shredSize : (j+1)*shredSize],
						}

						encoded, err := binaryCodec.Encode(shred)
						if err != nil {
							b.Fatal(err)
						}
						shreds[j] = encoded
					}

					recoveryShreds, err := erasureEnc.GenerateRecoveryShreds(shreds)
					if err != nil {
						b.Fatal(err)
					}

					allShreds := append(shreds, recoveryShreds...)
					ok, err := erasureEnc.Verify(allShreds)
					if err != nil {
						b.Fatal(err)
					}
					if !ok {
						b.Fatal("verification failed")
					}
				}
			})
		}
	}
}
