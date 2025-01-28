package gblsminsigtest

import (
	"encoding/binary"
	"fmt"
	"sync"

	"github.com/gordian-engine/gordian/gcrypto/gblsminsig"
)

var muSigners sync.RWMutex
var generatedSigners []gblsminsig.Signer

func DeterministicSigners(n int) []gblsminsig.Signer {
	res := optimisticLoadSigners(n)

	if len(res) >= n {
		return res
	}

	// We weren't able to load all the signers from the read lock, so take the write lock.
	muSigners.Lock()
	defer muSigners.Unlock()

	// Check the length again, because it is possible that
	// another writer filled the generated slice before we acquired the lock.
	if len(generatedSigners) < n {
		sizedSigners := make([]gblsminsig.Signer, 0, n)
		generatedSigners = append(sizedSigners, generatedSigners...)
		generatedSigners = generatedSigners[:n]

		var wg sync.WaitGroup
		for i := len(res); i < n; i++ {
			wg.Add(1)
			go generateOneSigner(&wg, &generatedSigners[i], i)
		}

		wg.Wait()
	}

	for i := len(res); i < n; i++ {
		res = append(res, generatedSigners[i])
	}

	return res
}

func optimisticLoadSigners(n int) []gblsminsig.Signer {
	res := make([]gblsminsig.Signer, 0, n)

	muSigners.RLock()
	defer muSigners.RUnlock()

	for i, s := range generatedSigners {
		if i >= n {
			break
		}

		res = append(res, s)
	}

	return res
}

func generateOneSigner(wg *sync.WaitGroup, dst *gblsminsig.Signer, i int) {
	defer wg.Done()

	ikm := [32]byte{}
	binary.BigEndian.PutUint64(ikm[24:32], uint64(i))

	s, err := gblsminsig.NewSigner(ikm[:])
	if err != nil {
		panic(fmt.Errorf("failed to make signer: %w", err))
	}

	*dst = s
}

func DeterministicPubKeys(n int) []gblsminsig.PubKey {
	out := make([]gblsminsig.PubKey, n)
	for i, s := range DeterministicSigners(n) {
		out[i] = s.PubKey().(gblsminsig.PubKey)
	}
	return out
}
