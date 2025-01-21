package gblsminsig

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math/big"
	"math/bits"

	"github.com/bits-and-blooms/bitset"
	"github.com/gordian-engine/gordian/gcrypto"
	blst "github.com/supranational/blst/bindings/go"
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

	pubKeys := make([]gcrypto.PubKey, m.sigTree.NUnaggregatedKeys())
	for i := range pubKeys {
		k, _, _ := m.sigTree.Get(i)
		pubKeys[i] = (*PubKey)(&k)
	}

	// Get the bit set representing the validators who voted for the committing block.
	var bs bitset.BitSet
	m.SignatureBitSet(&bs)

	// Now get the combination index for that set of validators.
	var mainIndex big.Int
	getMainCombinationIndex(len(pubKeys), &bs, &mainIndex)

	// Rely on integer division to simplify going from bits to whole bytes.
	mainIndexByteSize := (mainIndex.BitLen() + 7) / 8

	mainKeyID := make([]byte, 2+mainIndexByteSize)
	binary.BigEndian.PutUint16(mainKeyID[:2], uint16(bs.Count()))
	_ = mainIndex.FillBytes(mainKeyID[2:]) // Discard result since we pre-sized.

	// TODO: if we aren't using the key here,
	// then there should probably be a separate method
	// to only get the finally aggregated signature.
	_, mainSig := m.sigTree.Finalized()

	f := gcrypto.FinalizedCommonMessageSignatureProof{
		Keys:       pubKeys,
		PubKeyHash: m.keyHash,

		MainMessage: m.msg,

		MainSignatures: []gcrypto.SparseSignature{
			{
				KeyID: mainKeyID,
				Sig:   mainSig.Compress(),
			},
		},
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

func (SignatureProofScheme) ValidateFinalizedProof(
	proof gcrypto.FinalizedCommonMessageSignatureProof,
	hashesBySignContent map[string]string,
) (
	signBitsByHash map[string]*bitset.BitSet, allSignaturesUnique bool,
) {
	nKeys := len(proof.Keys)
	keys := make([]*PubKey, nKeys)
	for i, k := range proof.Keys {
		// We are often using PubKey values,
		// but we actually use a slice of pointers in Finalize,
		// so follow that here.
		keys[i] = k.(*PubKey)
	}

	// There is surely a better way we can use an existing sigTree for this.
	// But for now, we are going to decode the combination index
	// from the singular signature proof for the main block,
	// in order to get the bit set of the main keys present;
	// then we aggregate that set of main keys,
	// so that we can verify the given signature.

	if len(proof.MainSignatures) != 1 {
		// We expect exactly one main signature.
		return nil, false
	}

	mainKeyID := proof.MainSignatures[0].KeyID
	if len(mainKeyID) < 3 {
		// We need exactly two bytes for k,
		// and at least one byte for the combination index.
		return nil, false
	}

	k := int(binary.BigEndian.Uint16(mainKeyID[:2]))

	var combIndex big.Int
	combIndex.SetBytes(mainKeyID[2:])

	var keyBS bitset.BitSet
	indexToMainCombination(nKeys, k, &combIndex, &keyBS)

	aggMainKey := new(blst.P2)
	for u, ok := keyBS.NextSet(0); ok && int(u) < nKeys; u, ok = keyBS.NextSet(u + 1) {
		i := int(u)
		aggMainKey = aggMainKey.Add((*blst.P2Affine)(keys[i]))
	}

	finalizedMainKey := (*PubKey)(aggMainKey.ToAffine())
	if !finalizedMainKey.Verify(proof.MainMessage, proof.MainSignatures[0].Sig) {
		return nil, false
	}

	// Since the main proof checked out,
	// optimistically size the outgoing map.
	signBitsByHash = make(map[string]*bitset.BitSet, 1+len(proof.Rest))
	mainHash, ok := hashesBySignContent[string(proof.MainMessage)]
	if !ok {
		panic(fmt.Errorf(
			"BUG: missing main hash for sign content %x",
			proof.MainMessage,
		))
	}
	signBitsByHash[mainHash] = keyBS.Clone()

	// TODO: handle all of proof.Rest.

	return signBitsByHash, true
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
