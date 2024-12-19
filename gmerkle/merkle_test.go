package gmerkle_test

import (
	"crypto/sha256"
	"fmt"
	"testing"

	"github.com/bits-and-blooms/bitset"
	"github.com/gordian-engine/gordian/gmerkle"
	"github.com/stretchr/testify/require"
)

// Very simple implementation of Scheme.
type sha256Scheme struct {
	M uint8 // Branch factor.

	// If set, and if there are less than M children on the rightmost branch in a row,
	// pad with empty SHA values.
	PadRightmostBranches bool

	// If set, prefix non-leaf hashes with two bytes, the depth and the row-index.
	HashPosition bool
}

func (s sha256Scheme) BranchFactor() uint8 {
	return s.M
}

func (s sha256Scheme) LeafID(idx int, leafData string) ([sha256.Size]byte, error) {
	var in []byte
	if s.HashPosition {
		in = append(in, byte(idx))
	}
	in = append(in, leafData...)
	return sha256.Sum256(in), nil
}

func (s sha256Scheme) BranchID(depth, rowIdx int, childIDs [][sha256.Size]byte) ([sha256.Size]byte, error) {
	h := sha256.New()
	if s.HashPosition {
		h.Write([]byte{byte(depth), byte(rowIdx)})
	}
	for _, id := range childIDs {
		h.Write(id[:])
	}

	if s.PadRightmostBranches && len(childIDs) < int(s.M) {
		pad := sha256.Sum256([]byte{})
		for i := len(childIDs); i < int(s.M); i++ {
			h.Write(pad[:])
		}
	}

	var out [sha256.Size]byte
	_ = h.Sum(out[:0])
	return out, nil
}

