package sigtree

import (
	"fmt"
	"iter"
	"math"
	"math/bits"
	"slices"

	"github.com/bits-and-blooms/bitset"
	blst "github.com/supranational/blst/bindings/go"
)

// Tree presents a set of keys and signatures in a tree,
// using an array layout.
type Tree struct {
	keys []blst.P2Affine
	sigs []blst.P1Affine

	// The bitset indicating what signatures are present.
	// This is exported so that the SignatureProof can read it.
	SigBits *bitset.BitSet

	// Number of unaggregated keys.
	nKeys int
}

// New returns a new Tree.
// The keys are an iterator because the caller collects [gcrypto.PubKey]
// but this tree wants the lower-level blst values;
// since we are collecting the values into a new slice,
// it doesn't make sense to have the caller allocate a new slice either.
func New(keys iter.Seq[blst.P2Affine], nKeys int) Tree {
	if nKeys < 1 || nKeys > math.MaxUint16 {
		panic(fmt.Errorf("BUG: nKeys must be > 1 and < %d: got %d", math.MaxUint16, nKeys))
	}

	var leavesWidth int
	if nKeys&(nKeys-1) == 0 {
		// Already a power of two, so just use that value directly.
		leavesWidth = nKeys
	} else {
		leavesWidth = 1 << (bits.Len16(uint16(nKeys)))
	}

	nNodes := 2*leavesWidth - 1

	t := Tree{
		keys: make([]blst.P2Affine, nNodes),
		sigs: make([]blst.P1Affine, nNodes),

		// We already knew it fits in a uint16,
		// so uint(nKeys) is safe.
		SigBits: bitset.New(uint(nKeys)),

		nKeys: nKeys,
	}

	// Populate first row of leaf keys.
	i := 0
	for key := range keys {
		t.keys[i] = key
		i++
	}

	layerWidth := leavesWidth

	// Then aggregate all the keys pairwise into a tree.
	readOffset := 0
	for readOffset < nNodes {
		nextLayerWidth := layerWidth >> 1
		for j := range nextLayerWidth {
			srcIdx := readOffset + j*2
			t.keys[readOffset+layerWidth+j] = aggregateKeys(
				t.keys[srcIdx],
				t.keys[srcIdx+1],
			)
		}

		readOffset += layerWidth
		layerWidth = nextLayerWidth
	}

	return t
}

// NUnaggregatedKeys returns the number of unaggregated keys in the tree.
func (t Tree) NUnaggregatedKeys() int {
	return t.nKeys
}

// Index searches through the tree and returns the numeric index
// for the key equal to the input k.
//
// If no matching key is found, -1 is returned.
func (t Tree) Index(k blst.P2Affine) int {
	// This is doing a linear search for now.
	// Unclear if it's worth optimizing.
	// We could maintain a separate list of indexes
	// that represents the keys sorted lexicographically (less memory but with binary search),
	// or we could use a map (more memory for keys but simpler lookup).
	for i, tk := range t.keys {
		if tk.Equals(&k) {
			return i
		}
	}
	return -1
}

// Get returns the key and signature at the given index.
// The ok value indicates whether the index was in bounds.
// The key is guaranteed to be set if ok is true,
// and the signature may be a zero value
// if it was not explicitly set or inferred by its children being set.
func (t Tree) Get(idx int) (key blst.P2Affine, sig blst.P1Affine, ok bool) {
	if idx < 0 || idx >= len(t.keys) {
		return blst.P2Affine{}, blst.P1Affine{}, false
	}
	return t.keys[idx], t.sigs[idx], true
}

