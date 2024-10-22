package tmdriver

import (
	"github.com/gordian-engine/gordian/tm/tmconsensus"
)

// InitChainRequest is sent from the engine to the driver,
// ensuring that the consensus store is in an appropriate initial state.
//
// InitChainRequest does not have an associated context like the other request types,
// because it is not associated with the lifecycle of a single step or round.
type InitChainRequest struct {
	Genesis tmconsensus.ExternalGenesis

	Resp chan InitChainResponse
}

// InitChainResponse is sent by the driver in response to an [InitChainRequest].
type InitChainResponse struct {
	// The app state hash to use in the first proposed block's PrevAppStateHash field.
	AppStateHash []byte

	// The validators for the consensus engine to use in the first proposed block.
	// If nil, the engine will use the GenesisValidators from the request.
	Validators []tmconsensus.Validator
}

// FinalizeBlockRequest is sent from the state machine to the driver,
// notifying the driver that the given header represents the block that is to be committed.
//
// The driver must evaluate the block corresponding to the header
// and return the validators to set as NextValidators on the subsequent block;
// and it must return the resulting app state hash,
// to be used as PrevAppStateHash in the subsequent block.
//
// Consumers of this value may assume that Resp is buffered and sends will not block.
type FinalizeBlockRequest struct {
	Header tmconsensus.Header
	Round  uint32

	Resp chan FinalizeBlockResponse
}

// FinalizeBlockResponse is sent by the driver in response to a [FinalizeBlockRequest].
type FinalizeBlockResponse struct {
	// For an unambiguous indicator of the block the driver finalized.
	Height    uint64
	Round     uint32
	BlockHash []byte

	// The resulting validators after evaluating the block.
	// If we are finalizing the block at height H,
	// this value will be used as the NextValidators field in block at height H+1,
	// thereby becoming the current validators at height H+2.
	Validators []tmconsensus.Validator

	// The app state after evaluating the block.
	AppStateHash []byte
}
