package tmcodectest

import (
	"context"
	"testing"

	"github.com/rollchains/gordian/gcrypto"
	"github.com/rollchains/gordian/tm/tmcodec"
	"github.com/rollchains/gordian/tm/tmconsensus"
	"github.com/rollchains/gordian/tm/tmconsensus/tmconsensustest"
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

	t.Run("proposed blocks, plain blocks, and signed blocks", func(t *testing.T) {
		for _, useInitialHeight := range []bool{true, false} {
			useInitialHeight := useInitialHeight

			outerName := "past initial height"
			if useInitialHeight {
				outerName = "at initial height"
			}

			t.Run(outerName, func(t *testing.T) {
				t.Run("round trip", func(t *testing.T) {
					t.Parallel()

					fx := tmconsensustest.NewStandardFixture(4)
					pb := fx.NextProposedBlock([]byte("app_data"), 0)
					fx.SignProposal(ctx, &pb, 0)

					prevBlock := pb.Block

					if !useInitialHeight {
						vt := tmconsensus.VoteTarget{Height: 1, Round: 0, BlockHash: string(pb.Block.Hash)}
						fx.CommitBlock(pb.Block, []byte("app_hash"), 0, map[string]gcrypto.CommonMessageSignatureProof{
							string(pb.Block.Hash): fx.PrecommitSignatureProof(ctx, vt, nil, []int{0, 1, 2, 3}),
						})

						pb = fx.NextProposedBlock([]byte("app_data_2"), 0)
						fx.SignProposal(ctx, &pb, 0)
					}

					mc := mcf()

					t.Run("proposed block", func(t *testing.T) {
						for _, ac := range tmconsensustest.AnnotationCombinations() {
							ac := ac
							t.Run(ac.Name, func(t *testing.T) {
								if !useInitialHeight {
									activePrecommit := fx.PrecommitSignatureProof(
										ctx,
										tmconsensus.VoteTarget{Height: 1, Round: 0, BlockHash: string(prevBlock.Hash)},
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
											string(prevBlock.Hash): activePrecommit.AsSparse().Signatures,
											"":                     nilPrecommit.AsSparse().Signatures,
										},
									}
									pb.Block.PrevCommitProof = proof

									pb.Annotations = ac.Annotations
								}

								b, err := mc.MarshalProposedBlock(pb)
								require.NoError(t, err)

								var got tmconsensus.ProposedBlock
								require.NoError(t, mc.UnmarshalProposedBlock(b, &got))

								require.Equal(t, pb, got)
							})
						}
					})

					t.Run("plain block", func(t *testing.T) {
						b, err := mc.MarshalBlock(pb.Block)
						require.NoError(t, err)

						var got tmconsensus.Block
						require.NoError(t, mc.UnmarshalBlock(b, &got))

						require.Equal(t, pb.Block, got)
					})

					t.Run("plain block with annotations", func(t *testing.T) {
						// Just move the proposed block's annotations over the block's.
						pb.Block.Annotations, pb.Annotations = pb.Annotations, tmconsensus.Annotations{}

						b, err := mc.MarshalBlock(pb.Block)
						require.NoError(t, err)

						var got tmconsensus.Block
						require.NoError(t, mc.UnmarshalBlock(b, &got))

						require.Equal(t, pb.Block, got)
					})
				})

				t.Run("determinism", func(t *testing.T) {
					t.Parallel()

					fx := tmconsensustest.NewStandardFixture(8)

					pb := fx.NextProposedBlock([]byte("app_data"), 0)
					fx.SignProposal(ctx, &pb, 0)

					if !useInitialHeight {
						vt := tmconsensus.VoteTarget{Height: 1, Round: 0, BlockHash: string(pb.Block.Hash)}
						fx.CommitBlock(pb.Block, []byte("app_hash"), 0, map[string]gcrypto.CommonMessageSignatureProof{
							string(pb.Block.Hash): fx.PrecommitSignatureProof(ctx, vt, nil, []int{0, 1, 2, 3}),
						})

						pb = fx.NextProposedBlock([]byte("app_data_2"), 0)
						fx.SignProposal(ctx, &pb, 0)
					}

					mc := mcf()

					t.Run("proposed block", func(t *testing.T) {
						orig, err := mc.MarshalProposedBlock(pb)
						require.NoError(t, err)

						for i := 0; i < determinismTries; i++ {
							got, err := mc.MarshalProposedBlock(pb)
							require.NoError(t, err)

							require.Equal(t, orig, got)
						}
					})

					t.Run("plain block", func(t *testing.T) {
						orig, err := mc.MarshalBlock(pb.Block)
						require.NoError(t, err)

						for i := 0; i < determinismTries; i++ {
							got, err := mc.MarshalBlock(pb.Block)
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

			fx := tmconsensustest.NewStandardFixture(8)

			pb := fx.NextProposedBlock([]byte("app_data"), 0)

			vt := tmconsensus.VoteTarget{Height: 1, Round: 0}
			nilVT := tmconsensus.VoteTarget{Height: 1, Round: 0, BlockHash: string(pb.Block.Hash)}
			fullProof := map[string]gcrypto.CommonMessageSignatureProof{
				string(pb.Block.Hash): fx.PrevoteSignatureProof(ctx, vt, nil, []int{0, 1, 2, 3}),
				"":                    fx.PrevoteSignatureProof(ctx, nilVT, nil, []int{4, 5, 6, 7}),
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

			fx := tmconsensustest.NewStandardFixture(8)

			pb := fx.NextProposedBlock([]byte("app_data"), 0)

			vt := tmconsensus.VoteTarget{Height: 1, Round: 0}
			nilVT := tmconsensus.VoteTarget{Height: 1, Round: 0, BlockHash: string(pb.Block.Hash)}
			fullProof := map[string]gcrypto.CommonMessageSignatureProof{
				string(pb.Block.Hash): fx.PrevoteSignatureProof(ctx, vt, nil, []int{0, 1, 2, 3}),
				"":                    fx.PrevoteSignatureProof(ctx, nilVT, nil, []int{4, 5, 6, 7}),
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

			fx := tmconsensustest.NewStandardFixture(8)
			pb := fx.NextProposedBlock([]byte("app_data"), 0)

			vt := tmconsensus.VoteTarget{Height: 1, Round: 0}
			nilVT := tmconsensus.VoteTarget{Height: 1, Round: 0, BlockHash: string(pb.Block.Hash)}
			fullProof := map[string]gcrypto.CommonMessageSignatureProof{
				string(pb.Block.Hash): fx.PrecommitSignatureProof(ctx, vt, nil, []int{0, 1, 2, 3}),
				"":                    fx.PrecommitSignatureProof(ctx, nilVT, nil, []int{4, 5, 6, 7}),
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

			fx := tmconsensustest.NewStandardFixture(8)

			pb := fx.NextProposedBlock([]byte("app_data"), 0)

			vt := tmconsensus.VoteTarget{Height: 1, Round: 0}
			nilVT := tmconsensus.VoteTarget{Height: 1, Round: 0, BlockHash: string(pb.Block.Hash)}
			fullProof := map[string]gcrypto.CommonMessageSignatureProof{
				string(pb.Block.Hash): fx.PrecommitSignatureProof(ctx, vt, nil, []int{0, 1, 2, 3}),
				"":                    fx.PrecommitSignatureProof(ctx, nilVT, nil, []int{4, 5, 6, 7}),
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
				name: "with proposed block",
				populate: func(m *tmcodec.ConsensusMessage) {
					fx := tmconsensustest.NewStandardFixture(3)

					pb := fx.NextProposedBlock([]byte("app_data"), 0)
					fx.SignProposal(ctx, &pb, 0)

					m.ProposedBlock = &pb
				},
			},
			{
				name: "with prevote proof",
				populate: func(m *tmcodec.ConsensusMessage) {
					fx := tmconsensustest.NewStandardFixture(8)

					pb := fx.NextProposedBlock([]byte("app_data"), 0)

					vt := tmconsensus.VoteTarget{Height: 1, Round: 0}
					nilVT := tmconsensus.VoteTarget{Height: 1, Round: 0, BlockHash: string(pb.Block.Hash)}
					fullProof := map[string]gcrypto.CommonMessageSignatureProof{
						string(pb.Block.Hash): fx.PrevoteSignatureProof(ctx, vt, nil, []int{0, 1, 2, 3}),
						"":                    fx.PrevoteSignatureProof(ctx, nilVT, nil, []int{4, 5, 6, 7}),
					}

					proof, err := tmconsensus.PrevoteSparseProofFromFullProof(1, 0, fullProof)
					require.NoError(t, err)

					m.PrevoteProof = &proof
				},
			},
			{
				name: "with precommit proof",
				populate: func(m *tmcodec.ConsensusMessage) {
					fx := tmconsensustest.NewStandardFixture(8)

					pb := fx.NextProposedBlock([]byte("app_data"), 0)

					vt := tmconsensus.VoteTarget{Height: 1, Round: 0}
					nilVT := tmconsensus.VoteTarget{Height: 1, Round: 0, BlockHash: string(pb.Block.Hash)}
					fullProof := map[string]gcrypto.CommonMessageSignatureProof{
						string(pb.Block.Hash): fx.PrecommitSignatureProof(ctx, vt, nil, []int{0, 1, 2, 3}),
						"":                    fx.PrecommitSignatureProof(ctx, nilVT, nil, []int{4, 5, 6, 7}),
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
