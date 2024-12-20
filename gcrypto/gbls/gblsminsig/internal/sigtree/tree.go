package sigtree

import (
	"fmt"
	"iter"

	blst "github.com/supranational/blst/bindings/go"
)

// Tree presents a set of keys and signatures in a tree,
// using an array layout.
type Tree struct {
	nodes []node

	// Number of unaggregated keys.
	nKeys int
}

// Node in a tree.
// Every node has a Key set during a call to [New],
// and the Sig field is populated during calls to [Tree.AddSignature].
type node struct {
	Key blst.P2Affine
	Sig blst.P1Affine
}

// New returns a new Tree.
// The keys are an iterator because the caller collects [gcrypto.PubKey]
// but this tree wants the lower-level blst values;
// since we are collecting the values into a new slice,
// it doesn't make sense to have the caller allocate a new slice either.
func New(keys iter.Seq[blst.P2Affine], nKeys int) Tree {
	// For now, it must be a power of 2.
	if nKeys&(nKeys-1) != 0 {
		panic(fmt.Errorf("TODO: handle keys that are not power of 2 (got %d)", nKeys))
	}

	width := nKeys
	nNodes := width
	for width > 1 {
		width >>= 1
		nNodes += width
	}

	// Populate first row of leaf keys.
	nodes := make([]node, nNodes)
	layerWidth := 0
	for key := range keys {
		nodes[layerWidth].Key = key
		layerWidth++
	}

	// Then aggregate all the keys pairwise into a tree.
	readOffset := 0
	for readOffset < nNodes {
		nextLayerWidth := layerWidth >> 1
		for j := range nextLayerWidth {
			srcIdx := readOffset + j*2
			nodes[readOffset+layerWidth+j] = node{
				Key: aggregateKeys(
					nodes[srcIdx].Key,
					nodes[srcIdx+1].Key,
				),
			}
		}

		readOffset += layerWidth
		layerWidth = nextLayerWidth
	}

	return Tree{nodes: nodes, nKeys: nKeys}
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
	// that represents the keys sorted lexicographically (less memory),
	// or we could use a map (more memory but simpler).
	for i, node := range t.nodes {
		if node.Key.Equals(&k) {
			return i
		}
	}
	return -1
}

// Get returns the key and signature at the given index.
// The ok value indicates whether the index was valid.
// The key is guaranteed to be set if ok is true,
// and the signature may be a zero value
// if it was not explicitly set or inferred by its children being set.
func (t Tree) Get(idx int) (key blst.P2Affine, sig blst.P1Affine, ok bool) {
	if idx < 0 || idx >= len(t.nodes) {
		return blst.P2Affine{}, blst.P1Affine{}, false
	}

	key = t.nodes[idx].Key
	sig = t.nodes[idx].Sig
	// This should catch a gap in the initial unaggregated row.
	ok = key != (blst.P2Affine{})
	return key, sig, ok
}

// AddSignature associates the signature with the key at the given index.
// It is the caller's responsibility to ensure the signature was verified first,
// using Get if necessary to retrieve the key.
//
// If this signature's neighbor is also populated,
// the parent signature will be aggregated automatically,
// cascading up as many layers as required.
func (t Tree) AddSignature(idx int, sig blst.P1Affine) {
AGAIN:
	t.nodes[idx].Sig = sig

	if idx == len(t.nodes)-1 {
		// We just wrote the root signature.
		// No parents or neighbors to check.
		return
	}

	// Calculate our current layer first.
	layerStart := 0
	layerWidth := t.nKeys // TODO: this is wrong when not a power of 2.
	for idx >= layerStart+layerWidth {
		layerStart += layerWidth
		layerWidth >>= 1
	}

	offset := idx - layerStart

	parentIdx := layerStart + layerWidth + offset/2
	if t.nodes[parentIdx].Sig != (blst.P1Affine{}) {
		// Parent already has a signature,
		// so no work left to do.
		// (We could technically populate the neighbor via subtraction here,
		// but that currently doesn't seem necessary.)
		fmt.Printf("\tParent of %d (%d) already had a non-zero signature, stopping\n",
			idx, parentIdx)
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

	neighborSig := t.nodes[idx].Sig
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
	goto AGAIN
}

func aggregateKeys(a, b blst.P2Affine) blst.P2Affine {
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
