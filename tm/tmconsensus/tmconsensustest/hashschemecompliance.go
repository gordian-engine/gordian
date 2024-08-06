package tmconsensustest

import (
	"context"
	"slices"
	"testing"

	"github.com/rollchains/gordian/gcrypto"
	"github.com/rollchains/gordian/tm/tmconsensus"
	"github.com/stretchr/testify/require"
)

// TestHashSchemeCompliance runs the compliance tests for a hashing scheme.
//
// makeDefaultBlock should only be set if the hash scheme has specific restrictions on blocks,
// such as expecting certain values to have exact lengths.
// It is acceptable for makeDefaultBlock to panic on error.
// If makeDefaultBlock is nil, a default block maker function is used.
//
// We currently assume that a hashing scheme value is stateless,
// and so a single value can be used for all tests.
func TestHashSchemeCompliance(
	t *testing.T,
	h tmconsensus.HashScheme,
	p gcrypto.CommonMessageSignatureProofScheme,
	makeDefaultBlock func(gcrypto.CommonMessageSignatureProofScheme) tmconsensus.Block,
) {
	t.Run("Block", func(t *testing.T) {
		if makeDefaultBlock == nil {
			makeDefaultBlock = defaultHashSchemeBlock
		}

		t.Run("validate default block", func(t *testing.T) {
			b := makeDefaultBlock(p)
			if len(b.PrevBlockHash) == 0 {
				t.Errorf("PrevBlockHash must not be empty")
			}
			if len(b.DataID) == 0 {
				t.Errorf("DataID must not be empty")
			}

			// TODO: assert some details of PrevCommitProof.

			if len(b.Validators) < 2 {
				t.Errorf("Validators must have at least two elements, preferably more")
			}
			if len(b.NextValidators) < 2 {
				t.Errorf("NextValidators must have at least two elements, preferably more")
			}
		})

		if t.Failed() {
			t.Fatalf("default block validation failed; cannot continue")
		}

		origHash, err := h.Block(makeDefaultBlock(p))
		require.NoError(t, err)

		t.Run("determinism", func(t *testing.T) {
			t.Parallel()

			b := makeDefaultBlock(p)

			for i := 0; i < 100; i++ {
				bh, err := h.Block(b)
				require.NoError(t, err)

				require.Equalf(t, origHash, bh, "calculated different block hash on attempt %d/100", i+1)
			}
		})

		t.Run("existing hash value is not consulted", func(t *testing.T) {
			b := makeDefaultBlock(p)

			if b.Hash == nil {
				b.Hash = []byte("some hash")
			} else {
				b.Hash = nil
			}

			bh, err := h.Block(b)
			require.NoError(t, err)

			require.Equal(t, origHash, bh, "returned hash should have been the same regardless of existing hash field value")
		})

		t.Run("block fields", func(t *testing.T) {
			t.Parallel()

			tcs := []struct {
				name string
				fn   func(*tmconsensus.Block)
			}{
				{
					name: "PrevBlockHash",
					fn: func(b *tmconsensus.Block) {
						b.PrevBlockHash[0]++
					},
				},
				{
					name: "Height",
					fn: func(b *tmconsensus.Block) {
						b.Height++
					},
				},
				{
					name: "DataID",
					fn: func(b *tmconsensus.Block) {
						b.DataID[0]++
					},
				},

				// TODO: manipulate PrevCommitProof.

				{
					name: "Validators (drop element)",
					fn: func(b *tmconsensus.Block) {
						b.Validators = b.Validators[1:]
					},
				},
				{
					name: "Validators (change power on one element)",
					fn: func(b *tmconsensus.Block) {
						b.Validators[0].Power++
					},
				},
				{
					name: "NextValidators (drop element)",
					fn: func(b *tmconsensus.Block) {
						b.NextValidators = b.NextValidators[1:]
					},
				},
				{
					name: "NextValidators (change power on one element)",
					fn: func(b *tmconsensus.Block) {
						b.NextValidators[0].Power++
					},
				},
				// TODO: change just the ID of one in Validators, NextValidators

			}

			// Use AnnotationCombinations to expand the test cases.
			for _, ac := range AnnotationCombinations() {
				if ac.Annotations.User == nil && ac.Annotations.Driver == nil {
					// Don't add the empty case.
					continue
				}

				tcs = append(tcs, struct {
					name string
					fn   func(*tmconsensus.Block)
				}{
					name: ac.Name,
					fn: func(b *tmconsensus.Block) {
						b.Annotations = ac.Annotations
					},
				})
			}

			for _, tc := range tcs {
				tc := tc
				var seenHashes [][]byte
				t.Run(tc.name, func(t *testing.T) {
					b := makeDefaultBlock(p)
					tc.fn(&b)
					bh, err := h.Block(b)
					require.NoError(t, err)

					require.NotEqualf(t, origHash, bh, "same hash calculated after changing block field %s", tc.name)

					require.NotContainsf(t, seenHashes, bh, "hash for case %q duplicated with some earlier case", tc.name)
					seenHashes = append(seenHashes, bh)
				})
			}
		})
	})

	t.Run("PubKeys", func(t *testing.T) {
		t.Parallel()

		pubKeys := DeterministicValidatorsEd25519(3).PubKeys()
		origHash, err := h.PubKeys(pubKeys)
		require.NoError(t, err)

		t.Run("respects order", func(t *testing.T) {
			// Local clone since we are going to shuffle these.
			keys := slices.Clone(pubKeys)

			keys[0], keys[1] = keys[1], keys[0]
			newHash, err := h.PubKeys(keys)
			require.NoError(t, err)

			require.NotEqual(t, origHash, newHash)
		})

		t.Run("deterministic", func(t *testing.T) {
			for i := 0; i < 10; i++ {
				gotHash, err := h.PubKeys(pubKeys)
				require.NoError(t, err)
				require.Equal(t, origHash, gotHash)
			}
		})
	})

	t.Run("VotePowers", func(t *testing.T) {
		t.Parallel()

		pows := []uint64{1000, 100, 10}
		origHash, err := h.VotePowers(pows)
		require.NoError(t, err)

		t.Run("respects order", func(t *testing.T) {
			// Local clone since we are going to shuffle these.
			powsClone := slices.Clone(pows)

			powsClone[0], powsClone[1] = powsClone[1], powsClone[0]
			newHash, err := h.VotePowers(powsClone)
			require.NoError(t, err)

			require.NotEqual(t, origHash, newHash)
		})

		t.Run("deterministic", func(t *testing.T) {
			for i := 0; i < 10; i++ {
				gotHash, err := h.VotePowers(pows)
				require.NoError(t, err)
				require.Equal(t, origHash, gotHash)
			}
		})
	})
}

