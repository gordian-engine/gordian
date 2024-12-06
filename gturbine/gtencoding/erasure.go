package gtencoding

import (
	"fmt"

	"github.com/klauspost/reedsolomon"
)

const maxTotalShreds = 128

type Encoder struct {
	enc            reedsolomon.Encoder
	dataShreds     int
	recoveryShreds int
}

func NewEncoder(dataShreds, recoveryShreds int) (*Encoder, error) {
	if dataShreds <= 0 {
		return nil, fmt.Errorf("data shreds must be > 0")
	}
	if recoveryShreds <= 0 {
		return nil, fmt.Errorf("recovery shreds must be > 0")
	}
	if dataShreds+recoveryShreds > maxTotalShreds {
		return nil, fmt.Errorf("total shreds must be <= %d", maxTotalShreds)
	}

	enc, err := reedsolomon.New(dataShreds, recoveryShreds)
	if err != nil {
		return nil, fmt.Errorf("failed to create reed-solomon encoder: %w", err)
	}

	return &Encoder{
		enc:            enc,
		dataShreds:     dataShreds,
		recoveryShreds: recoveryShreds,
	}, nil
}

func (e *Encoder) GenerateRecoveryShreds(shreds [][]byte) ([][]byte, error) {
	if len(shreds) != e.dataShreds {
		return nil, fmt.Errorf("expected %d data shreds, got %d", e.dataShreds, len(shreds))
	}

	totalShreds := make([][]byte, e.dataShreds+e.recoveryShreds)
	copy(totalShreds, shreds)

	for i := e.dataShreds; i < len(totalShreds); i++ {
		totalShreds[i] = make([]byte, len(shreds[0]))
	}

	if err := e.enc.Encode(totalShreds); err != nil {
		return nil, fmt.Errorf("encoding failed: %w", err)
	}

	return totalShreds[e.dataShreds:], nil
}

func (e *Encoder) Reconstruct(allShreds [][]byte) error {
	if len(allShreds) != e.dataShreds+e.recoveryShreds {
		return fmt.Errorf("expected %d total shreds, got %d", e.dataShreds+e.recoveryShreds, len(allShreds))
	}

	// Count non-nil shreds
	validShreds := 0
	for _, shred := range allShreds {
		if shred != nil {
			validShreds++
		}
	}

	// Need at least dataShreds valid pieces for reconstruction
	if validShreds < e.dataShreds {
		return fmt.Errorf("insufficient shreds for reconstruction: have %d, need %d", validShreds, e.dataShreds)
	}

	if err := e.enc.Reconstruct(allShreds); err != nil {
		return fmt.Errorf("reconstruction failed: %w", err)
	}
	return nil
}

func (e *Encoder) Verify(allShreds [][]byte) (bool, error) {
	if len(allShreds) != e.dataShreds+e.recoveryShreds {
		return false, fmt.Errorf("expected %d total shreds, got %d", e.dataShreds+e.recoveryShreds, len(allShreds))
	}
	return e.enc.Verify(allShreds)
}
