package gblsminsig

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"iter"
	"math/big"
	"math/bits"
	"slices"
	"strings"

	"github.com/bits-and-blooms/bitset"
	"github.com/gordian-engine/gordian/gcrypto"
	blst "github.com/supranational/blst/bindings/go"
)

// SignatureProofScheme is a [gcrypto.SignatureProofScheme]
// for the minimal-sized-signature BLS format.
//
// At a high level, the most important detail to understand about BLS
// is that any number keys and signatures can be respectively aggregated
// into a single key or signature.
// Performing the aggregation is relatively cheap,
// and verifying an aggregated signature is about as expensive
// as verifying an unaggregated one.
//
// Without getting deep into the math behind BLS,
// keys and signatures exist as points on elliptic curves,
// and points on those curves have the property that they can be added and subtracted.
// Those additions and subtractions are commutative,
// just like everyday addition and subtraction that we are used to.
// That means if we have keys A, B, C, and D,
// it doesn't matter if we do (A + (B + (C + D))) or (D + (A + (C + B)));
// the result is the same regardless of order.
//
// However, to correctly distribute the aggregated keys and signatures during validator gossip,
// we do have to be particular about how we aggregate.
// First consider a naive case with the A, B, C, and D key example above.
// We will consider "A" to mean the combination of both the key and the signature belonging to A.
// Suppose you get one message of the aggregation (A+B)
// and another message of the aggregation (B+D).
// You are allowed to aggregate those two messages, resulting in (A+B+B+D),
// but this unorganized pattern is unsustainable at scale.
// If the next message you got was (B+C+D),
// you could aggregate that into (A+B+B+B+C+D+D).
// In fact you could verify that signature properly if you continue to track
// which keys and signatures were accumulated more than once,
// but if we require the network to track counts of how many times a pairing is present,
// it ends up quickly becoming a secondary and irrelevant detail to track.
//
// So instead of allowing arbitrary aggregation during gossip,
// we require the aggregations to be pairwise in a tree format.
// If our keyset was the 8 keys, A, B, C, D, E, F, G, and H,
// then valid pairings include A+B, C+D, A+B+C+D, or A+B+C+D+E+F+G+H.
// Invalid pairings include B+C (because that would leave A, the first key, unpaired),
// or A+B+E+F (while A+B and E+F are independently correct, they are not a contiguous collection).
//
// The pairwise tree format also allows us to use a single number to represent a determinstic pairing,
// which is much more efficient than using bitsets.
// For example, while numbers 0-7 indicate the independent keys A-H,
// then index 8 would indicate A+B, 9 would indicate C+D,
// all the way up to 15 indicating the full aggregation of A through H.
// (A binary tree with n leaves when n is a power of two then has (2*n)-1 total nodes.)
// Transmitting that single number is currently encoded as a 16-bit integer key ID in the sparse signature,
// which is significantly less data to transmit than the aggregated key,
// while remaining completely sufficient for any other validator to decode.
//
// When we get to signature finalization -- that is, writing a previous commit proof in a block header --
// we no longer abide by the pairwise rules,
// because the finalized signature is not intended to be modified further.
//
// There are two parts to finalized signatures.
// First is the "main" signature: this is signing the block that is being committed.
// The main signature is always present.
// Besides the main signature, there is a collection of zero or more signatures
// for any other votes, which could be votes for nil or votes for a different block.
//
// The key IDs for the finalized signatures are represented in two parts.
// First is a two-byte header, a big-endian uint16, representing the number of signatures
// aggregated to produce the finalized signature.
// The remaining bytes are a big-endian encoding of the [combinatorial index]
// of the present signatures from the key space.
// The meaning of "key space" varies slightly depending on
// what part of the finalized signature we are inspecting.
// For the main signature, the key space is the full original key set,
// so if there were 100 validators and we indicate 90 of them voted for the block,
// then that index indicates exactly which 90 keys make up that aggregation.
//
// Then the non-main signatures follow a specific ordering.
// Suppose of the remaining 10, there were 6 votes for nil,
// and then 2 votes for some other block.
// We order by the number of votes descending,
// and in case of a tie then it goes by sign content ascending.
//
// The combinatorial index for the 6 votes for nil are encoded as "10 choose 6"
// because we know that 90 of the 100 signatures were already used;
// then the two votes for the other block are encoded as "4 choose 2"
// The last two absent votes have no representation in the finalized signature.
// Using a reduced key space here allows us to pack signature representations
// into fewer bytes, as for example "4 choose 2" only has 6 possible values
// and "100 choose 2" has 4,950 possible values.
//
// [combinatorial index]: https://en.wikipedia.org/wiki/Combinatorial_number_system
type SignatureProofScheme struct{}