func TestTree_RootID(t *testing.T) {
	t.Run("complete first depth, binary tree", func(t *testing.T) {
		leaves := []string{"This", "is", "a", "test."}

		// Manually calculate the shas to discover the root hash.

		d0 := make([][sha256.Size]byte, 4)
		for i, leaf := range leaves {
			d0[i] = sha256.Sum256([]byte(leaf))
		}

		d1 := make([][sha256.Size]byte, 2)
		var in []byte
		in = append(in, d0[0][:]...) // sha256("This")
		in = append(in, d0[1][:]...) // sha256("is")
		d1[0] = sha256.Sum256(in)    // sha256(sha256("This") + sha256("is"))

		in = in[:0]
		in = append(in, d0[2][:]...) // sha256("a")
		in = append(in, d0[3][:]...) // sha256("test.")
		d1[1] = sha256.Sum256(in)    // sha256(sha256("a") + sha256("test."))

		in = in[:0]
		in = append(in, d1[0][:]...)
		in = append(in, d1[1][:]...)
		root := sha256.Sum256(in) // sha256( sha256(sha256("This") + sha256("is")) + sha256(sha256("a") + sha256("test.")) )

		scheme := sha256Scheme{M: 2}
		tree, err := gmerkle.NewTree(scheme, leaves)
		require.NoError(t, err)

		require.Equal(t, root, tree.RootID())
	})

	t.Run("incomplete first depth, binary tree", func(t *testing.T) {
		leaves := []string{"only", "three", "leaves"}

		d0 := make([][sha256.Size]byte, 3)
		for i, leaf := range leaves {
			d0[i] = sha256.Sum256([]byte(leaf))
		}

		var in []byte
		in = append(in, d0[0][:]...) // sha256("only")
		in = append(in, d0[1][:]...) // sha256("three")
		left := sha256.Sum256(in)    // sha256(sha256("only") + sha256("three"))

		in = in[:0]
		in = append(in, left[:]...)
		in = append(in, d0[2][:]...)
		root := sha256.Sum256(in) // sha256( sha256(sha256("only") + sha256("three")) + sha256("leaves") )

		scheme := sha256Scheme{M: 2}
		tree, err := gmerkle.NewTree(scheme, leaves)
		require.NoError(t, err)

		require.Equal(t, root, tree.RootID())
	})

	t.Run("complete first depth, 3-ary tree", func(t *testing.T) {
		leaves := []string{"exactly", "three", "leaves"}

		d0 := make([][sha256.Size]byte, 3)
		for i, leaf := range leaves {
			d0[i] = sha256.Sum256([]byte(leaf))
		}

		var in []byte
		in = append(in, d0[0][:]...) // sha256("exactly")
		in = append(in, d0[1][:]...) // sha256("three")
		in = append(in, d0[2][:]...) // sha256("leaves")
		root := sha256.Sum256(in)    // sha256(sha256("exactly") + sha256("three") + sha256("leaves"))

		scheme := sha256Scheme{M: 3}
		tree, err := gmerkle.NewTree(scheme, leaves)
		require.NoError(t, err)

		require.Equal(t, root, tree.RootID())
	})

	t.Run("incomplete first depth, 3-ary tree", func(t *testing.T) {
		leaves := []string{"just", "two"}

		d0 := make([][sha256.Size]byte, 2)
		for i, leaf := range leaves {
			d0[i] = sha256.Sum256([]byte(leaf))
		}

		var in []byte
		in = append(in, d0[0][:]...) // sha256("just")
		in = append(in, d0[1][:]...) // sha256("two")
		root := sha256.Sum256(in)    // sha256(sha256("just") + sha256("two"))

		scheme := sha256Scheme{M: 3}
		tree, err := gmerkle.NewTree(scheme, leaves)
		require.NoError(t, err)

		require.Equal(t, root, tree.RootID())
	})

	t.Run("complete 3-ary tree", func(t *testing.T) {
		leaves := []string{"one", "two", "three", "four", "five", "six", "seven", "eight", "nine"}

		d0 := make([][sha256.Size]byte, 9)
		for i, leaf := range leaves {
			d0[i] = sha256.Sum256([]byte(leaf))
		}

		d1 := make([][sha256.Size]byte, 3)
		var in []byte
		in = append(in, d0[0][:]...)
		in = append(in, d0[1][:]...)
		in = append(in, d0[2][:]...)
		d1[0] = sha256.Sum256(in) // 1-3

		in = in[:0]
		in = append(in, d0[3][:]...)
		in = append(in, d0[4][:]...)
		in = append(in, d0[5][:]...)
		d1[1] = sha256.Sum256(in) // 4-6

		in = in[:0]
		in = append(in, d0[6][:]...)
		in = append(in, d0[7][:]...)
		in = append(in, d0[8][:]...)
		d1[2] = sha256.Sum256(in) // 7-9

		in = in[:0]
		in = append(in, d1[0][:]...)
		in = append(in, d1[1][:]...)
		in = append(in, d1[2][:]...)
		root := sha256.Sum256(in) // sha256( sha256(1-3) + sha256(4-6) + sha256(7-9) )

		scheme := sha256Scheme{M: 3}
		tree, err := gmerkle.NewTree(scheme, leaves)
		require.NoError(t, err)

		require.Equal(t, root, tree.RootID())
	})

	t.Run("padding, 3-ary tree", func(t *testing.T) {
		leaves := []string{"one", "two", "three", "four"}

		d0 := make([][sha256.Size]byte, 4)
		for i, leaf := range leaves {
			d0[i] = sha256.Sum256([]byte(leaf))
		}

		d1 := make([][sha256.Size]byte, 2)

		var in []byte
		in = append(in, d0[0][:]...)
		in = append(in, d0[1][:]...)
		in = append(in, d0[2][:]...)
		d1[0] = sha256.Sum256(in) // 1-3

		pad := sha256.Sum256([]byte{})
		in = in[:0]
		in = append(in, d1[0][:]...) // 1-3
		in = append(in, d0[3][:]...) // 4
		in = append(in, pad[:]...)
		root := sha256.Sum256(in)

		scheme := sha256Scheme{M: 3, PadRightmostBranches: true}
		tree, err := gmerkle.NewTree(scheme, leaves)
		require.NoError(t, err)

		require.Equal(t, root, tree.RootID())
	})

	t.Run("positional information is passed to scheme's ID functions", func(t *testing.T) {
		leaves := []string{"only", "three", "leaves"}

		d0 := make([][sha256.Size]byte, 3)
		for i, leaf := range leaves {
			in := []byte{byte(i)}
			in = append(in, leaf...)
			d0[i] = sha256.Sum256(in)
		}

		var in []byte
		in = append(in, 1, 0)
		in = append(in, d0[0][:]...) // sha256("only")
		in = append(in, d0[1][:]...) // sha256("three")
		left := sha256.Sum256(in)    // sha256(\x01 \x00 + sha256("only") + sha256("three"))

		in = in[:0]
		in = append(in, 2, 0)
		in = append(in, left[:]...)
		in = append(in, d0[2][:]...)
		root := sha256.Sum256(in)

		scheme := sha256Scheme{M: 2, HashPosition: true}
		tree, err := gmerkle.NewTree(scheme, leaves)
		require.NoError(t, err)

		require.Equal(t, root, tree.RootID())
	})
}

