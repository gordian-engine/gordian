package tmmemstore

import (
	"context"
	"fmt"
	"sync"

	"github.com/gordian-engine/gordian/tm/tmconsensus"
	"github.com/gordian-engine/gordian/tm/tmstore"
)

type RoundStore struct {
	mu sync.RWMutex

	// Height -> Round -> Hash -> Headers.
	// There will almost always be a single proposed header under the hash,
	// but with multiple proposers, it is possible that two independent proposers
	// will propose an identical header,
	// differing only by the proposal metadata.
	phs map[uint64]map[uint32]map[string][]tmconsensus.ProposedHeader

	// Height -> Round -> collection.
	prevotes, precommits map[uint64]map[uint32]tmconsensus.SparseSignatureCollection

	// Height -> collection.
	// There really should never be more than one replayed header in a height, though.
	replayedHeaders map[uint64][]tmconsensus.Header
}

func NewRoundStore() *RoundStore {
	return &RoundStore{
		phs: make(map[uint64]map[uint32]map[string][]tmconsensus.ProposedHeader),

		prevotes:   make(map[uint64]map[uint32]tmconsensus.SparseSignatureCollection),
		precommits: make(map[uint64]map[uint32]tmconsensus.SparseSignatureCollection),

		replayedHeaders: make(map[uint64][]tmconsensus.Header),
	}
}

func (s *RoundStore) SaveRoundProposedHeader(ctx context.Context, ph tmconsensus.ProposedHeader) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	byRound, ok := s.phs[ph.Header.Height]
	if !ok {
		byRound = make(map[uint32]map[string][]tmconsensus.ProposedHeader)
		s.phs[ph.Header.Height] = byRound
	}

	byHash, ok := byRound[ph.Round]
	if !ok {
		byHash = make(map[string][]tmconsensus.ProposedHeader)
		byRound[ph.Round] = byHash
	}

	if havePHs, ok := byHash[string(ph.Header.Hash)]; ok {
		// Already have a proposed header for this hash.
		// See if it is from the same proposer.
		for _, have := range havePHs {
			if ph.ProposerPubKey.Equal(have.ProposerPubKey) {
				return tmstore.OverwriteError{
					Field: "pubkey",
					Value: fmt.Sprintf("%x", ph.ProposerPubKey.PubKeyBytes()),
				}
			}
		}
	}

	byHash[string(ph.Header.Hash)] = append(byHash[string(ph.Header.Hash)], ph)

	return nil
}

func (s *RoundStore) SaveRoundReplayedHeader(ctx context.Context, h tmconsensus.Header) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Check if an existing proposed header has the same hash.
	if byRound, ok := s.phs[h.Height]; ok {
		for _, byHash := range byRound {
			if _, ok := byHash[string(h.Hash)]; ok {
				return tmstore.OverwriteError{
					Field: "hash",
					Value: fmt.Sprintf("%x", h.Hash),
				}
			}
		}
	}

	s.replayedHeaders[h.Height] = append(s.replayedHeaders[h.Height], h)
	return nil
}

func (s *RoundStore) OverwriteRoundPrevoteProofs(
	ctx context.Context,
	height uint64,
	round uint32,
	proofs tmconsensus.SparseSignatureCollection,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	byRound, ok := s.prevotes[height]
	if !ok {
		byRound = make(map[uint32]tmconsensus.SparseSignatureCollection)
		s.prevotes[height] = byRound
	}

	byRound[round] = proofs
	return nil
}

func (s *RoundStore) OverwriteRoundPrecommitProofs(
	ctx context.Context,
	height uint64,
	round uint32,
	proofs tmconsensus.SparseSignatureCollection,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	byRound, ok := s.precommits[height]
	if !ok {
		byRound = make(map[uint32]tmconsensus.SparseSignatureCollection)
		s.precommits[height] = byRound
	}

	byRound[round] = proofs
	return nil
}

func (s *RoundStore) LoadRoundState(ctx context.Context, height uint64, round uint32) (
	phs []tmconsensus.ProposedHeader,
	prevotes, precommits tmconsensus.SparseSignatureCollection,
	err error,
) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if roundMap, ok := s.phs[height]; ok {
		if hashMap, ok := roundMap[round]; ok && len(hashMap) > 0 {
			phs = make([]tmconsensus.ProposedHeader, 0, len(hashMap))
			for _, phsForHash := range hashMap {
				phs = append(phs, phsForHash...)
			}
		}
	}

	if prevoteHeightMap, ok := s.prevotes[height]; ok {
		prevotes = prevoteHeightMap[round]
	}

	replayedHeaders := s.replayedHeaders[height]
	if precommitHeightMap, ok := s.precommits[height]; ok {
		precommits = precommitHeightMap[round]

		for hash := range precommits.BlockSignatures {
			if hash == "" {
				continue
			}

			// For each non-nil precommit hash,
			// check if it is for a replayed header;
			// if so, add the replayed header to the proposed header list.
			for _, rh := range replayedHeaders {
				if hash == string(rh.Hash) {
					phs = append(phs, tmconsensus.ProposedHeader{Header: rh})
				}
			}
		}
	}

	if phs == nil && prevotes.BlockSignatures == nil && precommits.BlockSignatures == nil {
		return nil, prevotes, precommits, tmconsensus.RoundUnknownError{WantHeight: height, WantRound: round}
	}

	return phs, prevotes, precommits, nil
}
