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
				Metadata: &gturbine.ShredMetadata{
					FullDataSize:        size.size,
					BlockHash:           make([]byte, 32),
					GroupID:             uuid.New().String(),
					Height:              1,
					TotalDataShreds:     16,
					TotalRecoveryShreds: 4,
				},
				Index: 0,
				Data:  data,
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
				Metadata: &gturbine.ShredMetadata{
					FullDataSize:        size.size,
					BlockHash:           make([]byte, 32),
					GroupID:             uuid.New().String(),
					Height:              1,
					TotalDataShreds:     16,
					TotalRecoveryShreds: 4,
				},
				Index: 0,
				Data:  data,
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
