package gtbuilder

import (
	"testing"
)

func BenchmarkTreeBuilder(b *testing.B) {
	sizes := []struct {
		name      string
		valCount  int
		fanout    uint32
	}{
		{"100-Validators-200-Fanout", 100, 200},
		{"500-Validators-200-Fanout", 500, 200},
		{"1000-Validators-200-Fanout", 1000, 200},
		{"2000-Validators-200-Fanout", 2000, 200},
		{"500-Validators-100-Fanout", 500, 100},
		{"500-Validators-400-Fanout", 500, 400},
	}

	for _, size := range sizes {
		// Create indices array
		indices := make([]uint64, size.valCount)
		for i := range indices {
			indices[i] = uint64(i)
		}

		b.Run(size.name, func(b *testing.B) {
			builder := NewTreeBuilder(size.fanout)
			b.ResetTimer()

			for i := 0; i < b.N; i++ {
				// Make copy of indices since they get modified
				indicesCopy := make([]uint64, len(indices))
				copy(indicesCopy, indices)

				tree, err := builder.BuildTree(indicesCopy, uint64(i), 0)
				if err != nil {
					b.Fatal(err)
				}
				if tree == nil {
					b.Fatal("expected non-nil tree")
				}
			}
		})
	}
}

func BenchmarkFindLayerPosition(b *testing.B) {
	sizes := []struct {
		name      string
		valCount  int
		searchPct float64 // percentage through validator set to search
	}{
		{"500-Val-First-10pct", 500, 0.1},
		{"500-Val-Middle", 500, 0.5},
		{"500-Val-Last-10pct", 500, 0.9},
		{"2000-Val-First-10pct", 2000, 0.1},
		{"2000-Val-Middle", 2000, 0.5},
		{"2000-Val-Last-10pct", 2000, 0.9},
	}

	for _, size := range sizes {
		indices := make([]uint64, size.valCount)
		for i := range indices {
			indices[i] = uint64(i)
		}

		builder := NewTreeBuilder(200)
		tree, _ := builder.BuildTree(indices, 1, 0)
		searchIdx := uint64(float64(size.valCount) * size.searchPct)

		b.Run(size.name, func(b *testing.B) {
			b.ResetTimer()

			for i := 0; i < b.N; i++ {
				layer, idx := FindLayerPosition(tree, searchIdx)
				if layer == nil || idx == -1 {
					b.Fatal("failed to find validator")
				}
			}
		})
	}
}

func BenchmarkGetChildren(b *testing.B) {
	sizes := []struct {
		name      string
		valCount  int
		fanout    uint32
	}{
		{"500-Val-200-Fanout-Root", 500, 200},
		{"500-Val-200-Fanout-Mid", 500, 200},
		{"2000-Val-200-Fanout-Root", 2000, 200},
		{"2000-Val-200-Fanout-Mid", 2000, 200},
	}

	for _, size := range sizes {
		indices := make([]uint64, size.valCount)
		for i := range indices {
			indices[i] = uint64(i)
		}

		builder := NewTreeBuilder(size.fanout)
		tree, _ := builder.BuildTree(indices, 1, 0)

		// Test with root validator and middle layer validator
		rootIdx := tree.Root.Validators[0]
		midIdx := tree.Root.Children[0].Validators[0]

		b.Run(size.name+"-Root", func(b *testing.B) {
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				children := GetChildren(tree, rootIdx)
				if len(children) == 0 {
					b.Fatal("expected children")
				}
			}
		})

		b.Run(size.name+"-Mid", func(b *testing.B) {
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				children := GetChildren(tree, midIdx)
				if children == nil {
					b.Fatal("expected valid children slice")
				}
			}
		})
	}
}