func (SignatureProofScheme) New(msg []byte, keys []gcrypto.PubKey, pubKeyHash string) (
	gcrypto.CommonMessageSignatureProof, error,
) {
	// It's unfortunate that we are allocating another slice for this.
	// Prefer to call NewSignatureProof directly
	// if you already have a slice of gblsminsig.PubKey.
	blsKeys := make([]PubKey, len(keys))
	for i, k := range keys {
		blsKeys[i] = k.(PubKey)
	}

	return NewSignatureProof(msg, blsKeys, pubKeyHash)
}

func (SignatureProofScheme) KeyIDChecker(keys []gcrypto.PubKey) gcrypto.KeyIDChecker {
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

func (SignatureProofScheme) CanMergeFinalizedProofs() bool {
	// Finalized proofs cannot be decomposed into precommits.
	return false
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
	// We will continue to use this bit set to track which validators have alredy voted.
	var presentVoteBits bitset.BitSet
	m.SignatureBitSet(&presentVoteBits)

	// Now get the combination index for that set of validators.
	// We reuse combIndex later too.
	var combIndex big.Int
	calculateCombinationIndex(len(pubKeys), &presentVoteBits, &combIndex)

	// Rely on integer division to simplify going from bits to whole bytes.
	// Note that if the combIndex is zero
	// (i.e. everyone voted for the committing block)
	// that the bit length will be zero,
	// and therefore no bytes will be used in populating the combination index.
	mainIndexByteSize := (combIndex.BitLen() + 7) / 8

	mainKeyID := make([]byte, 2+mainIndexByteSize)
	binary.BigEndian.PutUint16(mainKeyID[:2], uint16(presentVoteBits.Count()))
	_ = combIndex.FillBytes(mainKeyID[2:]) // Discard result since we pre-sized.

	mainSig := m.sigTree.FinalizedSig()

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
		// Don't allocate a map or do any other work
		// if there are no other signatures.
		return f
	}

	sortRestForFinalizing(rest)

	f.Rest = make(map[string][]gcrypto.SparseSignature, len(rest))

	var (
		// The bit sets indicating the current proof's indices
		// in the reduced set and in the original set, respectively.
		reducedBits, projectedBits bitset.BitSet

		// These are the result of createKeyProjection,
		// called in the front of the rest loop.
		reducedKeys []gcrypto.PubKey
		projections originalProjection
	)

	for _, r := range rest {
		p := r.(SignatureProof)

		// After this call, projectedBits holds the indices of the original keys
		// represented in the current proof.
		p.SignatureBitSet(&projectedBits)

		reducedKeys, projections = createKeyProjection(pubKeys, &presentVoteBits)

		// Create bitset in reduced key set, reusing the reducedBits bit set.
		reducedBits.ClearAll()
		for u, ok := projectedBits.NextSet(0); ok; u, ok = projectedBits.NextSet(u + 1) {
			idx, found := projections.FindReducedIndex(int(u))
			if found {
				reducedBits.Set(uint(idx))

				// Set the bit corresponding to the original public keys too.
				presentVoteBits.Set(uint(projections[idx]))
			} else {
				// Since we are finalizing a proof that should already be validated,
				// panicking here is appropriate.
				// Anything wrong with the proofs should have been detected much earlier.
				panic(fmt.Errorf(
					"BUG: proof that signed %x said it represented original key at index %d, but that index was not part of the projection",
					p.msg, u,
				))
			}
		}

		calculateCombinationIndex(len(reducedKeys), &reducedBits, &combIndex)

		// Create key ID using same format as main.
		// Rely on integer division to simplify going from bits to whole bytes.
		// Again, if combIndex is zero, indexByteSize will be zero too.
		indexByteSize := (combIndex.BitLen() + 7) / 8
		restKeyID := make([]byte, 2+indexByteSize)
		binary.BigEndian.PutUint16(restKeyID[:2], uint16(reducedBits.Count()))
		_ = combIndex.FillBytes(restKeyID[2:]) // Discard result since we pre-sized.

		restSig := p.sigTree.FinalizedSig()

		f.Rest[string(p.msg)] = []gcrypto.SparseSignature{
			{
				KeyID: restKeyID,
				Sig:   restSig.Compress(),
			},
		}
	}

	return f
}

// sortRestForFinalizing sorts the rest entries first by k (the number of keys) descending;
// and if k is equal, sort by the signing content ascending.
func sortRestForFinalizing(rest []gcrypto.CommonMessageSignatureProof) {
	slices.SortFunc(rest, func(a, b gcrypto.CommonMessageSignatureProof) int {
		var bs bitset.BitSet

		aa := a.(SignatureProof)
		aa.SignatureBitSet(&bs)
		na := bs.Count()

		bb := b.(SignatureProof)
		bb.SignatureBitSet(&bs)
		nb := bs.Count()

		if na > nb {
			return -1
		}
		if nb > na {
			return 1
		}

		// Otherwise they're equal, so compare their messages
		// (in normal, ascending order).
		ret := bytes.Compare(aa.msg, bb.msg)
		if ret == 0 {
			panic(fmt.Errorf(
				"BUG: have two separate rest entries with same message %x",
				aa.msg,
			))
		}

		return ret
	})
}

