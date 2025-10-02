package tmconsensus

import (
	"bytes"
	"fmt"
	"slices"
	"sort"

	"github.com/gordian-engine/gordian/gcrypto"
)

// Validator is the simple representation of a validator,
// just a public key and a voting power.
type Validator struct {
	PubKey gcrypto.PubKey
	Power  uint64
}

// ValidatorSet is a fixed, ordered collection of validators.
type ValidatorSet struct {
	// Consumers must assume that the Validators slice is shared in many places
	// and therefore cannot be modified.
	Validators []Validator

	// The public keys of the validators,
	// with each public key at the corresponding index in the Validators field.
	//
	// Many lower-level APIs work directly against a slice of public keys,
	// so it is simpler to have one canonical slice of the public keys
	// rather than repeatedly allocating
	// or otherwise maintaining a managed slice.
	PubKeys []gcrypto.PubKey

	// Hashes generated via a [HashScheme].
	PubKeyHash, VotePowerHash []byte
}

// ValidatorSet reports whether the collection of validators and the calculated hashes
// are the same in v and other.
func (v ValidatorSet) Equal(other ValidatorSet) bool {
	return bytes.Equal(v.PubKeyHash, other.PubKeyHash) &&
		bytes.Equal(v.VotePowerHash, other.VotePowerHash) &&
		ValidatorSlicesEqual(v.Validators, other.Validators)
}

// NewValidatorSet returns a ValidatorSet based on vs,
// with hashes calculated using hs.
//
// NewValidatorSet assumes ownership over the validator slice,
// so that slice should not be modified after passing it to NewValidatorSet.
func NewValidatorSet(vs []Validator, hs HashScheme) (ValidatorSet, error) {
	pubKeys := ValidatorsToPubKeys(vs)
	s := ValidatorSet{
		Validators: vs,
		PubKeys:    pubKeys,
	}

	var err error
	s.PubKeyHash, err = hs.PubKeys(ValidatorsToPubKeys(vs))
	if err != nil {
		return ValidatorSet{}, fmt.Errorf("failed to calculate public key hash: %w", err)
	}

	s.VotePowerHash, err = hs.VotePowers(ValidatorsToVotePowers(vs))
	if err != nil {
		return ValidatorSet{}, fmt.Errorf("failed to calculate vote power hash: %w", err)
	}

	return s, nil
}

// SortValidators sorts vs in-place, by power descending,
// and then by public key ascending.
func SortValidators(vs []Validator) {
	sort.Slice(vs, func(i, j int) bool {
		if vs[i].Power == vs[j].Power {
			return bytes.Compare(vs[i].PubKey.PubKeyBytes(), vs[j].PubKey.PubKeyBytes()) < 0
		}
		return vs[i].Power > vs[j].Power
	})
}

// ValidatorsToPubKeys returns a slice of just the public keys of vs.
func ValidatorsToPubKeys(vs []Validator) []gcrypto.PubKey {
	out := make([]gcrypto.PubKey, len(vs))
	for i, v := range vs {
		out[i] = v.PubKey
	}
	return out
}

// ValidatorsToVotePowers returns a slice of just the vote powers of vs.
func ValidatorsToVotePowers(vs []Validator) []uint64 {
	out := make([]uint64, len(vs))
	for i, v := range vs {
		out[i] = v.Power
	}
	return out
}

// ValidatorSlicesEqual reports whether the slices vs1 and vs2 are equivalent.
func ValidatorSlicesEqual(vs1, vs2 []Validator) bool {
	return slices.EqualFunc(vs1, vs2, func(v1, v2 Validator) bool {
		return v1.Power == v2.Power && v1.PubKey.Equal(v2.PubKey)
	})
}

// CanTrustValidators reports whether the validator set vs contains at least 1/3 voting power
// represented by the passed-in set of trusted public keys.
func CanTrustValidators(vs []Validator, pubKeys []gcrypto.PubKey) bool {
	trustedKeys := make(map[string]struct{}, len(pubKeys))
	for _, k := range pubKeys {
		trustedKeys[string(k.PubKeyBytes())] = struct{}{}
	}

	var totalPower, trustedPower uint64
	for _, v := range vs {
		totalPower += v.Power
		if _, trusted := trustedKeys[string(v.PubKey.PubKeyBytes())]; trusted {
			trustedPower += v.Power
		}
	}

	return trustedPower > ByzantineMinority(totalPower)+1
}
