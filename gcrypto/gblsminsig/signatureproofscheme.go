package gblsminsig

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math/big"
	"math/bits"

	"github.com/bits-and-blooms/bitset"
	"github.com/gordian-engine/gordian/gcrypto"
)

type SignatureProofScheme struct{}

func (SignatureProofScheme) New(msg []byte, keys []PubKey, pubKeyHash string) (
	gcrypto.CommonMessageSignatureProof, error,
) {
	return NewSignatureProof(msg, keys, pubKeyHash)
}

func (SignatureProofScheme) KeyIDChecker(keys []PubKey) gcrypto.KeyIDChecker {
	var n int
	nKeys := len(keys)
	if nKeys&(nKeys-1) == 0 {
		// Already a power of two.
		n = nKeys
	} else {
		n = 1 << bits.Len16(uint16(nKeys))
	}

	n = 2*n - 1
	return keyIDChecker{n: n}
}

type keyIDChecker struct {
	n int
}

func (c keyIDChecker) IsValid(keyID []byte) bool {
	if len(keyID) != 2 {
		return false
	}

	id := int(binary.BigEndian.Uint16(keyID))
	return id < c.n
}

func (SignatureProofScheme) Finalize(
	main gcrypto.CommonMessageSignatureProof,
	rest []gcrypto.CommonMessageSignatureProof,
) gcrypto.FinalizedCommonMessageSignatureProof {
	m := main.(SignatureProof)

	// TODO: m.AsSparse does unnecessary work;
	// we could add an unexported method specifically for this use case.
	mainSigs := m.AsSparse().Signatures
	_ = mainSigs

	pubKeys := make([]gcrypto.PubKey, m.sigTree.NUnaggregatedKeys())
	for i := range pubKeys {
		k, _, _ := m.sigTree.Get(i)
		pubKeys[i] = (*PubKey)(&k)
	}

	var bs bitset.BitSet
	m.SignatureBitSet(&bs)

	var mainIndex big.Int
	getMainCombinationIndex(len(pubKeys), &bs, &mainIndex)

	f := gcrypto.FinalizedCommonMessageSignatureProof{
		Keys:       pubKeys,
		PubKeyHash: m.keyHash,

		MainMessage: m.msg,
	}

	if len(rest) == 0 {
		// Don't allocate a map if there are no other signatures.
		return f
	}

	f.Rest = make(map[string][]gcrypto.SparseSignature, len(rest))
	for _, r := range rest {
		p := r.(SignatureProof)
		f.Rest[string(p.msg)] = p.AsSparse().Signatures
	}

	return f
}

func getMainCombinationIndex(nKeys int, bs *bitset.BitSet, out *big.Int) {
	k := int(bs.Count())

	out.SetUint64(0)
	var scratch big.Int

	// Iterate over the present indices, in order from lowest to highest.
	prev := -1
	for u, ok := bs.NextSet(0); ok && int(u) < nKeys; u, ok = bs.NextSet(u + 1) {
		i := int(u)
		remainingPositions := k - 1

		for j := prev + 1; j < i; j++ {
			remainingNumbers := nKeys - j - 1

			binomialCoefficient(remainingNumbers, remainingPositions, &scratch)
			out.Add(out, &scratch)
		}

		prev = i
		k--
	}
}

func indexToMainCombination(nKeys int, k int, combIndex *big.Int, out *bitset.BitSet) {
	out.ClearAll()
	if k == 0 {
		panic(errors.New("BUG: never call indexToCombination with k=0"))
	}

	var remaining, scratch big.Int
	remaining.Set(combIndex)

	curr := 0
	remainingPositions := k

	for remainingPositions > 0 {
		binomialCoefficient(nKeys-curr-1, remainingPositions-1, &scratch)

		// While the remaining value is >= possible combinations, increment curr.
		for curr < nKeys && remaining.Cmp(&scratch) >= 0 {
			remaining.Sub(&remaining, &scratch)
			curr++
			if curr < nKeys {
				binomialCoefficient(nKeys-curr-1, remainingPositions-1, &scratch)
			}
		}

		// We've found the next position that would give us this combination index.
		if curr < nKeys {
			out.Set(uint(curr))

			curr++
			remainingPositions--
		}
	}
}

func binomialCoefficient(n, k int, out *big.Int) {
	if k > n {
		panic(fmt.Errorf("BUG: k(%d) > n(%d)", k, n))
	}

	if k == 0 || k == n {
		out.SetUint64(1)
		return
	}

	// Use the smaller of k and n-k for fewer iterations.
	if k > n/2 {
		k = n - k
	}

	out.SetUint64(1)
	var scratch big.Int
	for i := range k {
		scratch.SetInt64(int64(n - i))
		out.Mul(out, &scratch)

		scratch.SetInt64(int64(i) + 1)
		out.Div(out, &scratch)
	}
}
