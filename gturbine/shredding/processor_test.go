package shredding

import (
	"bytes"
	"crypto/rand"
	"testing"
)

func TestProcessor(t *testing.T) {
	t.Run("basic process and reassemble", func(t *testing.T) {
		processor, err := NewProcessor(32, 4, 2)
		if err != nil {
			t.Fatal(err)
		}

		block := []byte("test block data")
		height := uint64(100)

		group, err := processor.ProcessBlock(block, height)
		if err != nil {
			t.Fatal(err)
		}

		if len(group.DataShreds) != 4 {
			t.Errorf("expected 4 data shreds, got %d", len(group.DataShreds))
		}
		if len(group.RecoveryShreds) != 2 {
			t.Errorf("expected 2 recovery shreds, got %d", len(group.RecoveryShreds))
		}

		reassembled, err := processor.ReassembleBlock(group)
		if err != nil {
			t.Fatal(err)
		}

		if !bytes.Equal(block, reassembled) {
			t.Error("reassembled block does not match original")
		}
	})

	t.Run("recovery from missing shreds", func(t *testing.T) {
		processor, err := NewProcessor(32, 4, 2)
		if err != nil {
			t.Fatal(err)
		}

		block := []byte("test block that needs recovery")
		group, err := processor.ProcessBlock(block, 1)
		if err != nil {
			t.Fatal(err)
		}

		// Remove some data shreds
		group.DataShreds[0] = nil
		group.DataShreds[1] = nil

		reassembled, err := processor.ReassembleBlock(group)
		if err != nil {
			t.Fatal(err)
		}

		if !bytes.Equal(block, reassembled) {
			t.Error("reassembled block does not match original after recovery")
		}
	})

	t.Run("large block processing", func(t *testing.T) {
		processor, err := NewProcessor(64*1024, 4, 2) // 64KB chunks
		if err != nil {
			t.Fatal(err)
		}

		block := make([]byte, 256*1024) // 256KB block
		if _, err := rand.Read(block); err != nil {
			t.Fatal(err)
		}

		group, err := processor.ProcessBlock(block, 1)
		if err != nil {
			t.Fatal(err)
		}

		reassembled, err := processor.ReassembleBlock(group)
		if err != nil {
			t.Fatal(err)
		}

		if !bytes.Equal(block, reassembled) {
			t.Error("reassembled large block does not match original")
		}
	})

	t.Run("too many missing shreds", func(t *testing.T) {
		processor, err := NewProcessor(32, 4, 2)
		if err != nil {
			t.Fatal(err)
		}

		block := []byte("test block")
		group, err := processor.ProcessBlock(block, 1)
		if err != nil {
			t.Fatal(err)
		}

		// Remove too many shreds to recover
		group.DataShreds[0] = nil
		group.DataShreds[1] = nil
		group.DataShreds[2] = nil

		_, err = processor.ReassembleBlock(group)
		if err == nil {
			t.Error("expected error with too many missing shreds")
		}
	})
}
