package erasure

import (
	"bytes"
	"crypto/rand"
	"testing"
)

func TestEncoder(t *testing.T) {
	t.Run("basic encode decode", func(t *testing.T) {
		enc, err := NewEncoder(4, 2)
		if err != nil {
			t.Fatal(err)
		}

		data := [][]byte{
			[]byte("shard 1"),
			[]byte("shard 2"),
			[]byte("shard 3"),
			[]byte("shard 4"),
		}

		parity, err := enc.Encode(data)
		if err != nil {
			t.Fatal(err)
		}

		if len(parity) != 2 {
			t.Fatalf("expected 2 parity shards, got %d", len(parity))
		}

		// Combine data and parity
		all := append(data, parity...)
		if ok, err := enc.Verify(all); err != nil || !ok {
			t.Error("verification failed for valid shards")
		}
	})

	t.Run("recovery scenarios", func(t *testing.T) {
		enc, _ := NewEncoder(4, 2)
		original := [][]byte{
			[]byte("shard 1"),
			[]byte("shard 2"),
			[]byte("shard 3"),
			[]byte("shard 4"),
		}
		parity, _ := enc.Encode(original)
		all := append(original, parity...)

		tests := []struct {
			name      string
			corrupt   []int
			wantError bool
		}{
			{"one data shard", []int{0}, false},
			{"two data shards", []int{0, 1}, false},
			{"one parity shard", []int{4}, false},
			{"mixed shards", []int{1, 4}, false},
			{"three shards", []int{0, 1, 2}, true},
			{"all parity", []int{4, 5}, false},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				corrupted := make([][]byte, len(all))
				copy(corrupted, all)

				for _, idx := range tt.corrupt {
					corrupted[idx] = nil
				}

				err := enc.Reconstruct(corrupted)
				if (err != nil) != tt.wantError {
					t.Errorf("Reconstruct() error = %v, wantError %v", err, tt.wantError)
					return
				}

				if !tt.wantError {
					for i, shard := range corrupted {
						if i < len(original) && !bytes.Equal(shard, original[i]) {
							t.Errorf("shard %d not properly reconstructed", i)
						}
					}
				}
			})
		}
	})

	t.Run("large data handling", func(t *testing.T) {
		enc, _ := NewEncoder(6, 3)
		shardSize := 1024 * 1024 // 1MB per shard

		original := make([][]byte, 6)
		for i := range original {
			original[i] = make([]byte, shardSize)
			rand.Read(original[i])
		}

		parity, err := enc.Encode(original)
		if err != nil {
			t.Fatal(err)
		}

		all := append(original, parity...)

		// Corrupt 2 data shards and 1 parity shard
		all[0] = nil
		all[2] = nil
		all[6] = nil

		if err := enc.Reconstruct(all); err != nil {
			t.Fatal(err)
		}

		for i := range original {
			if !bytes.Equal(all[i], original[i]) {
				t.Errorf("shard %d not properly reconstructed", i)
			}
		}
	})

	t.Run("input validation", func(t *testing.T) {
		tests := []struct {
			name        string
			dataShards  int
			parityShards int
			wantError   bool
		}{
			{"valid config", 4, 2, false},
			{"zero data shards", 0, 2, true},
			{"zero parity shards", 4, 0, true},
			{"negative data shards", -1, 2, true},
			{"negative parity shards", 4, -1, true},
			{"too many total shards", 128, 128, true},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				_, err := NewEncoder(tt.dataShards, tt.parityShards)
				if (err != nil) != tt.wantError {
					t.Errorf("NewEncoder() error = %v, wantError %v", err, tt.wantError)
				}
			})
		}
	})
}
