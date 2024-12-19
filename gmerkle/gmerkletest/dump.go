package gmerkletest

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/gordian-engine/gordian/gmerkle"
)

// DumpStringIDTree is a shorthand for DumpTree when the tree has string IDs.
func DumpStringIDTree(t *gmerkle.Tree[string]) string {
	return DumpTree(t, func(id string) string { return id })
}

// DumpTree returns a crude string showing the structure of the Merkle tree t.
// This output is only meant for debugging purposes, and it is not guaranteed to be stable.
func DumpTree[I comparable](t *gmerkle.Tree[I], idFormatter func(I) string) string {
	var buf bytes.Buffer
	rootDepth := -1

	t.WalkFromRootD(func(id I, depth, _ int, childBranchIDs []I, leafIdx, nLeaves int) (stop bool) {
		if rootDepth == -1 {
			rootDepth = depth
		}

		indent := strings.Repeat("   ", rootDepth-depth)
		buf.WriteString(indent)

		if nLeaves == 1 {
			fmt.Fprintf(&buf, "[L=%d] ", leafIdx)
		}

		buf.WriteString(idFormatter(id))
		buf.WriteByte('\n')
		return false
	})

	return buf.String()
}
