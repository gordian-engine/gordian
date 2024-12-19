package gmerkle

import (
	"fmt"

	"github.com/bits-and-blooms/bitset"
)

// Scheme specifies the details on producing a merkle tree from an ordered collection of leaves.
// Type parameter L is the leaf data, and I is the ID type of the nodes.
// The ID type will often be a byte slice representing a cryptographic hash,
// but in the case of public key aggregation it could be a public key.
type Scheme[L any, I comparable] interface {
	// How many children each branch must have
	// (excepting the rightmost branch in a row, which will have at least 1 element
	// but possibly less than BranchFactor).
	// This should usually be 2 unless profiling has indicated a more appropriate value.
	BranchFactor() uint8

	// BranchID calculates the ID for a branch.
	// The childIDs slice may have fewer than BranchFactor elements
	// if it is the rightmost node in a height,
	// but it will always have at least one element.
	//
	// It is the scheme's decision whether to pad the rightmost branch
	// when len(childIDs) < BranchFactor().
	//
	// The depth and rowIdx values are provided so that implementors
	// may use them in ID calculations as a preventative measure against
	// second preimage attacks, if so desired.
	BranchID(depth, rowIdx int, childIDs []I) (I, error)

	// LeafID calculates the ID for the given leaf data.
	// Like branchID, the index value is provided
	// as a possible prevention measure of second preimage attacks.
	LeafID(idx int, leafData L) (I, error)
}

// Tree is the primary Merkle tree used in Gordian.
// Compared to other possible implementations,
// features of this implementation include:
//   - Rather than representing branches as "hashes", they are "IDs" of the type parameter I.
//     This allows flexibility to aggregate public keys in a tree.
//   - All elements are assumed to be unique.
//     If two distinct leaves hash to the same ID,
//     Looking up the second instance probably will not work.
//   - The tree expects all leaf values to be known up front;
//     there is no support for modifying a tree.
//     As a result, methods are safe to call concurrently.
//   - Lookups into the tree reference start and end ranges of the original leaf data.
//     That is to say, looking up an ID could indicate it references the leaf values
//     at indices 5-8.
//   - After calculating the leaf IDs, the tree holds no reference to the leaf values.
//   - While a branching factor of 2 is suggested,
//     other sizes may be specified through the [Scheme].
type Tree[I comparable] struct {
	// Branch factor.
	m int

	nLeaves int

	// Tree of branch values, stored as a slice of slices.
	// The first row is the branches directly representing the leaves,
	// and the last row contains the lone root branch.
	branches [][]branch[I]
}

// RootID returns the ID of the root branch of the tree.
func (t *Tree[I]) RootID() I {
	return t.branches[len(t.branches)-1][0].ID
}

// Lookup searches the tree for a branch with the given ID.
// The ID may be the root of the tree, a leaf's ID,
// or any intermediate branch.
// If a match is found, leafIdxStart is the starting index into the original slice of leaf data,
// and n is how many elements are represented by the matching branch.
// If no match is found, Lookup returns -1, 0.
func (t *Tree[I]) Lookup(id I) (leafIdxStart, n int) {
	branchSpan := 1
	for ri, row := range t.branches {
		if ri > 0 {
			branchSpan *= t.m
		}

		for bi, b := range row {
			if b.ID == id {
				start := branchSpan * bi
				n = branchSpan
				if start+n > t.nLeaves {
					n = t.nLeaves - start
				}

				return start, n
			}
		}
	}
	return -1, 0
}

