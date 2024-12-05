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

		dataShreds := [][]byte{
			[]byte("data shard 1"),
			[]byte("data shard 2"),
			[]byte("data shard 3"),
			[]byte("data shard 4"),
		}

		recoveryShreds, err := enc.GenerateRecoveryShreds(dataShreds)
		if err != nil {
			t.Fatal(err)
		}

		if len(recoveryShreds) != 2 {
			t.Fatalf("expected 2 recovery shreds, got %d", len(recoveryShreds))
		}

		allShreds := append(dataShreds, recoveryShreds...)
		if ok, err := enc.Verify(allShreds); err != nil || !ok {
			t.Error("verification failed for valid shreds")
		}
	})

	t.Run("recovery scenarios", func(t *testing.T) {
		enc, _ := NewEncoder(4, 2)
		dataShreds := [][]byte{
			[]byte("data shard 1"),
			[]byte("data shard 2"),
			[]byte("data shard 3"),
			[]byte("data shard 4"),
		}
		recoveryShreds, _ := enc.GenerateRecoveryShreds(dataShreds)
		allShreds := append(dataShreds, recoveryShreds...)

		tests := []struct {
			name      string
			corrupt   []int
			wantError bool
		}{
			{"lose one data shard", []int{0}, false},
			{"lose two data shreds", []int{0, 1}, false},
			{"lose all recovery shreds", []int{4, 5}, false},
			{"lose three shreds", []int{0, 1, 2}, true},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				corrupted := make([][]byte, len(allShreds))
				copy(corrupted, allShreds)

				for _, idx := range tt.corrupt {
					corrupted[idx] = nil
				}

				err := enc.Reconstruct(corrupted)
				if (err != nil) != tt.wantError {
					t.Errorf("Reconstruct() error = %v, wantError %v", err, tt.wantError)
					return
				}

				if !tt.wantError {
					for i := range dataShreds {
						if !bytes.Equal(corrupted[i], dataShreds[i]) {
							t.Errorf("shard %d not properly reconstructed", i)
						}
					}
				}
			})
		}
	})

	t.Run("large shreds", func(t *testing.T) {
		enc, _ := NewEncoder(6, 3)
		shredSize := 1024 * 1024 // 1MB shreds

		dataShreds := make([][]byte, 6)
		for i := range dataShreds {
			dataShreds[i] = make([]byte, shredSize)
			rand.Read(dataShreds[i])
		}

		recoveryShreds, err := enc.GenerateRecoveryShreds(dataShreds)
		if err != nil {
			t.Fatal(err)
		}

		allShreds := append(dataShreds, recoveryShreds...)

		// Remove 2 data shreds and 1 recovery shred
		allShreds[0] = nil
		allShreds[2] = nil
		allShreds[6] = nil

		if err := enc.Reconstruct(allShreds); err != nil {
			t.Fatal(err)
		}

		for i := range dataShreds {
			if !bytes.Equal(allShreds[i], dataShreds[i]) {
				t.Errorf("shard %d not properly reconstructed", i)
			}
		}
	})
}
