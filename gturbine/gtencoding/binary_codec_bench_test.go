package gtencoding

import (
	"bytes"
	"crypto/rand"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/gordian-engine/gordian/gturbine"
)

// TestShred represents a reusable test shred configuration
type TestShred struct {
	size int
}

var testConfigs = []int{
	64,          // Minimum size
	1024,        // 1KB
	64 * 1024,   // 64KB
	1024 * 1024, // 1MB
}

// BenchmarkBinaryCodec runs comprehensive benchmarks for the binary codec
func BenchmarkBinaryCodec(b *testing.B) {
	for _, cfg := range testConfigs {
		b.Run(benchName("Encode", cfg), func(b *testing.B) {
			benchmarkEncode(b, cfg)
		})
		b.Run(benchName("Decode", cfg), func(b *testing.B) {
			benchmarkDecode(b, cfg)
		})
		b.Run(benchName("RoundTrip", cfg), func(b *testing.B) {
			benchmarkRoundTrip(b, cfg)
		})
	}
}

// BenchmarkBinaryCodecParallel tests parallel encoding/decoding performance
func BenchmarkBinaryCodecParallel(b *testing.B) {
	for _, cfg := range testConfigs {
		b.Run(benchName("EncodeParallel", cfg), func(b *testing.B) {
			benchmarkEncodeParallel(b, cfg)
		})
		b.Run(benchName("DecodeParallel", cfg), func(b *testing.B) {
			benchmarkDecodeParallel(b, cfg)
		})
	}
}

// Helper to create consistent benchmark names
func benchName(op string, size int) string {
	return fmt.Sprintf("%s/%dB", op, size)
}

// Helper to create a test shred
func createTestShred(size int) *gturbine.Shred {
	data := make([]byte, size)
	rand.Read(data)

	return &gturbine.Shred{
		Metadata: &gturbine.ShredMetadata{
			FullDataSize:        size,
			BlockHash:           bytes.Repeat([]byte{0xFF}, blockHashSize), // Fixed pattern for consistent benchmarking
			GroupID:             uuid.New().String(),
			Height:              1,
			TotalDataShreds:     16,
			TotalRecoveryShreds: 4,
		},
		Index: 0,
		Data:  data,
		Hash:  bytes.Repeat([]byte{0xFF}, blockHashSize), // Fixed pattern for consistent benchmarking,
	}
}

func benchmarkEncode(b *testing.B, size int) {
	codec := NewBinaryShardCodec()
	shred := createTestShred(size)

	b.ResetTimer()
	b.SetBytes(int64(size + prefixSize))

	for i := 0; i < b.N; i++ {
		_, err := codec.Encode(shred)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func benchmarkDecode(b *testing.B, size int) {
	codec := NewBinaryShardCodec()
	shred := createTestShred(size)
	encoded, err := codec.Encode(shred)
	if err != nil {
		b.Fatal(err)
	}

	b.ResetTimer()
	b.SetBytes(int64(size + prefixSize))

	for i := 0; i < b.N; i++ {
		_, err := codec.Decode(encoded)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func benchmarkRoundTrip(b *testing.B, size int) {
	codec := NewBinaryShardCodec()
	shred := createTestShred(size)

	b.ResetTimer()
	b.SetBytes(int64(size + prefixSize))

	for i := 0; i < b.N; i++ {
		encoded, err := codec.Encode(shred)
		if err != nil {
			b.Fatal(err)
		}
		_, err = codec.Decode(encoded)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func benchmarkEncodeParallel(b *testing.B, size int) {
	codec := NewBinaryShardCodec()
	shred := createTestShred(size)

	b.ResetTimer()
	b.SetBytes(int64(size + prefixSize))

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, err := codec.Encode(shred)
			if err != nil {
				b.Fatal(err)
			}
		}
	})
}

func benchmarkDecodeParallel(b *testing.B, size int) {
	codec := NewBinaryShardCodec()
	shred := createTestShred(size)
	encoded, err := codec.Encode(shred)
	if err != nil {
		b.Fatal(err)
	}

	b.ResetTimer()
	b.SetBytes(int64(size + prefixSize))

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, err := codec.Decode(encoded)
			if err != nil {
				b.Fatal(err)
			}
		}
	})
}