// BitSetToIDs returns branch IDs in t that map to leaves at the indices indicated in b.
// In many use cases of Tree, the branch IDs are nothing more than arbitrary identifiers of collections of leaves.
// For those, the simple bitset will be a sufficient and more condensed representation of the leaves' presence.
// For example, you may use bitset 0b1111 (the 4 least significant bits) to represent which leaves are used,
// and you may transmit [sig0, sig1, sig2, sig3] with the understanding that those map
// to the bitset in the expected order.
//
// However, in some use cases -- particularly those involving key and signature aggregation --
// the branch IDs are meaningful as they indicate the aggregated public key
// of a collection of non-aggregated public keys.
// In that case you may still use a bitset to indicate which leaves are represented
// (because the bitset can be transmitted in fewer bytes than the raw IDs),
// but then you will want to discover which set of IDs should be used to represent that bitset.
// As in the previous example, you may use bitset 0b1111,
// but you may transmit only a single signature that is the result of aggregating
// the signatures for the first 4 values.
// Depending on how expensive it is to re-calculate an aggregated public key,
// you may want to track the tree's IDs and their corresponding keys in some persistent storage.
func (t *Tree[I]) BitSetToIDs(b *bitset.BitSet) []I {
	var out []I

	// The bitset package expects uint values everywhere.
	// Convert these values to uint once up front, for readability when we need them later.
	m := uint(t.m)
	nLeaves := uint(t.nLeaves)

	// nextBranchesToCheck is a bitset indicating which branch indices in the next row
	// we need to compare against the input b.
	// We start at the root node, so this can be seeded to a single bit.
	nextBranchesToCheck := bitset.New(1)
	nextBranchesToCheck.Set(0)

	for depth := len(t.branches) - 1; depth > 0; depth-- {
		rowLen := uint(len(t.branches[depth]))

		pageSize := uint(1)
		for i := 0; i < depth; i++ {
			pageSize *= m
		}

		branchesToCheck := nextBranchesToCheck
		nextBranchesToCheck = bitset.New(rowLen * m)

		// For each index indicated in the current branches to check:
		for checkBranchIdx, ok := branchesToCheck.NextSet(0); ok && checkBranchIdx < rowLen; checkBranchIdx, ok = branchesToCheck.NextSet(checkBranchIdx + 1) {
			curBranch := t.branches[depth][int(checkBranchIdx)]

			// Identify the range of bits indicated by the current branch.
			branchStartBit := pageSize * checkBranchIdx
			branchEndBit := branchStartBit + pageSize
			if branchEndBit > nLeaves {
				// If we are on a branch containing a raised orphan,
				// don't run past the leaf count (when we have an unbalanced tree).
				branchEndBit = nLeaves
			}

			// Now create the bitset for this branch.
			curBranchBits := bitset.New(nLeaves)
			curBranchBits.FlipRange(branchStartBit, branchEndBit)

			// Intersect with the input to see if this branch is fully represented in the input.
			curBranchBits.InPlaceIntersection(b)

			count := curBranchBits.Count()
			if count == branchEndBit-branchStartBit {
				// Every bit was set.
				// Include this branch; and we don't need to check the children for this branch.
				out = append(out, curBranch.ID)
				continue
			}

			if count == 0 {
				// Also don't need to check the children for this branch,
				// but we don't include this ID in the output.
				continue
			}

			// Otherwise, some but not all of the current branch's bit representation were set in the input.
			// We will have to check all of this branch's children when we reach the next row.
			start, end := t.childIndexRange(depth, int(checkBranchIdx))
			nextBranchesToCheck.FlipRange(uint(start), uint(end))
		}

		// If we don't have to check anything in the next row, then we're done.
		if nextBranchesToCheck.None() {
			return out
		}
	}

	// Now we handle row 0.
	// At this point, we can do a plain intersection with the input
	// to determine which branches to include in the output.
	nextBranchesToCheck.InPlaceIntersection(b)
	rowLen := uint(len(t.branches[0]))
	for checkBranchIdx, ok := nextBranchesToCheck.NextSet(0); ok && checkBranchIdx < rowLen; checkBranchIdx, ok = nextBranchesToCheck.NextSet(checkBranchIdx + 1) {
		curBranch := t.branches[0][checkBranchIdx]
		out = append(out, curBranch.ID)
	}

	return out
}

