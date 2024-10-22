package gcrypto_test

import (
	"testing"

	"github.com/gordian-engine/gordian/gcrypto"
	"github.com/stretchr/testify/require"
)

func TestSignatureProofMergeResult_Combine(t *testing.T) {
	for _, tc := range []struct {
		res1, res2, want gcrypto.SignatureProofMergeResult
	}{
		{
			// All true fields.
			res1: gcrypto.SignatureProofMergeResult{
				AllValidSignatures:  true,
				IncreasedSignatures: true,
				WasStrictSuperset:   true,
			},
			res2: gcrypto.SignatureProofMergeResult{
				AllValidSignatures:  true,
				IncreasedSignatures: true,
				WasStrictSuperset:   true,
			},

			want: gcrypto.SignatureProofMergeResult{
				AllValidSignatures:  true,
				IncreasedSignatures: true,
				WasStrictSuperset:   true,
			},
		},

		{
			// All false fields.
			res1: gcrypto.SignatureProofMergeResult{
				AllValidSignatures:  false,
				IncreasedSignatures: false,
				WasStrictSuperset:   false,
			},
			res2: gcrypto.SignatureProofMergeResult{
				AllValidSignatures:  false,
				IncreasedSignatures: false,
				WasStrictSuperset:   false,
			},

			want: gcrypto.SignatureProofMergeResult{
				AllValidSignatures:  false,
				IncreasedSignatures: false,
				WasStrictSuperset:   false,
			},
		},

		{
			// One all false, one all true.
			res1: gcrypto.SignatureProofMergeResult{
				AllValidSignatures:  false,
				IncreasedSignatures: false,
				WasStrictSuperset:   false,
			},
			res2: gcrypto.SignatureProofMergeResult{
				AllValidSignatures:  true,
				IncreasedSignatures: true,
				WasStrictSuperset:   true,
			},

			want: gcrypto.SignatureProofMergeResult{
				AllValidSignatures:  false,
				IncreasedSignatures: true,
				WasStrictSuperset:   false,
			},
		},
	} {
		out := tc.res1.Combine(tc.res2)
		require.Equalf(t, tc.want, out, "(%#v).Combine(%#v)", tc.res1, tc.res2)

		// Results should be commutative.
		out = tc.res2.Combine(tc.res1)
		require.Equalf(t, tc.want, out, "(%#v).Combine(%#v)", tc.res2, tc.res1)
	}
}