type stringConcatScheme struct{}

func (stringConcatScheme) BranchFactor() uint8 { return 2 }

func (stringConcatScheme) LeafID(_ int, leafData string) (string, error) {
	return leafData, nil
}

func (stringConcatScheme) BranchID(_, _ int, childIDs []string) (string, error) {
	if len(childIDs) == 1 {
		return childIDs[0], nil
	}
	return childIDs[0] + childIDs[1], nil
}

func TestTree_Lookup(t *testing.T) {
	t.Run("complete first row", func(t *testing.T) {
		leaves := []string{"0", "1", "2", "3", "4", "5", "6", "7"}

		tree, err := gmerkle.NewTree(stringConcatScheme{}, leaves)
		require.NoError(t, err)

		for _, tc := range []struct {
			search string
			start  int
			n      int
		}{
			{search: "x", start: -1, n: 0},

			{search: "0", start: 0, n: 1},
			{search: "01", start: 0, n: 2},
			{search: "0123", start: 0, n: 4},
			{search: "01234567", start: 0, n: 8},

			{search: "1", start: 1, n: 1},
			{search: "12", start: -1, n: 0}, // Partial prefix, not found.

			{search: "2", start: 2, n: 1},
			{search: "23", start: 2, n: 2},

			{search: "3", start: 3, n: 1},

			{search: "4", start: 4, n: 1},
			{search: "45", start: 4, n: 2},
			{search: "456", start: -1, n: 0}, // Partial prefix, not found.
			{search: "4567", start: 4, n: 4},

			{search: "5", start: 5, n: 1},

			{search: "6", start: 6, n: 1},
			{search: "67", start: 6, n: 2},

			{search: "7", start: 7, n: 1},
		} {
			start, n := tree.Lookup(tc.search)
			require.Equal(t, tc.start, start, "incorrect start value when searching "+tc.search)
			require.Equal(t, tc.n, n, "incorrect n value when searching "+tc.search)
		}
	})

	t.Run("incomplete first row", func(t *testing.T) {
		leaves := []string{"0", "1", "2", "3", "4", "5", "6"}

		tree, err := gmerkle.NewTree(stringConcatScheme{}, leaves)
		require.NoError(t, err)

		for _, tc := range []struct {
			search string
			start  int
			n      int
		}{
			{search: "x", start: -1, n: 0},

			{search: "0", start: 0, n: 1},
			{search: "01", start: 0, n: 2},
			{search: "0123", start: 0, n: 4},
			{search: "0123456", start: 0, n: 7},

			{search: "1", start: 1, n: 1},
			{search: "12", start: -1, n: 0}, // Partial prefix, not found.

			{search: "2", start: 2, n: 1},
			{search: "23", start: 2, n: 2},

			{search: "3", start: 3, n: 1},

			{search: "4", start: 4, n: 1},
			{search: "45", start: 4, n: 2},
			{search: "456", start: 4, n: 3},

			{search: "5", start: 5, n: 1},

			{search: "6", start: 6, n: 1},
		} {
			start, n := tree.Lookup(tc.search)
			require.Equal(t, tc.start, start, "incorrect start value when searching "+tc.search)
			require.Equal(t, tc.n, n, "incorrect n value when searching "+tc.search)
		}
	})
}