// TreeVisitFunc is the callback passed to [Tree.WalkFromRoot].
// If it returns stop=true, traversal will stop and the callback will not be called again.
type TreeVisitFunc[I comparable] func(id I, depth, rowIdx int, childBranchIDs []I, leafIndex, nLeaves int) (stop bool)

// WalkFromRootB starts at the root branch of the tree
// and traverses breadth-first, calling fn on each branch.
func (t *Tree[I]) WalkFromRootB(fn TreeVisitFunc[I]) {
	rootRow := t.branches[len(t.branches)-1]
	if len(rootRow) != 1 {
		panic(fmt.Errorf("BUG: invalid tree; root row had %d branches", len(rootRow)))
	}

	if len(t.branches) == 1 {
		// Special case: tree contains only a single element.
		fn(rootRow[0].ID, 0, 0, nil, 0, 1)
		return
	}

	rootChildIDs := make([]I, len(t.branches[len(t.branches)-2]))
	for i, b := range t.branches[len(t.branches)-2] {
		rootChildIDs[i] = b.ID
	}

	if fn(rootRow[0].ID, len(t.branches)-1, 0, rootChildIDs, 0, t.nLeaves) {
		return
	}

	for depth := len(t.branches) - 2; depth > 0; depth-- {
		pageSize := 1
		for i := 0; i < depth; i++ {
			pageSize *= t.m
		}

		row := t.branches[depth]
		for i, b := range row {
			var childBranchIDs []I
			childBranchStart, childBranchEnd := t.childIndexRange(depth, i)
			if childBranchEnd > childBranchStart {
				childBranchIDs = make([]I, childBranchEnd-childBranchStart)
				for j, cb := range t.branches[depth-1][childBranchStart:childBranchEnd] {
					childBranchIDs[j] = cb.ID
				}
			}

			leafIdx := pageSize * i
			nLeaves := pageSize
			if leafIdx+nLeaves > t.nLeaves {
				nLeaves = t.nLeaves - leafIdx
			}
			if fn(b.ID, depth, i, childBranchIDs, leafIdx, nLeaves) {
				return
			}
		}
	}

	// Row 0 is a special case, as it won't have any children.
	for i, b := range t.branches[0] {
		if fn(b.ID, 0, i, nil, i, 1) {
			return
		}
	}
}

// WalkFromRootD starts at the root branch of the tree
// and traverses depth-first, calling fn on each branch.
func (t *Tree[I]) WalkFromRootD(fn TreeVisitFunc[I]) {
	rootRow := t.branches[len(t.branches)-1]
	if len(rootRow) != 1 {
		panic(fmt.Errorf("BUG: invalid tree; root row had %d branches", len(rootRow)))
	}

	if len(t.branches) == 1 {
		// Special case: tree contains only a single element.
		fn(rootRow[0].ID, 0, 0, nil, 0, 1)
		return
	}

	rootChildIDs := make([]I, len(t.branches[len(t.branches)-2]))
	for i, b := range t.branches[len(t.branches)-2] {
		rootChildIDs[i] = b.ID
	}

	if fn(rootRow[0].ID, len(t.branches)-1, 0, rootChildIDs, 0, t.nLeaves) {
		return
	}

	for i := range rootChildIDs {
		if t.walkD(len(t.branches)-2, i, fn) {
			return
		}
	}
}

func (t *Tree[I]) walkD(depth, rowIdx int, fn TreeVisitFunc[I]) (stop bool) {
	b := t.branches[depth][rowIdx]
	if depth == 0 {
		// Just emit the current branch.
		return fn(b.ID, 0, rowIdx, nil, rowIdx, 1)
	}

	// Otherwise we are at depth > 0, so we should have child nodes.
	// Emit ourself first.

	pageSize := 1
	for i := 0; i < depth; i++ {
		pageSize *= t.m
	}

	var childIDs []I
	childIdxStart, childIdxEnd := t.childIndexRange(depth, rowIdx)
	if childIdxEnd > childIdxStart {
		childSlice := t.branches[depth-1][childIdxStart:childIdxEnd]
		childIDs = make([]I, len(childSlice))
		for i, b := range childSlice {
			childIDs[i] = b.ID
		}
	}

	leafIdx := pageSize * rowIdx
	nLeaves := pageSize
	if leafIdx+nLeaves > t.nLeaves {
		nLeaves = t.nLeaves - leafIdx
	}

	if fn(b.ID, depth, rowIdx, childIDs, leafIdx, nLeaves) {
		return true
	}

	// Now walk our child nodes.
	for idx := childIdxStart; idx < childIdxEnd; idx++ {
		if t.walkD(depth-1, idx, fn) {
			return true
		}
	}

	return false
}