// calculateCombinationIndex writes the combination index to the out argument.
// nKeys is the count of the input set of keys,
// and bs indicates which indices in the input set are to be represented.
// The out argument is an argument, not a return value,
// so that we can reuse the underlying bytes already allocated to out.
func calculateCombinationIndex(nKeys int, bs *bitset.BitSet, out *big.Int) {
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

// createKeyProjection accepts the original set of keys
// and a bit set indicating which keys have already been used;
// and it returns a new slice of the remaining keys,
// and an originalProjection (which is a list of the indices
// into the original keys).
//
// This function operates on slices of gcrypto.PubKey
// because we are already dealing with a slice of that type in [(SignatureProofScheme).Finalize].
func createKeyProjection(originalKeys []gcrypto.PubKey, usedKeysBitSet *bitset.BitSet) (
	reducedKeys []gcrypto.PubKey,
	p originalProjection,
) {
	sz := len(originalKeys) - int(usedKeysBitSet.Count())
	reducedKeys = make([]gcrypto.PubKey, 0, sz)
	p = make(originalProjection, 0, sz)

	for i, k := range originalKeys {
		if usedKeysBitSet.Test(uint(i)) {
			continue
		}

		// The original key was not used, so add it to the reduced set.
		p = append(p, i)
		reducedKeys = append(reducedKeys, k)
	}
	return reducedKeys, p
}

// originalProjection is an ordered collection of original indices
// to maintain a projection into the original set of public keys,
// based on a reduced key set.
//
// For example, if there were ten original keys indexed 0-9,
// and then the reduced key set took 0-5, and 8,
// then the remaining slice would be [6, 7, 9].
// The indices into the slice are the "reduced indices",
// meaning the reduced index 0 corresponds to original index 6,
// and reduced indices 1 and 2 correspond to original indices 7 and 9.
type originalProjection []int

// FindReducedIndex accepts the index within the original set,
// and returns the value of the reduced index
// and a boolean indicating whether the value was found.
//
// For example, if the projection contains [3, 5, 7]
// then that indicates your reduced set of keys map to the original keys
// at indices 3, 5, and 7.
// In that case, FindReducedIndex(4) would return (-1, false)
// because there is no reduced index for original index 4.
// But FindReducedIndex(7) would return (2, true)
// because the reduced set at index 2 maps to the original key at index 7.
func (p originalProjection) FindReducedIndex(originalIdx int) (int, bool) {
	return slices.BinarySearch(p, int(originalIdx))
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
	if len(mainKeyID) < 2 {
		// We need exactly two bytes for k.
		// If the combination index was zero, there will be no bytes used.
		return nil, false
	}

	k := int(binary.BigEndian.Uint16(mainKeyID[:2]))
	if k > nKeys {
		// Invalid/corrupted key.
		return nil, false
	}

	// Scratch combination index to reuse on every proof we process.
	var combIndex big.Int
	combIndex.SetBytes(mainKeyID[2:])

	// The bits indicating which keys in the original set have been used so far.
	// This value is used throughout the rest loop.
	var usedOriginalBits bitset.BitSet
	decodeCombinationIndex(nKeys, k, &combIndex, &usedOriginalBits)

	aggMainKey := new(blst.P2)
	for u, ok := usedOriginalBits.NextSet(0); ok && int(u) < nKeys; u, ok = usedOriginalBits.NextSet(u + 1) {
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
	signBitsByHash[mainHash] = usedOriginalBits.Clone()

	// Variables to reuse in the upcoming loop.
	var (
		// The bits into the reduced set of keys,
		// that the current proof represents.
		reducedProofBits bitset.BitSet

		// The bits into the original set of keys,
		// that the current proof represents.
		originalProjectionBits bitset.BitSet

		// These are the result of createKeyProjection,
		// called in the front of the rest loop.
		reducedKeys []gcrypto.PubKey
		projections originalProjection
	)

	for msgContent, sigs := range orderedRestSignatures(proof.Rest) {
		if len(sigs) != 1 {
			// Each rest entry should have exactly one signature,
			// if it was finalized through the same scheme.
			return nil, false
		}

		sig := sigs[0]
		if len(sig.KeyID) < 2 {
			// We need exactly two bytes for k.
			// If the combination index was zero, there will be no bytes used.
			return nil, false
		}

		k := int(binary.BigEndian.Uint16(sig.KeyID[:2]))

		// Reusing combIndex from outside loop.
		combIndex.SetBytes(sig.KeyID[2:])

		// Determine the bits for this proof.
		// First get the reduced key set.
		reducedKeys, projections = createKeyProjection(proof.Keys, &usedOriginalBits)
		// Then determine the bit set mapping this combination index into the reduced key set.
		if k > len(reducedKeys) {
			// Corrupt/invalid key ID.
			return nil, false
		}
		decodeCombinationIndex(len(reducedKeys), k, &combIndex, &reducedProofBits)

		// Project back to original key set and check for duplicates.
		originalProjectionBits.ClearAll()
		for u, ok := reducedProofBits.NextSet(0); ok; u, ok = reducedProofBits.NextSet(u + 1) {
			// Find the original index for this reduced index.
			pIdx := int(u)
			if pIdx >= len(projections) {
				panic(errors.New("BUG: lost original index"))
			}
			oIdx := uint(projections[pIdx])

			if usedOriginalBits.Test(oIdx) {
				// Duplicate signature.
				return signBitsByHash, false
			}

			// Mark the index of the original key as used for this proof.
			originalProjectionBits.Set(oIdx)

			// That bit is used now, so just set it while we're here.
			usedOriginalBits.Set(oIdx)
		}

		// Aggregate the keys by using indices into the original set.
		aggKey := new(blst.P2)
		for u, ok := originalProjectionBits.NextSet(0); ok; u, ok = originalProjectionBits.NextSet(u + 1) {
			i := int(u)
			aggKey = aggKey.Add((*blst.P2Affine)(keys[i]))
		}

		// And verify that the signature matches the aggregated key.
		finalizedKey := (*PubKey)(aggKey.ToAffine())
		if !finalizedKey.Verify([]byte(msgContent), sig.Sig) {
			return nil, false
		}

		// It was valid, so add it to the output.
		hash, ok := hashesBySignContent[msgContent]
		if !ok {
			panic(fmt.Errorf("BUG: missing hash for sign content %x", msgContent))
		}

		// We always clone this because we know the size to use now
		// and we are reusing the local value anyway.
		signBitsByHash[hash] = originalProjectionBits.Clone()
	}

	return signBitsByHash, true
}

// decodeCombinationIndex accepts n, k, and the combination index,
// and writes to the out bit set,
// setting the bits of the indices that the combination index represents.
func decodeCombinationIndex(nKeys int, k int, combIndex *big.Int, out *bitset.BitSet) {
	out.ClearAll()
	if k == 0 {
		panic(errors.New("BUG: never call decodeCombinationIndex with k=0"))
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

// orderedRestSignatures reads from the provided map and returns an iterator
// that goes in order of largest to smallest k (where k is the big endian uint16
// at the start of the key ID); and in case of a tie for k,
// then orders by the sign content ascending.
func orderedRestSignatures(rest map[string][]gcrypto.SparseSignature) iter.Seq2[string, []gcrypto.SparseSignature] {
	type order struct {
		k           [2]byte
		signContent string
	}

	orders := make([]order, 0, len(rest))
	for signContent, ss := range rest {
		if len(ss) == 0 {
			// Invalid value for the map.
			// We will just skip this in the output for now,
			// and it should bubble up as a different error later.
			//
			// Alernatively we could set k to 0xFFFF,
			// which should immediately be identified as an error.
			continue
		}

		o := order{signContent: signContent}
		_ = copy(o.k[:], ss[0].KeyID)
		orders = append(orders, o)
	}

	slices.SortFunc(orders, func(a, b order) int {
		ret := bytes.Compare(b.k[:], a.k[:]) // b, a for descending by key size.
		if ret == 0 {
			ret = strings.Compare(a.signContent, b.signContent)
		}
		return ret
	})

	// Now that we have sorted the keys, we can iterate over that slice
	// to produce the iterator.
	return func(yield func(string, []gcrypto.SparseSignature) bool) {
		for _, o := range orders {
			if !yield(o.signContent, rest[o.signContent]) {
				return
			}
		}
	}
}

// The binomial coefficient ("n choose k")
// is the number of ways to choose k elements from a set of n elements,
// where selection order does not matter.
//
// We use this when determining the combination index,
// which is part of the key ID of the finalized signatures.
func binomialCoefficient(n, k int, out *big.Int) {
	if k > n {
		// The standard library returns zero here,
		// but this is a caller bug in our case.
		panic(fmt.Errorf("BUG: k(%d) > n(%d): caller needs to prevent this case", k, n))
	}

	if k == 0 || k == n {
		// Unlikely early return.
		out.SetUint64(1)
		return
	}

	// Assume the standard library is an optimized calculation.
	// We could possibly do better if we use some caching,
	// but let's hold off on that until profiling shows it worthwhile.
	out.Binomial(int64(n), int64(k))
}