type visitCall struct {
	id             string
	depth, rowIdx  int
	childBranchIDs []string
	leafIdx        int
	nLeaves        int
}

type visitor struct {
	Calls []visitCall
}

func (v *visitor) Visit(id string, depth, rowIdx int, childBranchIDs []string, leafIdx, nLeaves int) (stop bool) {
	v.Calls = append(v.Calls, visitCall{
		id:             id,
		depth:          depth,
		rowIdx:         rowIdx,
		childBranchIDs: childBranchIDs,
		leafIdx:        leafIdx,
		nLeaves:        nLeaves,
	})
	return false
}

func TestTree_WalkFromRootB(t *testing.T) {
	t.Run("full, balanced, binary tree", func(t *testing.T) {
		leaves := []string{"0", "1", "2", "3", "4", "5", "6", "7"}

		tree, err := gmerkle.NewTree(stringConcatScheme{}, leaves)
		require.NoError(t, err)

		var v visitor
		tree.WalkFromRootB(v.Visit)

		require.Len(t, v.Calls, 1+2+4+8)

		require.Equal(t, visitCall{
			id:             "01234567",
			depth:          3,
			rowIdx:         0,
			childBranchIDs: []string{"0123", "4567"},
			leafIdx:        0,
			nLeaves:        8,
		}, v.Calls[0])

		require.Equal(t, visitCall{
			id:             "0123",
			depth:          2,
			rowIdx:         0,
			childBranchIDs: []string{"01", "23"},
			leafIdx:        0,
			nLeaves:        4,
		}, v.Calls[1])

		require.Equal(t, visitCall{
			id:             "4567",
			depth:          2,
			rowIdx:         1,
			childBranchIDs: []string{"45", "67"},
			leafIdx:        4,
			nLeaves:        4,
		}, v.Calls[2])

		require.Equal(t, visitCall{
			id:             "01",
			depth:          1,
			rowIdx:         0,
			childBranchIDs: []string{"0", "1"},
			leafIdx:        0,
			nLeaves:        2,
		}, v.Calls[3])

		require.Equal(t, visitCall{
			id:             "23",
			depth:          1,
			rowIdx:         1,
			childBranchIDs: []string{"2", "3"},
			leafIdx:        2,
			nLeaves:        2,
		}, v.Calls[4])

		require.Equal(t, visitCall{
			id:             "45",
			depth:          1,
			rowIdx:         2,
			childBranchIDs: []string{"4", "5"},
			leafIdx:        4,
			nLeaves:        2,
		}, v.Calls[5])

		require.Equal(t, visitCall{
			id:             "67",
			depth:          1,
			rowIdx:         3,
			childBranchIDs: []string{"6", "7"},
			leafIdx:        6,
			nLeaves:        2,
		}, v.Calls[6])

		for i := 0; i < 8; i++ {
			require.Equal(t, visitCall{
				id:             fmt.Sprintf("%d", i),
				depth:          0,
				rowIdx:         i,
				childBranchIDs: nil,
				leafIdx:        i,
				nLeaves:        1,
			}, v.Calls[7+i])
		}
	})

	t.Run("tree with orphan", func(t *testing.T) {
		leaves := []string{"0", "1", "2", "3", "4"}

		tree, err := gmerkle.NewTree(stringConcatScheme{}, leaves)
		require.NoError(t, err)

		var v visitor
		tree.WalkFromRootB(v.Visit)
		for i, vc := range v.Calls {
			t.Logf("%d) %#v", i, vc)
		}

		// require.Len(t, v.Calls, 1+2+4+8)

		require.Equal(t, visitCall{
			id:             "01234",
			depth:          3,
			rowIdx:         0,
			childBranchIDs: []string{"0123", "4"},
			leafIdx:        0,
			nLeaves:        5,
		}, v.Calls[0])

		require.Equal(t, visitCall{
			id:             "0123",
			depth:          2,
			rowIdx:         0,
			childBranchIDs: []string{"01", "23"},
			leafIdx:        0,
			nLeaves:        4,
		}, v.Calls[1])

		require.Equal(t, visitCall{
			id:             "4",
			depth:          2,
			rowIdx:         1,
			childBranchIDs: nil,
			leafIdx:        4,
			nLeaves:        1,
		}, v.Calls[2])

		require.Equal(t, visitCall{
			id:             "01",
			depth:          1,
			rowIdx:         0,
			childBranchIDs: []string{"0", "1"},
			leafIdx:        0,
			nLeaves:        2,
		}, v.Calls[3])

		require.Equal(t, visitCall{
			id:             "23",
			depth:          1,
			rowIdx:         1,
			childBranchIDs: []string{"2", "3"},
			leafIdx:        2,
			nLeaves:        2,
		}, v.Calls[4])

		for i := 0; i < 4; i++ {
			require.Equal(t, visitCall{
				id:             fmt.Sprintf("%d", i),
				depth:          0,
				rowIdx:         i,
				childBranchIDs: nil,
				leafIdx:        i,
				nLeaves:        1,
			}, v.Calls[5+i])
		}
	})

	t.Run("single element tree", func(t *testing.T) {
		leaves := []string{"alone"}

		tree, err := gmerkle.NewTree(stringConcatScheme{}, leaves)
		require.NoError(t, err)

		var v visitor
		tree.WalkFromRootB(v.Visit)

		require.Len(t, v.Calls, 1)

		require.Equal(t, visitCall{
			id:             "alone",
			depth:          0,
			rowIdx:         0,
			childBranchIDs: nil,
			leafIdx:        0,
			nLeaves:        1,
		}, v.Calls[0])
	})
}