// childIndexRange returns the two integers to use for slicing the branches underneath
// the branch at parentDepth, parentRowIdx.
func (t *Tree[I]) childIndexRange(parentDepth, parentRowIdx int) (start, end int) {
	if parentDepth == 0 {
		panic(fmt.Errorf("BUG: childIndexRange should not be called with parentDepth=0"))
	}

	start = t.m * parentRowIdx
	end = start + t.m
	if end > len(t.branches[parentDepth-1]) {
		end = len(t.branches[parentDepth-1])
	}

	return start, end
}

type branch[I comparable] struct {
	ID I

	// Given the depth and index, it is trivial to discover
	// the parent branches or the leaves this branch represents.
	Depth int
	Index int
}

// NewTree returns a new Merkle tree based on the given scheme and leaf data.
func NewTree[L any, I comparable](scheme Scheme[L, I], leafData []L) (*Tree[I], error) {
	m := int(scheme.BranchFactor()) // m as in "m-ary tree".
	if m < 2 {
		return nil, fmt.Errorf("branch factor must be at least 2 (got %d)", m)
	}

	rows := [][]branch[I]{
		make([]branch[I], len(leafData)),
	}

	// Add all the rows at the correct sizes.
	// Naive approach for now because it's simple to read and implement.
	for lastRow := rows[len(rows)-1]; len(lastRow) > 1; lastRow = rows[len(rows)-1] {
		sz, rem := len(lastRow)/m, len(lastRow)%m
		if rem > 0 {
			sz++
		}

		rows = append(rows, make([]branch[I], sz))
	}

	// Seed first row with leaf data.
	for i, ld := range leafData {
		leafID, err := scheme.LeafID(i, ld)
		if err != nil {
			return nil, fmt.Errorf("error generating leaf ID for leaf at index %d: %w", i, err)
		}
		rows[0][i] = branch[I]{
			ID:    leafID,
			Depth: 0,
			Index: i,
		}
	}

	for depth, row := range rows {
		if depth == 0 {
			// Already populated first row.
			continue
		}

		prevRow := rows[depth-1]
		for i := range row {
			start := i * m
			end := start + m
			if end > len(prevRow) {
				end = len(prevRow)
			}

			if end == start+1 {
				row[i] = branch[I]{
					ID:    prevRow[len(prevRow)-1].ID,
					Depth: depth,
					Index: i,
				}
				// Overwrite the outer rows value.
				// If we only re-slice prevRow,
				// that won't affect what goes into the tree,
				// as it's a different view into the same backing array.
				rows[depth-1] = prevRow[:len(prevRow)-1]
				prevRow = rows[depth-1]

				continue
			}

			childBranches := prevRow[start:end]
			childIDs := make([]I, len(childBranches))
			for j, b := range childBranches {
				childIDs[j] = b.ID
			}

			id, err := scheme.BranchID(depth, i, childIDs)
			if err != nil {
				return nil, fmt.Errorf("failed to calculate branch ID at index %d in depth %d: %w", i, depth, err)
			}

			row[i] = branch[I]{
				ID:    id,
				Depth: depth,
				Index: i,
			}
		}
	}

	return &Tree[I]{
		m:        m,
		nLeaves:  len(leafData),
		branches: rows,
	}, nil
}
