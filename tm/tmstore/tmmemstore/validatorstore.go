package tmmemstore

import (
	"context"
	"errors"
	"slices"
	"sync"

	"github.com/gordian-engine/gordian/gcrypto"
	"github.com/gordian-engine/gordian/tm/tmconsensus"
	"github.com/gordian-engine/gordian/tm/tmstore"
)

type ValidatorStore struct {
	hs tmconsensus.HashScheme

	mu   sync.RWMutex
	keys map[string][]gcrypto.PubKey
	pows map[string][]uint64
}

func NewValidatorStore(hs tmconsensus.HashScheme) *ValidatorStore {
	return &ValidatorStore{
		hs: hs,

		keys: make(map[string][]gcrypto.PubKey),
		pows: make(map[string][]uint64),
	}
}

func (s *ValidatorStore) SavePubKeys(_ context.Context, keys []gcrypto.PubKey) (string, error) {
	hash, err := s.hs.PubKeys(keys)
	if err != nil {
		return "", err
	}
	sHash := string(hash)

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.keys[sHash]; ok {
		return sHash, tmstore.PubKeysAlreadyExistError{ExistingHash: sHash}
	}

	// TODO: should this clone the public keys?
	s.keys[sHash] = keys
	return sHash, nil
}

func (s *ValidatorStore) SaveVotePowers(_ context.Context, pows []uint64) (string, error) {
	hash, err := s.hs.VotePowers(pows)
	if err != nil {
		return "", err
	}
	sHash := string(hash)

	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.pows[sHash]; ok {
		return sHash, tmstore.VotePowersAlreadyExistError{ExistingHash: sHash}
	}

	s.pows[sHash] = slices.Clone(pows)
	return sHash, nil
}

func (s *ValidatorStore) LoadPubKeys(_ context.Context, hash string) ([]gcrypto.PubKey, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	keys, ok := s.keys[hash]
	if !ok {
		return nil, tmstore.NoPubKeyHashError{Want: hash}
	}

	return keys, nil
}

func (s *ValidatorStore) LoadVotePowers(_ context.Context, hash string) ([]uint64, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	pows, ok := s.pows[hash]
	if !ok {
		return nil, tmstore.NoVotePowerHashError{Want: hash}
	}

	return pows, nil
}

func (s *ValidatorStore) LoadValidators(_ context.Context, keyHash, powHash string) ([]tmconsensus.Validator, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var err error
	keys, ok := s.keys[keyHash]
	if !ok {
		err = tmstore.NoPubKeyHashError{Want: keyHash}
	}
	pows, ok := s.pows[powHash]
	if !ok {
		err = errors.Join(err, tmstore.NoVotePowerHashError{Want: powHash})
	}

	if err != nil {
		return nil, err
	}

	if len(keys) != len(pows) {
		return nil, tmstore.PubKeyPowerCountMismatchError{
			NPubKeys:   len(keys),
			NVotePower: len(pows),
		}
	}

	vals := make([]tmconsensus.Validator, len(keys))
	for i, k := range keys {
		vals[i] = tmconsensus.Validator{
			PubKey: k,
			Power:  pows[i],
		}
	}

	return vals, nil
}
