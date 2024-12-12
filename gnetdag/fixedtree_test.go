package gnetdag_test

import (
	"testing"

	"github.com/gordian-engine/gordian/gnetdag"
	"github.com/stretchr/testify/require"
)

// Most of these tests use a branch factor of 3,
// resulting in layers like:
//	0 (L0)
//	1 2 3 (L1)
//	4 5 6 7 8 9 10 11 12 (L2)
//	13 14 15 16... (L3)

func TestFixedTree_Layer(t *testing.T) {
	t.Parallel()

	tree := gnetdag.FixedTree{BranchFactor: 3}
	require.Equal(t, 0, tree.Layer(0))
	require.Equal(t, 1, tree.Layer(1))
	require.Equal(t, 2, tree.Layer(4))

	tree.BranchFactor = 5
	require.Equal(t, 0, tree.Layer(0))
	require.Equal(t, 1, tree.Layer(4))
}

func TestFixedTree_Parent(t *testing.T) {
	t.Parallel()

	tree := gnetdag.FixedTree{BranchFactor: 3}
	require.Equal(t, -1, tree.Parent(0))

	require.Equal(t, 0, tree.Parent(1))
	require.Equal(t, 0, tree.Parent(2))
	require.Equal(t, 0, tree.Parent(3))

	require.Equal(t, 1, tree.Parent(4))
	require.Equal(t, 1, tree.Parent(5))
	require.Equal(t, 1, tree.Parent(6))
	require.Equal(t, 2, tree.Parent(7))
	require.Equal(t, 2, tree.Parent(8))
	require.Equal(t, 2, tree.Parent(9))
	require.Equal(t, 3, tree.Parent(10))
	require.Equal(t, 3, tree.Parent(11))
	require.Equal(t, 3, tree.Parent(12))

	require.Equal(t, 4, tree.Parent(13))
}

func TestFixedTree_FirstChild(t *testing.T) {
	t.Parallel()

	tree := gnetdag.FixedTree{BranchFactor: 3}

	require.Equal(t, 1, tree.FirstChild(0))

	require.Equal(t, 4, tree.FirstChild(1))
	require.Equal(t, 7, tree.FirstChild(2))
	require.Equal(t, 10, tree.FirstChild(3))

	require.Equal(t, 13, tree.FirstChild(4))
}
