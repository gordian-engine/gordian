package tmi

import "github.com/gordian-engine/gordian/gcrypto"

type AddPrevoteRequest struct {
	H uint64
	R uint32

	// Key is block hash.
	PrevoteUpdates map[string]VoteUpdate

	Response chan AddVoteResult
}

type AddPrecommitRequest struct {
	H uint64
	R uint32

	// Key is block hash.
	PrecommitUpdates map[string]VoteUpdate

	Response chan AddVoteResult
}

// VoteUpdate is part of AddPrevoteRequest and AddPrecommitRequest,
// indicating the new vote content and the previous version.
// The kernel uses the previous version to decide if the update
// can be applied or if the update is stale.
type VoteUpdate struct {
	Proof       gcrypto.CommonMessageSignatureProof
	PrevVersion uint32
}

// AddVoteResult is the result when applying an AddPrevoteRequest or AddPrecommitRequest.
type AddVoteResult uint8

const (
	_ AddVoteResult = iota // Invalid.

	AddVoteAccepted  // Votes successfully applied.
	AddVoteConflict  // Version conflict when applying votes; do a retry.
	AddVoteOutOfDate // Height and round too old; message should be ignored.
)
