package gsbd

import (
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"cosmossdk.io/core/transaction"
	"golang.org/x/crypto/blake2b"
)

const txsHashSize = 32

// We need the zero hash for the special, but probably common,
// case of zero transactions.
//go:generate go run ./dataid_generate.go

// TxsHash returns the accumulated hash resulting from
// the hash of the concatenated transaction hashes.
// This is the last segment of the data ID.
func TxsHash(txs []transaction.Tx) [txsHashSize]byte {
	hasher, err := blake2b.New(txsHashSize, nil)
	if err != nil {
		panic(fmt.Errorf("impossible: blake2b.New failed: %w", err))
	}

	for _, tx := range txs {
		hash := tx.Hash()
		_, _ = hasher.Write(hash[:])
	}

	txsHash := make([]byte, 0, txsHashSize)
	txsHash = hasher.Sum(txsHash)
	out := [txsHashSize]byte{}
	_ = copy(out[:], txsHash)
	return out
}

func DataID(
	height uint64,
	round uint32,
	txs []transaction.Tx,
) string {
	if len(txs) == 0 {
		return fmt.Sprintf("%d:%d%s", height, round, zeroHashSuffix)
	}

	return fmt.Sprintf("%d:%d:%d:%x", height, round, len(txs), TxsHash(txs))
}

func ParseDataID(id string) (
	height uint64, round uint32,
	nTxs int,
	txsHash [txsHashSize]byte,
	err error,
) {
	if hr, ok := strings.CutSuffix(id, zeroHashSuffix); ok {
		parts := strings.SplitN(hr, ":", 3) // Yes, 3, not 2.
		if len(parts) != 2 {
			err = errors.New("invalid data ID format; expected HEIGHT:ROUND:N_TXS:TXS_HASH")
			return
		}

		height, err = strconv.ParseUint(parts[0], 10, 64)
		if err != nil {
			err = fmt.Errorf("failed to parse height: %w", err)
			return
		}

		var u64 uint64
		u64, err = strconv.ParseUint(parts[1], 10, 32)
		if err != nil {
			err = fmt.Errorf("failed to parse round: %w", err)
			return
		}
		round = uint32(u64)

		// nTxs is already initialized to zero, don't need to reassign.
		copy(txsHash[:], zeroHash)

		return
	}

	parts := strings.SplitN(id, ":", 5) // Yes, 5, not 4.
	if len(parts) != 4 {
		err = errors.New("invalid data ID format; expected HEIGHT:ROUND:N_TXS:TXS_HASH")
		return
	}

	height, err = strconv.ParseUint(parts[0], 10, 64)
	if err != nil {
		err = fmt.Errorf("failed to parse height: %w", err)
		return
	}

	u64, err := strconv.ParseUint(parts[1], 10, 32)
	if err != nil {
		err = fmt.Errorf("failed to parse round: %w", err)
		return
	}
	round = uint32(u64)

	s64, err := strconv.ParseInt(parts[2], 10, 32)
	if err != nil {
		err = fmt.Errorf("failed to parse number of txs: %w", err)
		return
	}
	nTxs = int(s64)

	if wantLen := hex.EncodedLen(txsHashSize); len(parts[3]) != wantLen {
		// We could check this earlier to avoid work in parsing the numbers.
		err = fmt.Errorf("wrong length for txs hash; want %d, got %d", wantLen, len(parts[3]))
		return
	}
	_, err = hex.AppendDecode(txsHash[:0], []byte(parts[3]))
	if err != nil {
		err = fmt.Errorf("failed to decode txs hash: %w", err)
		return
	}

	// We've parsed everything, and err is nil.
	return
}