// AddSignature associates the signature with the key at the given index.
// It is the caller's responsibility to ensure the signature was verified first,
// using Get if necessary to retrieve the key.
//
// If this signature's neighbor is also populated,
// the parent signature will be aggregated automatically,
// cascading up as many layers as required.
func (t Tree) AddSignature(idx int, sig blst.P1Affine) {
	addedSigBits := false

AGAIN:
	t.sigs[idx] = sig

	if idx == len(t.sigs)-1 {
		// We just wrote the root signature.
		// No parents or neighbors to check.
		// But we do need to ensure every bit is set.
		t.SigBits.SetAll()
		return
	}

	var layerWidth int
	if t.nKeys&(t.nKeys-1) == 0 {
		// Already a power of two, so just use that value directly.
		layerWidth = t.nKeys
	} else {
		layerWidth = 1 << (bits.Len16(uint16(t.nKeys)))
	}

	// Calculate our current layer first.
	layerStart := 0
	var nLeaves uint = 1
	for idx >= layerStart+layerWidth {
		layerStart += layerWidth
		layerWidth >>= 1
		nLeaves <<= 1
	}

	// The offset in the current layer.
	offset := idx - layerStart

	// Now set the signature bit(s).
	// We only need to do this on the first loop;
	// discovered aggregations will not set any unset bits.
	if !addedSigBits {
		startLeaf := uint(offset) * nLeaves
		end := min(startLeaf+nLeaves, uint(t.nKeys))
		for i := uint(startLeaf); i < end; i++ {
			t.SigBits.Set(i)
		}

		addedSigBits = true
	}

	parentIdx := layerStart + layerWidth + offset/2
	if t.sigs[parentIdx] != (blst.P1Affine{}) {
		// Parent already has a signature,
		// so no work left to do.
		//
		// We could technically populate the neighbor via subtraction here,
		// but that currently doesn't seem necessary.
		// If we did populate the neighbor, then we save work in verifying the signature
		// should we ever receive it by itself later.
		// Alternatively, we could expand the tree API
		// so that we could cheaply and lazily check if the key is calculable.
		// Presumably subtracting one signature from another
		// is cheaper than verifying a signature.
		return
	}

	// The parent signature is missing. Do we have our neighbor?
	// Get the neighbor's index.
	// If current index is even, neighbor is to the right.
	if (idx & 1) == 0 {
		// Even index, neighbor to right.
		idx++
	} else {
		idx--
	}

	neighborKeyExists := t.keys[idx] != (blst.P2Affine{})
	if neighborKeyExists {
		neighborSig := t.sigs[idx]
		if neighborSig == (blst.P1Affine{}) {
			// Neighbor is missing, so we can't populate the parent.
			return
		}

		// We have sufficient information to build the parent's signature.
		// This is the same aggregation scheme we use in aggregateKeys,
		// which is to say it hasn't been benchmarked.
		aff := new(blst.P1).Add(&sig).Add(&neighborSig).ToAffine()
		idx = parentIdx
		sig = *aff
	} else {
		// The neighbor key doesn't exist, so the signature aggregates with nothing.
		// We keep the same signature,
		// but we update the index to the parent index and go again.
		idx = parentIdx
	}

	// Loop back to top so that we can traverse towards the root.
	goto AGAIN
}

func (t Tree) walkFromRoot(
	handle func(isRoot bool, idx int, key blst.P2Affine, sig blst.P1Affine),
) {
	if rootSig := t.sigs[len(t.sigs)-1]; rootSig != (blst.P1Affine{}) {
		// When we have the root signature, we don't need to traverse anything.
		handle(true, len(t.sigs)-1, t.keys[len(t.keys)-1], t.sigs[len(t.sigs)-1])
		return
	}

	curRowStart := len(t.sigs) - 3
	curRowWidth := 2

	// Track indices that we don't need to check,
	// due to an ancestor having already been included in the output.
	var skipCheck []bool
	if t.nKeys&(t.nKeys-1) == 0 {
		// Already a power of two, so just use that value directly.
		skipCheck = make([]bool, t.nKeys)
	} else {
		skipCheck = make([]bool, 1<<(bits.Len16(uint16(t.nKeys))))
	}

	// Intermediate layers (not root and not leaves).
	for curRowStart > 0 {
		for i := curRowStart; i < curRowStart+curRowWidth; i++ {
			if skipCheck[i-curRowStart] {
				// We already included an ancestor of this index.
				continue
			}

			// Do we have a signature for this node?
			if t.sigs[i] == (blst.P1Affine{}) {
				continue
			}

			// We do have a signature, and an ancestor didn't cover it.
			handle(false, i, t.keys[i], t.sigs[i])
			skipCheck[i-curRowStart] = true
		}

		// "Double" the skip check.
		for i := curRowWidth - 1; i >= 0; i-- {
			skipCheck[2*i+1] = skipCheck[i]
			skipCheck[2*i] = skipCheck[i]
		}

		curRowWidth *= 2
		curRowStart -= curRowWidth
	}

	// Finally, we are on the leaf row.
	for i := range t.nKeys {
		if skipCheck[i] {
			continue
		}
		if t.sigs[i] == (blst.P1Affine{}) {
			continue
		}
		handle(false, i, t.keys[i], t.sigs[i])
	}
}

