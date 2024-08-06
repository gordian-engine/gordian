package tmconsensustest_test

import (
	"bytes"
	"io"
	"testing"

	"github.com/rollchains/gordian/tm/tmconsensus"
	"github.com/rollchains/gordian/tm/tmconsensus/tmconsensustest"
	"github.com/stretchr/testify/require"
)

// TODO: this should be a SignatureSchemeCompliance test, not hardcoded to SimpleSignatureScheme.
func TestSimpleSignatureScheme(t *testing.T) {
	var s tmconsensustest.SimpleSignatureScheme

	var buf bytes.Buffer

	t.Run("proposal", func(t *testing.T) {
		orig := tmconsensus.Block{
			Height:           10,
			PrevBlockHash:    []byte("proposal"),
			PrevAppStateHash: []byte("state"),
			DataID:           []byte("app"),
		}

		// Get original content.
		buf.Reset()
		_, err := s.WriteProposalSigningContent(&buf, orig, 20, tmconsensus.Annotations{})
		require.NoError(t, err)
		origBytes := bytes.Clone(buf.Bytes())

		// Getting the original content again must match.
		t.Run("sign bytes should be consistent for same input", func(t *testing.T) {
			buf.Reset()
			_, err = s.WriteProposalSigningContent(&buf, orig, 20, tmconsensus.Annotations{})
			require.NoError(t, err)
			require.Equal(t, origBytes, buf.Bytes())
		})

		t.Run("sign bytes change after modifying block", func(t *testing.T) {
			for _, fn := range []func(tmconsensus.Block) tmconsensus.Block{
				func(b tmconsensus.Block) tmconsensus.Block {
					out := b
					out.Height = 1234
					return out
				},
				func(b tmconsensus.Block) tmconsensus.Block {
					out := b
					out.PrevBlockHash = []byte("different block hash")
					return out
				},
				func(b tmconsensus.Block) tmconsensus.Block {
					out := b
					out.PrevAppStateHash = []byte("different app state hash")
					return out
				},
				func(b tmconsensus.Block) tmconsensus.Block {
					out := b
					out.DataID = []byte("different app data ID")
					return out
				},
			} {
				modified := fn(orig)
				buf.Reset()
				_, err := s.WriteProposalSigningContent(&buf, modified, 20, tmconsensus.Annotations{})
				require.NoError(t, err)

				require.NotEqualf(t, origBytes, buf.Bytes(), "sign bytes should have differed for %#v and %#v", orig, modified)
			}
		})

		t.Run("sign bytes change after modifying round", func(t *testing.T) {
			buf.Reset()
			_, err := s.WriteProposalSigningContent(&buf, orig, 21, tmconsensus.Annotations{})
			require.NoError(t, err)

			require.NotEqual(t, origBytes, buf.Bytes(), "sign bytes should have differed after changing proposal round")
		})

		t.Run("sign bytes change after modifying annotations", func(t *testing.T) {
			var seen [][]byte

			for _, tc := range []struct {
				name string
				a    tmconsensus.Annotations
			}{
				{name: "empty but non-nil user annotation", a: tmconsensus.Annotations{User: []byte{}}},
				{name: "populated user annotation", a: tmconsensus.Annotations{User: []byte("user")}},
				{name: "empty but non-nil driver annotation", a: tmconsensus.Annotations{Driver: []byte{}}},
				{name: "populated driver annotation", a: tmconsensus.Annotations{Driver: []byte("driver")}},
				{name: "both driver and user populated", a: tmconsensus.Annotations{User: []byte("user"), Driver: []byte("driver")}},
			} {
				tc := tc
				t.Run(tc.name, func(t *testing.T) {
					buf.Reset()
					_, err := s.WriteProposalSigningContent(&buf, orig, 20, tc.a)
					require.NoError(t, err)

					got := bytes.Clone(buf.Bytes())
					require.NotEqual(t, origBytes, got, "sign bytes should have differed after modifying annotation")

					require.NotContains(t, seen, got)
					seen = append(seen, got)
				})
			}
		})
	})

	t.Run("votes", func(t *testing.T) {
		orig := tmconsensus.VoteTarget{
			Height:    10,
			Round:     20,
			BlockHash: "block_hash",
		}
		for _, tc := range []struct {
			name  string
			fn    func(io.Writer, tmconsensus.VoteTarget) (int, error)
			isNil bool
		}{
			{name: "active precommit", fn: s.WritePrecommitSigningContent},
			{name: "nil precommit", fn: s.WritePrecommitSigningContent, isNil: true},

			{name: "active prevote", fn: s.WritePrevoteSigningContent},
			{name: "nil prevote", fn: s.WritePrevoteSigningContent, isNil: true},
		} {
			fn := tc.fn
			isNil := tc.isNil
			t.Run(tc.name, func(t *testing.T) {
				// Get original content.
				vt := orig
				if isNil {
					vt.BlockHash = ""
				}

				buf.Reset()
				_, err := fn(&buf, vt)
				require.NoError(t, err)
				origBytes := bytes.Clone(buf.Bytes())

				// Getting the original content again must match.
				t.Run("sign bytes should be consistent for same input", func(t *testing.T) {
					buf.Reset()
					_, err = fn(&buf, vt)
					require.NoError(t, err)
					require.Equal(t, origBytes, buf.Bytes())
				})

				t.Run("sign bytes change after modification", func(t *testing.T) {
					cases := []func(tmconsensus.VoteTarget) tmconsensus.VoteTarget{
						func(b tmconsensus.VoteTarget) tmconsensus.VoteTarget {
							out := b
							out.Height = 1234
							return out
						},
						func(b tmconsensus.VoteTarget) tmconsensus.VoteTarget {
							out := b
							out.Round = 2345
							return out
						},
					}
					if !isNil {
						// Only change block hash for active case;
						// nil prevote ignoes those fields.
						cases = append(
							cases,
							func(b tmconsensus.VoteTarget) tmconsensus.VoteTarget {
								out := b
								out.BlockHash = "different block hash"
								return out
							},
						)
					}
					for _, modifier := range cases {
						modified := modifier(orig)
						buf.Reset()
						_, err := fn(&buf, modified)
						require.NoError(t, err)

						require.NotEqualf(t, origBytes, buf.Bytes(), "sign bytes should have differed for %#v and %#v", orig, modified)
					}
				})
			})
		}
	})
}