func TestTree_WalkFromRootD(t *testing.T) {
	t.Run("full, balanced, binary tree", func(t *testing.T) {
		leaves := []string{"0", "1", "2", "3", "4", "5", "6", "7"}

		tree, err := gmerkle.NewTree(stringConcatScheme{}, leaves)
		require.NoError(t, err)

		var v visitor
		tree.WalkFromRootD(v.Visit)

		require.Len(t, v.Calls, 1+2+4+8)

		require.Equal(t, visitCall{
			id:             "01234567",
			depth:          3,
			rowIdx:         0,
			childBranchIDs: []string{"0123", "4567"},
			leafIdx:        0,
			nLeaves:        8,
		}, v.Calls[0])

		require.Equal(t, visitCall{
			id:             "0123",
			depth:          2,
			rowIdx:         0,
			childBranchIDs: []string{"01", "23"},
			leafIdx:        0,
			nLeaves:        4,
		}, v.Calls[1])

		require.Equal(t, visitCall{
			id:             "01",
			depth:          1,
			rowIdx:         0,
			childBranchIDs: []string{"0", "1"},
			leafIdx:        0,
			nLeaves:        2,
		}, v.Calls[2])

		require.Equal(t, visitCall{
			id:             "0",
			depth:          0,
			rowIdx:         0,
			childBranchIDs: nil,
			leafIdx:        0,
			nLeaves:        1,
		}, v.Calls[3])

		require.Equal(t, visitCall{
			id:             "1",
			depth:          0,
			rowIdx:         1,
			childBranchIDs: nil,
			leafIdx:        1,
			nLeaves:        1,
		}, v.Calls[4])

		require.Equal(t, visitCall{
			id:             "23",
			depth:          1,
			rowIdx:         1,
			childBranchIDs: []string{"2", "3"},
			leafIdx:        2,
			nLeaves:        2,
		}, v.Calls[5])

		require.Equal(t, visitCall{
			id:             "2",
			depth:          0,
			rowIdx:         2,
			childBranchIDs: nil,
			leafIdx:        2,
			nLeaves:        1,
		}, v.Calls[6])

		require.Equal(t, visitCall{
			id:             "3",
			depth:          0,
			rowIdx:         3,
			childBranchIDs: nil,
			leafIdx:        3,
			nLeaves:        1,
		}, v.Calls[7])

		require.Equal(t, visitCall{
			id:             "4567",
			depth:          2,
			rowIdx:         1,
			childBranchIDs: []string{"45", "67"},
			leafIdx:        4,
			nLeaves:        4,
		}, v.Calls[8])

		require.Equal(t, visitCall{
			id:             "45",
			depth:          1,
			rowIdx:         2,
			childBranchIDs: []string{"4", "5"},
			leafIdx:        4,
			nLeaves:        2,
		}, v.Calls[9])

		require.Equal(t, visitCall{
			id:             "4",
			depth:          0,
			rowIdx:         4,
			childBranchIDs: nil,
			leafIdx:        4,
			nLeaves:        1,
		}, v.Calls[10])

		require.Equal(t, visitCall{
			id:             "5",
			depth:          0,
			rowIdx:         5,
			childBranchIDs: nil,
			leafIdx:        5,
			nLeaves:        1,
		}, v.Calls[11])

		require.Equal(t, visitCall{
			id:             "67",
			depth:          1,
			rowIdx:         3,
			childBranchIDs: []string{"6", "7"},
			leafIdx:        6,
			nLeaves:        2,
		}, v.Calls[12])

		require.Equal(t, visitCall{
			id:             "6",
			depth:          0,
			rowIdx:         6,
			childBranchIDs: nil,
			leafIdx:        6,
			nLeaves:        1,
		}, v.Calls[13])

		require.Equal(t, visitCall{
			id:             "7",
			depth:          0,
			rowIdx:         7,
			childBranchIDs: nil,
			leafIdx:        7,
			nLeaves:        1,
		}, v.Calls[14])
	})

	t.Run("tree with orphan", func(t *testing.T) {
		leaves := []string{"0", "1", "2", "3", "4"}

		tree, err := gmerkle.NewTree(stringConcatScheme{}, leaves)
		require.NoError(t, err)

		var v visitor
		tree.WalkFromRootD(v.Visit)

		// require.Len(t, v.Calls, 1+2+4+8)

		require.Equal(t, visitCall{
			id:             "01234",
			depth:          3,
			rowIdx:         0,
			childBranchIDs: []string{"0123", "4"},
			leafIdx:        0,
			nLeaves:        5,
		}, v.Calls[0])

		require.Equal(t, visitCall{
			id:             "0123",
			depth:          2,
			rowIdx:         0,
			childBranchIDs: []string{"01", "23"},
			leafIdx:        0,
			nLeaves:        4,
		}, v.Calls[1])

		require.Equal(t, visitCall{
			id:             "01",
			depth:          1,
			rowIdx:         0,
			childBranchIDs: []string{"0", "1"},
			leafIdx:        0,
			nLeaves:        2,
		}, v.Calls[2])

		require.Equal(t, visitCall{
			id:             "0",
			depth:          0,
			rowIdx:         0,
			childBranchIDs: nil,
			leafIdx:        0,
			nLeaves:        1,
		}, v.Calls[3])

		require.Equal(t, visitCall{
			id:             "1",
			depth:          0,
			rowIdx:         1,
			childBranchIDs: nil,
			leafIdx:        1,
			nLeaves:        1,
		}, v.Calls[4])

		require.Equal(t, visitCall{
			id:             "23",
			depth:          1,
			rowIdx:         1,
			childBranchIDs: []string{"2", "3"},
			leafIdx:        2,
			nLeaves:        2,
		}, v.Calls[5])

		require.Equal(t, visitCall{
			id:             "2",
			depth:          0,
			rowIdx:         2,
			childBranchIDs: nil,
			leafIdx:        2,
			nLeaves:        1,
		}, v.Calls[6])

		require.Equal(t, visitCall{
			id:             "3",
			depth:          0,
			rowIdx:         3,
			childBranchIDs: nil,
			leafIdx:        3,
			nLeaves:        1,
		}, v.Calls[7])

		require.Equal(t, visitCall{
			id:             "4",
			depth:          2,
			rowIdx:         1,
			childBranchIDs: nil,
			leafIdx:        4,
			nLeaves:        1,
		}, v.Calls[8])
	})

	t.Run("single element tree", func(t *testing.T) {
		leaves := []string{"alone"}

		tree, err := gmerkle.NewTree(stringConcatScheme{}, leaves)
		require.NoError(t, err)

		var v visitor
		tree.WalkFromRootD(v.Visit)

		require.Len(t, v.Calls, 1)

		require.Equal(t, visitCall{
			id:             "alone",
			depth:          0,
			rowIdx:         0,
			childBranchIDs: nil,
			leafIdx:        0,
			nLeaves:        1,
		}, v.Calls[0])
	})
}