func defaultHashSchemeBlock(p gcrypto.CommonMessageSignatureProofScheme) tmconsensus.Block {
	vals := DeterministicValidatorsEd25519(5)

	// All but the last validator; the NextValidators will have all the validators.
	blockValidators := vals[:len(vals)-1].Vals()
	bvPubKeys := vals[:len(vals)-1].PubKeys()

	precommitMsg := []byte("precommit")
	precommitProof, err := p.New([]byte("precommit"), bvPubKeys, "myhash")
	if err != nil {
		panic(err)
	}

	ctx := context.Background()

	sig0, err := vals[0].Signer.Sign(ctx, precommitMsg)
	if err != nil {
		panic(err)
	}
	precommitProof.AddSignature(sig0, bvPubKeys[0])

	// TODO: switch this to precommit with vote extension when available.
	sig2, err := vals[2].Signer.Sign(ctx, precommitMsg)
	if err != nil {
		panic(err)
	}
	precommitProof.AddSignature(sig2, bvPubKeys[2])

	nilPrecommitMsg := []byte("nil_precommit")

	nilPrecommitProof, err := p.New([]byte("nil_precommit"), bvPubKeys, "myhash")
	if err != nil {
		panic(err)
	}

	sig1, err := vals[1].Signer.Sign(ctx, nilPrecommitMsg)
	if err != nil {
		panic(err)
	}
	nilPrecommitProof.AddSignature(sig1, bvPubKeys[1])

	// TODO: switch this to precommit with vote extension when available.
	sig3, err := vals[3].Signer.Sign(ctx, nilPrecommitMsg)
	if err != nil {
		panic(err)
	}
	nilPrecommitProof.AddSignature(sig3, bvPubKeys[3])

	return tmconsensus.Block{
		PrevBlockHash: []byte("previous"),

		Height: 3,

		DataID: []byte("data_id"),

		Validators:     blockValidators,
		NextValidators: vals.Vals(), // One more next validator than current.

		PrevAppStateHash: []byte("prev_app_state"),
	}
}
