package gnetdag

// FixedTree represents a tree where each non-leaf node has a fixed number of children.
// With BranchFactor=3, the entries are arranged in layers like:
//
//	0 (L0)
//	1 2 3 (L1)
//	4 5 6 7 8 9 10 11 12 (L2)
//
// The entryIdx parameter used in methods on FixedTree
// are intended to be used as indices into an existing ordered slice
// that is to be treated as a tree.
//
// Methods on FixedTree use unchecked math,
// so invalid values, such as negative entry indices or branch factors,
// or branch factor so large that bf^2 overflows an int,
// result in undefined behavior.
type FixedTree struct {
	// The width of the layer at index 1.
	BranchFactor int
}

// Parent returns the "parent" index of the given entry index.
// It returns -1 for entryIdx = 0.
func (t FixedTree) Parent(entryIdx int) int {
	if entryIdx == 0 {
		return -1
	}

	// Special case for first layer, to avoid some off by one math.
	if entryIdx <= t.BranchFactor {
		return 0
	}

	curLayer := t.Layer(entryIdx)

	// Calculate how many entries are present before the parent layer.
	parentLayer := curLayer - 1
	ancestorEntries := 1
	ancestorWidth := 1
	for range parentLayer - 1 {
		ancestorWidth *= t.BranchFactor
		ancestorEntries += ancestorWidth
	}

	// Our current row has t.BranchFactor times more entries than the parent row,
	// so map our offset in the current row, into the offset of the parent row.
	parentLayerWidth := ancestorWidth * t.BranchFactor
	parentOffset := (entryIdx - parentLayerWidth - ancestorEntries) / t.BranchFactor
	return ancestorEntries + parentOffset
}

// FirstChild returns the entry index of the first child of the given entry index.
// Every parent contains t.BranchFactor children,
// but the FixedTree type does not track number of entries,
// so it is the caller's responsibility to confirm that there are
// at least t.BranchFactor children available.
func (t FixedTree) FirstChild(entryIdx int) int {
	if entryIdx == 0 {
		return 1
	}

	curLayerWidth := t.BranchFactor
	entriesBeforeCurLayer := 1

	for {
		if entryIdx <= entriesBeforeCurLayer+curLayerWidth {
			// Offset of the given entry index, within its respective layer.
			entryLayerOffset := entryIdx - entriesBeforeCurLayer

			// Then find the start of the next layer,
			// and move forward t.BranchFactor times according to the current layer offset.
			return entriesBeforeCurLayer + curLayerWidth + (entryLayerOffset * t.BranchFactor)
		}

		entriesBeforeCurLayer += curLayerWidth
		curLayerWidth *= t.BranchFactor
	}
}

// Layer returns the layer that would contain the given entry index.
func (t FixedTree) Layer(entryIdx int) int {
	if entryIdx == 0 {
		return 0
	}

	layer := 1
	layerWidth := t.BranchFactor
	entriesSoFar := 1 + t.BranchFactor

	for {
		if entryIdx < entriesSoFar {
			return layer
		}

		layer++
		layerWidth *= t.BranchFactor
		entriesSoFar += layerWidth
	}
}
