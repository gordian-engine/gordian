package shredding

import (
	"bytes"
	"crypto/rand"
	"testing"
)

func TestProcessorShredding(t *testing.T) {
	t.Run("even blocks", func(t *testing.T) {
		p, err := NewProcessor(DefaultChunkSize, DefaultDataShreds, DefaultRecoveryShreds)
		if err != nil {
			t.Fatal(err)
		}

		block := make([]byte, DefaultChunkSize*DefaultDataShreds)
		if _, err := rand.Read(block); err != nil {
			t.Fatal(err)
		}

		group, err := p.ProcessBlock(block, 1)
		if err != nil {
			t.Fatal(err)
		}

		for i := 0; i < DefaultDataShreds; i++ {
			if len(group.DataShreds[i].Data) != int(DefaultChunkSize) {
				t.Errorf("data shred %d incorrect size: got %d want %d", i, len(group.DataShreds[i].Data), DefaultChunkSize)
			}
		}

		reassembled, err := p.ReassembleBlock(group)
		if err != nil {
			t.Fatal(err)
		}

		if !bytes.Equal(block, reassembled) {
			t.Error("reassembled block doesn't match original")
		}
	})

	t.Run("uneven blocks", func(t *testing.T) {
		p, err := NewProcessor(DefaultChunkSize, DefaultDataShreds, DefaultRecoveryShreds)
		if err != nil {
			t.Fatal(err)
		}

		block := make([]byte, DefaultChunkSize*DefaultDataShreds-1000)
		if _, err := rand.Read(block); err != nil {
			t.Fatal(err)
		}

		group, err := p.ProcessBlock(block, 1)
		if err != nil {
			t.Fatal(err)
		}

		for i := 0; i < DefaultDataShreds-1; i++ {
			if len(group.DataShreds[i].Data) != int(DefaultChunkSize) {
				t.Errorf("data shred %d incorrect size: got %d want %d", i, len(group.DataShreds[i].Data), DefaultChunkSize)
			}
		}

		reassembled, err := p.ReassembleBlock(group)
		if err != nil {
			t.Fatal(err)
		}

		if !bytes.Equal(block, reassembled) {
			t.Error("reassembled block doesn't match original")
		}
	})

	t.Run("oversized block", func(t *testing.T) {
		p, err := NewProcessor(DefaultChunkSize, DefaultDataShreds, DefaultRecoveryShreds)
		if err != nil {
			t.Fatal(err)
		}

		block := make([]byte, DefaultChunkSize*DefaultDataShreds+1)
		if _, err := rand.Read(block); err != nil {
			t.Fatal(err)
		}

		if _, err := p.ProcessBlock(block, 1); err == nil {
			t.Error("expected error for oversized block")
		}
	})

	t.Run("zero size block", func(t *testing.T) {
		p, err := NewProcessor(DefaultChunkSize, DefaultDataShreds, DefaultRecoveryShreds)
		if err != nil {
			t.Fatal(err)
		}

		block := make([]byte, 0)
		group, err := p.ProcessBlock(block, 1)
		if err != nil {
			t.Fatal(err)
		}

		for i := 0; i < DefaultDataShreds; i++ {
			if len(group.DataShreds[i].Data) == 0 {
				t.Errorf("data shred %d has zero size", i)
			}
		}

		reassembled, err := p.ReassembleBlock(group)
		if err != nil {
			t.Fatal(err)
		}

		if !bytes.Equal(block, reassembled) {
			t.Error("reassembled block doesn't match original")
		}
	})
}