func TestTree_BitSetToIDs(t *testing.T) {
	t.Run("balanced tree", func(t *testing.T) {
		leaves := []string{"0", "1", "2", "3", "4", "5", "6", "7"}

		tree, err := gmerkle.NewTree(stringConcatScheme{}, leaves)
		require.NoError(t, err)

		for _, tc := range []struct {
			in     uint64
			expIDs []string
		}{
			{in: 0b11111111, expIDs: []string{"01234567"}},

			{in: 0b00000001, expIDs: []string{"0"}},
			{in: 0b00000011, expIDs: []string{"01"}},
			{in: 0b00001111, expIDs: []string{"0123"}},

			{in: 0b00010000, expIDs: []string{"4"}},
			{in: 0b00110000, expIDs: []string{"45"}},
			{in: 0b11110000, expIDs: []string{"4567"}},

			{in: 0b00001101, expIDs: []string{"23", "0"}},

			{in: 0b00011111, expIDs: []string{"0123", "4"}},
			{in: 0b00111111, expIDs: []string{"0123", "45"}},
			{in: 0b00101111, expIDs: []string{"0123", "5"}},

			{in: 0b11110001, expIDs: []string{"4567", "0"}},
			{in: 0b11110010, expIDs: []string{"4567", "1"}},
			{in: 0b11110100, expIDs: []string{"4567", "2"}},
			{in: 0b11111100, expIDs: []string{"4567", "23"}},
			{in: 0b11111101, expIDs: []string{"4567", "23", "0"}},

			{in: 0b01010101, expIDs: []string{"0", "2", "4", "6"}},
			{in: 0b10101010, expIDs: []string{"1", "3", "5", "7"}},

			{in: 0b00111100, expIDs: []string{"23", "45"}},

			{in: 0, expIDs: nil},
		} {
			b := bitset.From([]uint64{tc.in})
			gotIDs := tree.BitSetToIDs(b)
			require.Equalf(t, tc.expIDs, gotIDs, "with input %b", tc.in)
		}
	})

	t.Run("unbalanced tree", func(t *testing.T) {
		leaves := []string{"0", "1", "2", "3", "4"}

		tree, err := gmerkle.NewTree(stringConcatScheme{}, leaves)
		require.NoError(t, err)

		for _, tc := range []struct {
			in     uint64
			expIDs []string
		}{
			{in: 0b11111, expIDs: []string{"01234"}},

			{in: 0b10000, expIDs: []string{"4"}},

			// These are unintuitive results.
			// Note, the layout of the tree looks like:
			/*
			   01234
			      0123
			         01
			            [L=0] 0
			            [L=1] 1
			         23
			            [L=2] 2
			            [L=3] 3
			      [L=4] 4
			*/
			// When we traverse depth first, we observe that element 4 must be included
			// before we determine what subset of 0123 should be included.
			// Although unintuitive, this should be fine so long as it is consistent.
			{in: 0b10001, expIDs: []string{"4", "0"}},
			{in: 0b10011, expIDs: []string{"4", "01"}},
			{in: 0b10100, expIDs: []string{"4", "2"}},
			{in: 0b11100, expIDs: []string{"4", "23"}},
			{in: 0b11110, expIDs: []string{"4", "23", "1"}},
			{in: 0b11101, expIDs: []string{"4", "23", "0"}},
		} {
			b := bitset.From([]uint64{tc.in})
			gotIDs := tree.BitSetToIDs(b)
			require.Equalf(t, tc.expIDs, gotIDs, "with input %b", tc.in)
		}
	})
}
