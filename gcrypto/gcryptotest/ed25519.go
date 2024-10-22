package gcryptotest

import (
	"bytes"
	"crypto/ed25519"
	"fmt"
	"sync"

	"github.com/gordian-engine/gordian/gcrypto"
)

// DeterministicEd25519Signers returns a deterministic slice of ed25519 signer values.
//
// There are two advantages to using deterministic keys.
// First, subsequent runs of the same test will use the same keys,
// so logs involving keys or IDs will not change across runs,
// simplifying the debugging process.
// Second, the generated keys are cached,
// so there is effectively zero CPU time cost for additional tests
// calling this function, beyond the first call.
func DeterministicEd25519Signers(n int) []gcrypto.Ed25519Signer {
	// See if we can get all the signers under a read lock.
	res, got := optimisticLoadEd25519Signers(n)
	if got >= len(res) {
		return res
	}

	// Otherwise we don't have enough.
	// Take a write lock and copy in what we are missing.
	muEd.Lock()
	defer muEd.Unlock()

	// Now that we have the lock, check once more if we have enough private keys.
	// It's possible another goroutine grabbed the write lock before we did.
	haveGenerated := len(generatedEd25519)
	if haveGenerated < len(res) {
		newPrivs := make([]ed25519.PrivateKey, len(res)-haveGenerated)
		generatedEd25519 = append(generatedEd25519, newPrivs...)

		// Now that the generated private key slice has grown,
		// start a goroutine for each private key
		// to spread generation work across available CPUs.
		// It's fine if we start many goroutines and they content
		var wg sync.WaitGroup

		for i := haveGenerated; i < len(generatedEd25519); i++ {
			wg.Add(1)
			go generateEd25519PrivKey(i, &wg)
		}

		wg.Wait()
	}

	for i := got; i < len(res); i++ {
		privKeyBytes := bytes.Clone([]byte(generatedEd25519[i]))
		res[i] = gcrypto.NewEd25519Signer(privKeyBytes)
	}

	return res
}

func optimisticLoadEd25519Signers(n int) ([]gcrypto.Ed25519Signer, int) {
	res := make([]gcrypto.Ed25519Signer, n)

	// With the read lock, copy as many private keys as we have.
	muEd.RLock()
	defer muEd.RUnlock()

	got := 0
	for i := range res {
		if i >= len(generatedEd25519) {
			break
		}

		privKeyBytes := bytes.Clone([]byte(generatedEd25519[i]))
		res[i] = gcrypto.NewEd25519Signer(privKeyBytes)
		got++
	}

	return res, got
}

func generateEd25519PrivKey(idx int, wg *sync.WaitGroup) {
	defer wg.Done()

	seed := fmt.Sprintf("%032d", idx) // Seed must be 32 bytes long.
	generatedEd25519[idx] = ed25519.NewKeyFromSeed([]byte(seed))
}

var muEd sync.RWMutex
