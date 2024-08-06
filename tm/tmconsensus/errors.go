package tmconsensus

import (
	"bytes"
	"fmt"
	"sort"

	"github.com/rollchains/gordian/gcrypto"
)

// HashUnknownError indicates a reference to an unknown or unrecognized hash.
type HashUnknownError struct {
	Got []byte
}

func (e HashUnknownError) Error() string {
	return fmt.Sprintf("hash %X unknown", e.Got)
}

// PreviousHashMismatchError indicates an input that has the wrong previous block hash.
type PreviousHashMismatchError struct {
	Want, Got []byte
}

func (e PreviousHashMismatchError) Error() string {
	return fmt.Sprintf(
		"previous block hash mismatch: expected %X, got %X",
		e.Want, e.Got,
	)
}

// AppDataHashMismatchError indicates an input that has the wrong app data hash.
type AppDataHashMismatchError struct {
	Want, Got []byte
}

func (e AppDataHashMismatchError) Error() string {
	return fmt.Sprintf(
		"app data hash mismatch: expected %X, got %X",
		e.Want, e.Got,
	)
}

// AppStateHashMismatchError indicates an input that has the wrong app state hash.
type AppStateHashMismatchError struct {
	Want, Got []byte
}

func (e AppStateHashMismatchError) Error() string {
	return fmt.Sprintf(
		"app state hash mismatch: expected %X, got %X",
		e.Want, e.Got,
	)
}

// HeightMismatchError indicates an input that has the wrong height.
type HeightMismatchError struct {
	Want, Got uint64
}

func (e HeightMismatchError) Error() string {
	return fmt.Sprintf(
		"height mismatch: expected %d, got %d",
		e.Want, e.Got,
	)
}

// HeightUnknownError indicates a request for a height that is not known.
type HeightUnknownError struct {
	Want uint64
}

func (e HeightUnknownError) Error() string {
	return fmt.Sprintf("height %d unknown", e.Want)
}

// RoundUnknownError indicates a request for a height and round with no corresponding record.
type RoundUnknownError struct {
	WantHeight uint64
	WantRound  uint32
}

func (e RoundUnknownError) Error() string {
	return fmt.Sprintf("height/round %d/%d unknown", e.WantHeight, e.WantRound)
}

type HashAlreadyExistsError struct {
	Hash []byte
}

func (e HashAlreadyExistsError) Error() string {
	return fmt.Sprintf("hash %x already exists", e.Hash)
}

// ProposalOverwriteError is returned when a proposal already exists
// and another proposal is attempted for the same height and round,
// regardless of whether the new attempt is identical to the existing proposal.
type ProposalOverwriteError struct {
	Height uint64
	Round  uint32
}

func (e ProposalOverwriteError) Error() string {
	return fmt.Sprintf("attempted to overwrite existing proposal at height/round %d/%d", e.Height, e.Round)
}

// DoublePrevoteError indicates a prevote message contained both an active and a nil prevote
// from one or more validators.
type DoublePrevoteError struct {
	PubKeys []gcrypto.PubKey
}

func (e DoublePrevoteError) Error() string {
	var buf bytes.Buffer
	buf.WriteString("simultaneous active and nil prevotes: ")
	for i, pk := range e.PubKeys {
		if i > 0 {
			fmt.Fprintf(&buf, ", %x", pk.PubKeyBytes())
		} else {
			fmt.Fprintf(&buf, "%x", pk.PubKeyBytes())
		}
	}
	return buf.String()
}

// DoublePrevoteByIndexError indicates a prevote contained both an active and a nil prevote
// from one or more validators.
//
// [DoublePrevoteError] should be preferred over this type,
// but in some circumstances the validators are not available,
// so all we can do is use their indices.
type DoubleVoteByIndexError struct {
	ValidatorIdxs []int
}

func (e DoubleVoteByIndexError) ToDoublePrevoteError(vals []Validator) DoublePrevoteError {
	pubKeys := make([]gcrypto.PubKey, len(e.ValidatorIdxs))
	for i, idx := range e.ValidatorIdxs {
		pubKeys[i] = vals[idx].PubKey
	}
	return DoublePrevoteError{
		PubKeys: pubKeys,
	}
}

func (e DoubleVoteByIndexError) ToDoublePrecommitError(vals []Validator) DoublePrecommitError {
	pubKeys := make([]gcrypto.PubKey, len(e.ValidatorIdxs))
	for i, idx := range e.ValidatorIdxs {
		pubKeys[i] = vals[idx].PubKey
	}
	return DoublePrecommitError{
		PubKeys: pubKeys,
	}
}

func (e DoubleVoteByIndexError) Error() string {
	sort.Ints(e.ValidatorIdxs)

	var buf bytes.Buffer
	buf.WriteString("double votes by validators at indices: ")
	for i, idx := range e.ValidatorIdxs {
		if i > 0 {
			buf.WriteString(", ")
		}
		fmt.Fprintf(&buf, "%d", idx)
	}
	return buf.String()
}

// DoublePrecommitError indicates a precommit message contained both an active and a nil precommit
// from one or more validators.
type DoublePrecommitError struct {
	PubKeys []gcrypto.PubKey
}

func (e DoublePrecommitError) Error() string {
	var buf bytes.Buffer
	buf.WriteString("simultaneous active and nil precommits: ")
	for i, pk := range e.PubKeys {
		if i > 0 {
			fmt.Fprintf(&buf, ", %x", pk.PubKeyBytes())
		} else {
			fmt.Fprintf(&buf, "%x", pk.PubKeyBytes())
		}
	}
	return buf.String()
}

type EmptyVoteError struct{}

func (EmptyVoteError) Error() string {
	return "both active and nil votes are empty"
}

// VoteTargetMismatchError indicates two prevote or precommit values
// disagree on their vote target.
type VoteTargetMismatchError struct {
	Want, Got VoteTarget
}

func (e VoteTargetMismatchError) Error() string {
	return fmt.Sprintf(
		"vote target mismatch: expected height=%d/round=%d/hash=%x; got height=%d/round=%d/hash=%x",
		e.Want.Height, e.Want.Round, e.Want.BlockHash, e.Got.Height, e.Got.Round, e.Got.BlockHash,
	)
}
