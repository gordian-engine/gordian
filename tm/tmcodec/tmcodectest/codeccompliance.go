package tmcodectest

import (
	"context"
	"testing"

	"github.com/gordian-engine/gordian/gcrypto"
	"github.com/gordian-engine/gordian/tm/tmcodec"
	"github.com/gordian-engine/gordian/tm/tmconsensus"
	"github.com/gordian-engine/gordian/tm/tmconsensus/tmconsensustest"
	"github.com/stretchr/testify/require"
)

const determinismTries = 50

// In case there is any state in the codec,
// providing a factory function allows a clean start for every subtest.
type MarshalCodecFactory func() tmcodec.MarshalCodec

// TestMarshalCodecCompliance ensures the codec in mcf
// follows all expected properties of a [tmcodec.MarshalCodec].
func TestMarshalCodecCompliance(t *testing.T, mcf MarshalCodecFactory) {
	ctx := context.Background()

	t.Run("proposed headers, plain headers, and committed headers", func(t *testing.T) {
		for _, useInitialHeight := range []bool{true, false} {
			useInitialHeight := useInitialHeight

			outerName := "past initial height"
			if useInitialHeight {
				outerName = "at initial height"
			}

			t.Run(outerName, func(t *testing.T) {
				t.Run("round trip", func(t *testing.T) {
					t.Parallel()

					const curVals = 4
					fx := tmconsensustest.NewEd25519Fixture(curVals)
					getPH := func() (tmconsensus.ProposedHeader, tmconsensus.Header) {
						ph := fx.NextProposedHeader([]byte("app_data"), 0)

						// Add a validator to the next validator set,
						// to ensure we are correctly serializing and deserializing that set
						// separately from the current validators.
						var err error
						ph.Header.NextValidatorSet, err = tmconsensus.NewValidatorSet(
							tmconsensustest.DeterministicValidatorsEd25519(curVals+1).Vals(),
							fx.HashScheme,
						)
						require.NoError(t, err)
						fx.RecalculateHash(&ph.Header)

						fx.SignProposal(ctx, &ph, 0)

						prevHeader := ph.Header

						if !useInitialHeight {
							vt := tmconsensus.VoteTarget{Height: 1, Round: 0, BlockHash: string(ph.Header.Hash)}
							fx.CommitBlock(ph.Header, []byte("app_hash"), 0, map[string]gcrypto.CommonMessageSignatureProof{
								string(ph.Header.Hash): fx.PrecommitSignatureProof(ctx, vt, nil, []int{0, 1, 2, 3}),
							})

							ph = fx.NextProposedHeader([]byte("app_data_2"), 0)
							fx.SignProposal(ctx, &ph, 0)
						}
						return ph, prevHeader
					}

					mc := mcf()

					t.Run("full proposed header", func(t *testing.T) {
						for _, ac := range tmconsensustest.AnnotationCombinations() {
							ac := ac
							t.Run(ac.Name, func(t *testing.T) {
								ph, prevHeader := getPH()
								if !useInitialHeight {
									activePrecommit := fx.PrecommitSignatureProof(
										ctx,
										tmconsensus.VoteTarget{Height: 1, Round: 0, BlockHash: string(prevHeader.Hash)},
										nil,
										[]int{0, 1, 2},
									)
									nilPrecommit := fx.PrecommitSignatureProof(
										ctx,
										tmconsensus.VoteTarget{Height: 1, Round: 0},
										nil,
										[]int{3},
									)

									proof := tmconsensus.CommitProof{
										Round: 0,

										PubKeyHash: nilPrecommit.AsSparse().PubKeyHash,

										Proofs: map[string][]gcrypto.SparseSignature{
											string(prevHeader.Hash): activePrecommit.AsSparse().Signatures,
											"":                      nilPrecommit.AsSparse().Signatures,
										},
									}
									ph.Header.PrevCommitProof = proof

									ph.Annotations = ac.Annotations
								}

								b, err := mc.MarshalProposedHeader(ph)
								require.NoError(t, err)

								var got tmconsensus.ProposedHeader
								require.NoError(t, mc.UnmarshalProposedHeader(b, &got))

								require.Equal(t, ph, got)
							})
						}
					})

					t.Run("replayed proposed header", func(t *testing.T) {
						for _, ac := range tmconsensustest.AnnotationCombinations() {
							ac := ac
							t.Run(ac.Name, func(t *testing.T) {
								ph, prevHeader := getPH()

								// Replayed proposed headers are missing these two fields,
								// so we need to ensure they are still marshaled correctly.
								ph.ProposerPubKey = nil
								ph.Signature = nil

								if !useInitialHeight {
									activePrecommit := fx.PrecommitSignatureProof(
										ctx,
										tmconsensus.VoteTarget{Height: 1, Round: 0, BlockHash: string(prevHeader.Hash)},
										nil,
										[]int{0, 1, 2},
									)
									nilPrecommit := fx.PrecommitSignatureProof(
										ctx,
										tmconsensus.VoteTarget{Height: 1, Round: 0},
										nil,
										[]int{3},
									)

									proof := tmconsensus.CommitProof{
										Round: 0,

										PubKeyHash: nilPrecommit.AsSparse().PubKeyHash,

										Proofs: map[string][]gcrypto.SparseSignature{
											string(prevHeader.Hash): activePrecommit.AsSparse().Signatures,
											"":                      nilPrecommit.AsSparse().Signatures,
										},
									}
									ph.Header.PrevCommitProof = proof

									ph.Annotations = ac.Annotations
								}

								b, err := mc.MarshalProposedHeader(ph)
								require.NoError(t, err)

								var got tmconsensus.ProposedHeader
								require.NoError(t, mc.UnmarshalProposedHeader(b, &got))

								require.Equal(t, ph, got)
							})
						}
					})

					t.Run("plain header", func(t *testing.T) {
						ph, _ := getPH()
						b, err := mc.MarshalHeader(ph.Header)
						require.NoError(t, err)

						var got tmconsensus.Header
						require.NoError(t, mc.UnmarshalHeader(b, &got))

						require.Equal(t, ph.Header, got)
					})

					t.Run("plain header with annotations", func(t *testing.T) {
						ph, _ := getPH()
						// Just move the proposed header's annotations over the plain header's.
						ph.Header.Annotations, ph.Annotations = ph.Annotations, tmconsensus.Annotations{}

						b, err := mc.MarshalHeader(ph.Header)
						require.NoError(t, err)

						var got tmconsensus.Header
						require.NoError(t, mc.UnmarshalHeader(b, &got))

						require.Equal(t, ph.Header, got)
					})

					t.Run("committed header", func(t *testing.T) {
						ph, _ := getPH()

						fx := tmconsensustest.NewEd25519Fixture(curVals)
						voteMap := map[string][]int{
							string(ph.Header.Hash): {0, 1, 2},
							"":                     {3}, // Include another vote that isn't for the block.
						}
						precommitProofs := fx.PrecommitProofMap(ctx, ph.Header.Height, 0, voteMap)
						fx.CommitBlock(ph.Header, []byte("app_state_1"), 0, precommitProofs)

						nextPH := fx.NextProposedHeader([]byte("whatever"), 0)

						committedHeader := tmconsensus.CommittedHeader{
							Header: ph.Header,
							Proof:  nextPH.Header.PrevCommitProof,
						}

						b, err := mc.MarshalCommittedHeader(committedHeader)
						require.NoError(t, err)

						var got tmconsensus.CommittedHeader
						require.NoError(t, mc.UnmarshalCommittedHeader(b, &got))

						require.Equal(t, committedHeader, got)
					})
				})

				t.Run("determinism", func(t *testing.T) {
					t.Parallel()

					fx := tmconsensustest.NewEd25519Fixture(8)

					ph := fx.NextProposedHeader([]byte("app_data"), 0)
					fx.SignProposal(ctx, &ph, 0)

					if !useInitialHeight {
						vt := tmconsensus.VoteTarget{Height: 1, Round: 0, BlockHash: string(ph.Header.Hash)}
						fx.CommitBlock(ph.Header, []byte("app_hash"), 0, map[string]gcrypto.CommonMessageSignatureProof{
							string(ph.Header.Hash): fx.PrecommitSignatureProof(ctx, vt, nil, []int{0, 1, 2, 3}),
						})

						ph = fx.NextProposedHeader([]byte("app_data_2"), 0)
						fx.SignProposal(ctx, &ph, 0)
					}

					mc := mcf()

					t.Run("proposed block", func(t *testing.T) {
						orig, err := mc.MarshalProposedHeader(ph)
						require.NoError(t, err)

						for i := 0; i < determinismTries; i++ {
							got, err := mc.MarshalProposedHeader(ph)
							require.NoError(t, err)

							require.Equal(t, orig, got)
						}
					})

					t.Run("plain block", func(t *testing.T) {
						orig, err := mc.MarshalHeader(ph.Header)
						require.NoError(t, err)

						for i := 0; i < determinismTries; i++ {
							got, err := mc.MarshalHeader(ph.Header)
							require.NoError(t, err)

							require.Equal(t, orig, got)
						}
					})
				})
			})
		}
	})

	t.Run("prevote proofs", func(t *testing.T) {
		t.Run("round trip", func(t *testing.T) {
			t.Parallel()

			fx := tmconsensustest.NewEd25519Fixture(8)

			ph := fx.NextProposedHeader([]byte("app_data"), 0)

			vt := tmconsensus.VoteTarget{Height: 1, Round: 0, BlockHash: string(ph.Header.Hash)}
			nilVT := tmconsensus.VoteTarget{Height: 1, Round: 0}
			fullProof := map[string]gcrypto.CommonMessageSignatureProof{
				string(ph.Header.Hash): fx.PrevoteSignatureProof(ctx, vt, nil, []int{0, 1, 2, 3}),
				"":                     fx.PrevoteSignatureProof(ctx, nilVT, nil, []int{4, 5, 6, 7}),
			}

			proof, err := tmconsensus.PrevoteSparseProofFromFullProof(1, 0, fullProof)
			require.NoError(t, err)

			mc := mcf()
			b, err := mc.MarshalPrevoteProof(proof)
			require.NoError(t, err)

			var got tmconsensus.PrevoteSparseProof
			require.NoError(t, mc.UnmarshalPrevoteProof(b, &got), string(b))

			require.Equal(t, proof, got)
		})

		t.Run("determinism", func(t *testing.T) {
			t.Parallel()

			fx := tmconsensustest.NewEd25519Fixture(8)

			ph := fx.NextProposedHeader([]byte("app_data"), 0)

			vt := tmconsensus.VoteTarget{Height: 1, Round: 0, BlockHash: string(ph.Header.Hash)}
			nilVT := tmconsensus.VoteTarget{Height: 1, Round: 0}
			fullProof := map[string]gcrypto.CommonMessageSignatureProof{
				string(ph.Header.Hash): fx.PrevoteSignatureProof(ctx, vt, nil, []int{0, 1, 2, 3}),
				"":                     fx.PrevoteSignatureProof(ctx, nilVT, nil, []int{4, 5, 6, 7}),
			}

			proof, err := tmconsensus.PrevoteSparseProofFromFullProof(1, 0, fullProof)
			require.NoError(t, err)

			mc := mcf()
			orig, err := mc.MarshalPrevoteProof(proof)
			require.NoError(t, err)

			for i := 0; i < determinismTries; i++ {
				got, err := mc.MarshalPrevoteProof(proof)
				require.NoError(t, err)

				require.Equal(t, orig, got)
			}
		})
	})

	t.Run("precommit proofs", func(t *testing.T) {
		t.Run("round trip", func(t *testing.T) {
			t.Parallel()

			fx := tmconsensustest.NewEd25519Fixture(8)
			ph := fx.NextProposedHeader([]byte("app_data"), 0)

			vt := tmconsensus.VoteTarget{Height: 1, Round: 0, BlockHash: string(ph.Header.Hash)}
			nilVT := tmconsensus.VoteTarget{Height: 1, Round: 0}
			fullProof := map[string]gcrypto.CommonMessageSignatureProof{
				string(ph.Header.Hash): fx.PrecommitSignatureProof(ctx, vt, nil, []int{0, 1, 2, 3}),
				"":                     fx.PrecommitSignatureProof(ctx, nilVT, nil, []int{4, 5, 6, 7}),
			}

			proof, err := tmconsensus.PrecommitSparseProofFromFullProof(1, 0, fullProof)
			require.NoError(t, err)

			mc := mcf()
			b, err := mc.MarshalPrecommitProof(proof)
			require.NoError(t, err)

			var got tmconsensus.PrecommitSparseProof
			require.NoError(t, mc.UnmarshalPrecommitProof(b, &got))

			require.Equal(t, proof, got)
		})

		t.Run("determinism", func(t *testing.T) {
			t.Parallel()

			fx := tmconsensustest.NewEd25519Fixture(8)

			ph := fx.NextProposedHeader([]byte("app_data"), 0)

			vt := tmconsensus.VoteTarget{Height: 1, Round: 0, BlockHash: string(ph.Header.Hash)}
			nilVT := tmconsensus.VoteTarget{Height: 1, Round: 0}
			fullProof := map[string]gcrypto.CommonMessageSignatureProof{
				string(ph.Header.Hash): fx.PrecommitSignatureProof(ctx, vt, nil, []int{0, 1, 2, 3}),
				"":                     fx.PrecommitSignatureProof(ctx, nilVT, nil, []int{4, 5, 6, 7}),
			}

			proof, err := tmconsensus.PrecommitSparseProofFromFullProof(1, 0, fullProof)
			require.NoError(t, err)

			mc := mcf()
			orig, err := mc.MarshalPrecommitProof(proof)
			require.NoError(t, err)

			for i := 0; i < determinismTries; i++ {
				got, err := mc.MarshalPrecommitProof(proof)
				require.NoError(t, err)

				require.Equal(t, orig, got)
			}
		})
	})

	t.Run("consensus message wrapper", func(t *testing.T) {
		for _, tc := range []struct {
			name     string
			populate func(m *tmcodec.ConsensusMessage)
		}{
			{
				name: "with proposed header",
				populate: func(m *tmcodec.ConsensusMessage) {
					fx := tmconsensustest.NewEd25519Fixture(3)

					ph := fx.NextProposedHeader([]byte("app_data"), 0)
					fx.SignProposal(ctx, &ph, 0)

					m.ProposedHeader = &ph
				},
			},
			{
				name: "with prevote proof",
				populate: func(m *tmcodec.ConsensusMessage) {
					fx := tmconsensustest.NewEd25519Fixture(8)

					ph := fx.NextProposedHeader([]byte("app_data"), 0)

					vt := tmconsensus.VoteTarget{Height: 1, Round: 0, BlockHash: string(ph.Header.Hash)}
					nilVT := tmconsensus.VoteTarget{Height: 1, Round: 0}
					fullProof := map[string]gcrypto.CommonMessageSignatureProof{
						string(ph.Header.Hash): fx.PrevoteSignatureProof(ctx, vt, nil, []int{0, 1, 2, 3}),
						"":                     fx.PrevoteSignatureProof(ctx, nilVT, nil, []int{4, 5, 6, 7}),
					}

					proof, err := tmconsensus.PrevoteSparseProofFromFullProof(1, 0, fullProof)
					require.NoError(t, err)

					m.PrevoteProof = &proof
				},
			},
			{
				name: "with precommit proof",
				populate: func(m *tmcodec.ConsensusMessage) {
					fx := tmconsensustest.NewEd25519Fixture(8)

					ph := fx.NextProposedHeader([]byte("app_data"), 0)

					vt := tmconsensus.VoteTarget{Height: 1, Round: 0, BlockHash: string(ph.Header.Hash)}
					nilVT := tmconsensus.VoteTarget{Height: 1, Round: 0}
					fullProof := map[string]gcrypto.CommonMessageSignatureProof{
						string(ph.Header.Hash): fx.PrecommitSignatureProof(ctx, vt, nil, []int{0, 1, 2, 3}),
						"":                     fx.PrecommitSignatureProof(ctx, nilVT, nil, []int{4, 5, 6, 7}),
					}

					proof, err := tmconsensus.PrecommitSparseProofFromFullProof(1, 0, fullProof)
					require.NoError(t, err)

					m.PrecommitProof = &proof
				},
			},
		} {
			tc := tc
			t.Run(tc.name, func(t *testing.T) {
				t.Run("round trip", func(t *testing.T) {
					t.Parallel()

					var msg, got tmcodec.ConsensusMessage
					tc.populate(&msg)
					t.Logf("After populating: %v", msg)

					mc := mcf()
					b, err := mc.MarshalConsensusMessage(msg)
					require.NoError(t, err)
					t.Logf("Marshaled value: %s", b)

					require.NoError(t, mc.UnmarshalConsensusMessage(b, &got))
					t.Logf("Unmarshaled value: %v", got)
					require.Equal(t, msg, got)
				})

				t.Run("determinism", func(t *testing.T) {
					t.Parallel()

					var msg tmcodec.ConsensusMessage
					tc.populate(&msg)

					mc := mcf()
					orig, err := mc.MarshalConsensusMessage(msg)
					require.NoError(t, err)

					for i := 0; i < determinismTries; i++ {
						got, err := mc.MarshalConsensusMessage(msg)
						require.NoError(t, err)

						require.Equal(t, orig, got)
					}
				})
			})
		}
	})
}
