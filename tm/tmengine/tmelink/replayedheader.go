package tmelink

import (
	"fmt"

	"github.com/gordian-engine/gordian/tm/tmconsensus"
)

// ReplayedHeaderRequest is sent from the Driver to the Engine
// during mirror catchup.
type ReplayedHeaderRequest struct {
	Header tmconsensus.Header
	Proof  tmconsensus.CommitProof

	Resp chan<- ReplayedHeaderResponse
}

// ReplayedHeaderResponse is the response to [ReplayedHeaderRequest]
// sent from the Engine internals back to the Driver.
//
// If the header was replayed successfully,
// the Err field is nil.
//
// Otherwise, the error will be of type
// [ReplayedHeaderValidationError], [ReplayedHeaderOutOfSyncError],
// or [ReplayedHeaderInternalError].
type ReplayedHeaderResponse struct {
	Err error
}

// ReplayedHeaderValidationError indicates that the engine
// failed to validate the replayed header,j
// for example a hash mismatch or insufficient signatures.
// On this error type, the driver should note that the source of the header
// may be maliciously constructing headers.
type ReplayedHeaderValidationError struct {
	Err error
}

func (e ReplayedHeaderValidationError) Error() string {
	return "validation of replayed header failed: " + e.Err.Error()
}

func (e ReplayedHeaderValidationError) Unwrap() error {
	return e.Err
}

// ReplayedHeaderOutOfSyncError indicates that the replayed header
// had an invalid height, either before or after the current voting height.
//
// If many headers are attempting to be replayed concurrently,
// this may be an expected error type.
// If the driver is only replaying one block at a time,
// this likely indicates a critical issue in the driver's logic.
type ReplayedHeaderOutOfSyncError struct {
	WantHeight, GotHeight uint64
}

func (e ReplayedHeaderOutOfSyncError) Error() string {
	return fmt.Sprintf("received header at height %d, expected %d", e.GotHeight, e.WantHeight)
}

// ReplayedHeaderInternalError indicates an internal error to the engine.
// Upon receiving this error, the driver may assume the mirror
// has experienced an unrecoverable error.
type ReplayedHeaderInternalError struct {
	Err error
}

func (e ReplayedHeaderInternalError) Error() string {
	return "internal error while handling replayed header: " + e.Err.Error()
}

func (e ReplayedHeaderInternalError) Unwrap() error {
	return e.Err
}
