package tmstore

import (
	"context"

	"github.com/gordian-engine/gordian/gcrypto"
	"github.com/gordian-engine/gordian/tm/tmconsensus"
)

// ValidatorStore manages storage and retrieval of sets of validators,
// split into sets of public keys and sets of voting powers.
type ValidatorStore interface {
	// SavePubKeys saves the ordered collection of public keys,
	// and returns the calculated hash to retrieve the same set of keys later.
	SavePubKeys(context.Context, []gcrypto.PubKey) (string, error)

	// SavePubKeys saves the ordered collection of vote powers,
	// and returns the calculated hash to retrieve the same set of powers later.
	SaveVotePowers(context.Context, []uint64) (string, error)

	// LoadPubKeys loads the set of public keys belonging to the given hash.
	LoadPubKeys(context.Context, string) ([]gcrypto.PubKey, error)

	// LoadVotePowers loads the set of vote powers belonging to the given hash.
	LoadVotePowers(context.Context, string) ([]uint64, error)

	// LoadValidators loads a set of validators constituted of the public keys and vote powers
	// corresponding to the given hashes.
	LoadValidators(ctx context.Context, keyHash, powHash string) ([]tmconsensus.Validator, error)
}
