package tmconsensustest

import (
	"context"
	"slices"
	"testing"

	"github.com/gordian-engine/gordian/gcrypto"
	"github.com/gordian-engine/gordian/tm/tmconsensus"
	"github.com/stretchr/testify/require"
)

// TestHashSchemeCompliance runs the compliance tests for a hashing scheme.
//
// makeDefaultHeader should only be set if the hash scheme has specific restrictions on block headers,
// such as expecting certain values to have exact lengths.
// It is acceptable for makeDefaultHeader to panic on error.
// If makeDefaultHeader is nil, a default header maker function is used.
//
// We currently assume that a hashing scheme value is stateless,
// and so a single value can be used for all tests.
func TestHashSchemeCompliance(
	t *testing.T,
	hs tmconsensus.HashScheme,
	p gcrypto.CommonMessageSignatureProofScheme,
	makeDefaultHeader func(gcrypto.CommonMessageSignatureProofScheme) tmconsensus.Header,
) {
	t.Run("Block", func(t *testing.T) {
		if makeDefaultHeader == nil {
			makeDefaultHeader = defaultHashSchemeHeader
		}

		t.Run("validate default block", func(t *testing.T) {
			h := makeDefaultHeader(p)
			if len(h.PrevBlockHash) == 0 {
				t.Errorf("PrevBlockHash must not be empty")
			}
			if len(h.DataID) == 0 {
				t.Errorf("DataID must not be empty")
			}

			// TODO: assert some details of PrevCommitProof.

			if len(h.ValidatorSet.Validators) < 2 {
				t.Errorf("Validators must have at least two elements, preferably more")
			}
			if len(h.NextValidatorSet.Validators) < 2 {
				t.Errorf("NextValidators must have at least two elements, preferably more")
			}
		})

		if t.Failed() {
			t.Fatalf("default block validation failed; cannot continue")
		}

		h := makeDefaultHeader(p)
		var err error
		h.ValidatorSet, err = tmconsensus.NewValidatorSet(h.ValidatorSet.Validators, hs)
		require.NoError(t, err)
		h.NextValidatorSet, err = tmconsensus.NewValidatorSet(h.NextValidatorSet.Validators, hs)
		require.NoError(t, err)

		origHash, err := hs.Block(h)
		require.NoError(t, err)

		t.Run("determinism", func(t *testing.T) {
			t.Parallel()

			h := makeDefaultHeader(p)
			var err error
			h.ValidatorSet, err = tmconsensus.NewValidatorSet(h.ValidatorSet.Validators, hs)
			require.NoError(t, err)
			h.NextValidatorSet, err = tmconsensus.NewValidatorSet(h.NextValidatorSet.Validators, hs)
			require.NoError(t, err)

			for i := 0; i < 100; i++ {
				bh, err := hs.Block(h)
				require.NoError(t, err)

				require.Equalf(t, origHash, bh, "calculated different block hash on attempt %d/100", i+1)
			}
		})

		t.Run("existing hash value is not consulted", func(t *testing.T) {
			h := makeDefaultHeader(p)
			var err error
			h.ValidatorSet, err = tmconsensus.NewValidatorSet(h.ValidatorSet.Validators, hs)
			require.NoError(t, err)
			h.NextValidatorSet, err = tmconsensus.NewValidatorSet(h.NextValidatorSet.Validators, hs)
			require.NoError(t, err)

			if h.Hash == nil {
				h.Hash = []byte("some hash")
			} else {
				h.Hash = nil
			}

			bh, err := hs.Block(h)
			require.NoError(t, err)

			require.Equal(t, origHash, bh, "returned hash should have been the same regardless of existing hash field value")
		})

		t.Run("block fields", func(t *testing.T) {
			t.Parallel()

			tcs := []struct {
				name string
				fn   func(*tmconsensus.Header)
			}{
				{
					name: "PrevBlockHash",
					fn: func(h *tmconsensus.Header) {
						h.PrevBlockHash[0]++
					},
				},
				{
					name: "Height",
					fn: func(h *tmconsensus.Header) {
						h.Height++
					},
				},
				{
					name: "DataID",
					fn: func(h *tmconsensus.Header) {
						h.DataID[0]++
					},
				},

				// TODO: manipulate PrevCommitProof.

				{
					name: "Validators (drop element)",
					fn: func(h *tmconsensus.Header) {
						updatedVS := h.ValidatorSet
						updatedVS.Validators = updatedVS.Validators[1:]
						h.ValidatorSet = updatedVS
					},
				},
				{
					name: "Validators (change power on one element)",
					fn: func(h *tmconsensus.Header) {
						h.ValidatorSet.Validators[0].Power++
					},
				},
				{
					name: "NextValidators (drop element)",
					fn: func(h *tmconsensus.Header) {
						updatedVS := h.NextValidatorSet
						updatedVS.Validators = updatedVS.Validators[1:]
						h.NextValidatorSet = updatedVS
					},
				},
				{
					name: "NextValidators (change power on one element)",
					fn: func(h *tmconsensus.Header) {
						h.NextValidatorSet.Validators[0].Power++
					},
				},
			}

			// Use AnnotationCombinations to expand the test cases.
			for _, ac := range AnnotationCombinations() {
				if ac.Annotations.User == nil && ac.Annotations.Driver == nil {
					// Don't add the empty case.
					continue
				}

				tcs = append(tcs, struct {
					name string
					fn   func(*tmconsensus.Header)
				}{
					name: ac.Name,
					fn: func(h *tmconsensus.Header) {
						h.Annotations = ac.Annotations
					},
				})
			}

			for _, tc := range tcs {
				tc := tc
				var seenHashes [][]byte
				t.Run(tc.name, func(t *testing.T) {
					h := makeDefaultHeader(p)
					tc.fn(&h)
					bh, err := hs.Block(h)
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
		origHash, err := hs.PubKeys(pubKeys)
		require.NoError(t, err)

		t.Run("respects order", func(t *testing.T) {
			// Local clone since we are going to shuffle these.
			keys := slices.Clone(pubKeys)

			keys[0], keys[1] = keys[1], keys[0]
			newHash, err := hs.PubKeys(keys)
			require.NoError(t, err)

			require.NotEqual(t, origHash, newHash)
		})

		t.Run("deterministic", func(t *testing.T) {
			for i := 0; i < 10; i++ {
				gotHash, err := hs.PubKeys(pubKeys)
				require.NoError(t, err)
				require.Equal(t, origHash, gotHash)
			}
		})
	})

	t.Run("VotePowers", func(t *testing.T) {
		t.Parallel()

		pows := []uint64{1000, 100, 10}
		origHash, err := hs.VotePowers(pows)
		require.NoError(t, err)

		t.Run("respects order", func(t *testing.T) {
			// Local clone since we are going to shuffle these.
			powsClone := slices.Clone(pows)

			powsClone[0], powsClone[1] = powsClone[1], powsClone[0]
			newHash, err := hs.VotePowers(powsClone)
			require.NoError(t, err)

			require.NotEqual(t, origHash, newHash)
		})

		t.Run("deterministic", func(t *testing.T) {
			for i := 0; i < 10; i++ {
				gotHash, err := hs.VotePowers(pows)
				require.NoError(t, err)
				require.Equal(t, origHash, gotHash)
			}
		})
	})
}

func defaultHashSchemeHeader(p gcrypto.CommonMessageSignatureProofScheme) tmconsensus.Header {
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

	return tmconsensus.Header{
		PrevBlockHash: []byte("previous"),

		Height: 3,

		DataID: []byte("data_id"),

		// We don't have a hash scheme yet at this point in the test,
		// so we make invalid ValidatorSet values lacking hashes.
		ValidatorSet: tmconsensus.ValidatorSet{
			Validators: blockValidators,
		},
		NextValidatorSet: tmconsensus.ValidatorSet{
			Validators: vals.Vals(), // One more next validator than current.
		},

		PrevAppStateHash: []byte("prev_app_state"),
	}
}