func (t Tree) SparseIndices(dst []int) []int {
	t.walkFromRoot(func(_ bool, i int, _ blst.P2Affine, _ blst.P1Affine) {
		dst = append(dst, i)
	})
	return dst
}

func (t Tree) FinalizedKey() (blst.P2Affine) {
	accKey := new(blst.P2)
	t.walkFromRoot(func(_ bool, _ int, k blst.P2Affine, _ blst.P1Affine) {
		accKey.Add(&k)
	})

	aff := accKey.ToAffine()
	return *aff
}

// Finalized returns the aggregation of all the present signatures,
// and the single aggregated key belonging to that aggregated signature.
// It does not mutate the tree.
func (t Tree) Finalized() (blst.P2Affine, blst.P1Affine) {
	// We will follow the same structure as in SparseIndices,
	// but we won't share that code since that produces a data structure we don't need.

	if rootSig := t.sigs[len(t.sigs)-1]; rootSig != (blst.P1Affine{}) {
		// We have the root signature, so we don't need to traverse anything.
		return t.keys[len(t.keys)-1], t.sigs[len(t.sigs)-1]
	}

	accKey := new(blst.P2)
	accSig := new(blst.P1)

	curRowStart := len(t.sigs) - 3
	curRowWidth := 2

	// Track indices that we don't need to check,
	// due to an ancestor having already been included in the output.
	var skipCheck []bool
	if t.nKeys&(t.nKeys-1) == 0 {
		// Already a power of two, so just use that value directly.
		skipCheck = make([]bool, t.nKeys)
	} else {
		skipCheck = make([]bool, 1<<(bits.Len16(uint16(t.nKeys))))
	}

	for curRowStart > 0 {
		for i := curRowStart; i < curRowStart+curRowWidth; i++ {
			if skipCheck[i-curRowStart] {
				// We already included an ancestor of this index.
				continue
			}

			// Do we have a signature for this node?
			if t.sigs[i] == (blst.P1Affine{}) {
				continue
			}

			// We do have a signature, and an ancestor didn't cover it.
			// So aggregate the representative key and signature.
			accKey = accKey.Add(&t.keys[i])
			accSig = accSig.Add(&t.sigs[i])
			skipCheck[i-curRowStart] = true
		}

		// "Double" the skip check.
		for i := curRowWidth - 1; i >= 0; i-- {
			skipCheck[2*i+1] = skipCheck[i]
			skipCheck[2*i] = skipCheck[i]
		}

		curRowWidth *= 2
		curRowStart -= curRowWidth
	}

	// Finally, we are on the leaf row.
	for i := range t.nKeys {
		if skipCheck[i] {
			continue
		}
		if t.sigs[i] == (blst.P1Affine{}) {
			continue
		}

		accKey = accKey.Add(&t.keys[i])
		accSig = accSig.Add(&t.sigs[i])
	}

	keyAff := accKey.ToAffine()
	sigAff := accSig.ToAffine()
	return *keyAff, *sigAff
}

// ClearSignatures zeros every signature in the tree.
// This is useful for reusing a tree if no keys have changed.
func (t Tree) ClearSignatures() {
	clear(t.sigs)
}

func (t Tree) Clone() Tree {
	return Tree{
		// Keys are immutable,
		// sigs are not.
		keys: t.keys,
		sigs: slices.Clone(t.sigs),

		SigBits: t.SigBits.Clone(),

		nKeys: t.nKeys,
	}
}

func (t Tree) Derive() Tree {
	return Tree{
		// Keys are immutable.
		keys: t.keys,

		sigs: make([]blst.P1Affine, len(t.keys)),

		SigBits: bitset.New(uint(t.nKeys)),

		nKeys: t.nKeys,
	}
}

func aggregateKeys(a, b blst.P2Affine) blst.P2Affine {
	// Keys are always aggregated such that the padded keys
	// are to the right of the non-padded keys,
	// so it is safe to only check if b is zero.
	if b == (blst.P2Affine{}) {
		return a
	}
	// There are a few other ways we could calculate this,
	// but I haven't benchmarked any of them.
	// Other options include:
	//  - p2.FromAffine.Add
	//  - new(blst.P2Aggregate.Aggregate(...)
	//
	// It probably is worth benchmarking,
	// because the Aggregate case may be fewer CGo calls.
	aff := new(blst.P2).Add(&a).Add(&b).ToAffine()
	return *aff
}
