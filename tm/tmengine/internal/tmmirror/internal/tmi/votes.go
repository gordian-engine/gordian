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

type AddFuturePrevoteRequest struct {
	H uint64
	R uint32

	PubKeyHash []byte

	// Mirror has to look these up out of band from the kernel.
	PubKeys []gcrypto.PubKey

	// Key is block hash.
	// There is no PrevVersion like in the non-future AddPrevoteRequest.
	Prevotes map[string]gcrypto.CommonMessageSignatureProof

	Resp chan AddVoteResult
}

type AddFuturePrecommitRequest struct {
	H uint64
	R uint32

	PubKeyHash []byte

	// Mirror has to look these up out of band from the kernel.
	PubKeys []gcrypto.PubKey

	// Key is block hash.
	// There is no PrevVersion like in the non-future AddPrecommitRequest.
	Precommits map[string]gcrypto.CommonMessageSignatureProof

	Resp chan AddVoteResult
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

	// The vote, when applied, contained no new data.
	// (This may only be possible with future votes?)
	AddVoteRedundant

	// Something went wrong internally -- most likely a store error.
	// This should only be a possible return value for future prevotes and precommits.
	AddVoteInternalError
)
