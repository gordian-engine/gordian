package tmconsensustest

import (
	"github.com/gordian-engine/gordian/gcrypto"
	"github.com/gordian-engine/gordian/tm/tmconsensus"
)

// PrivVal is the "private" view of the validators for use in the [Fixture] type,
// so that tests have access to the Signers backing the validators too.
type PrivVal struct {
	// The plain consensus validator.
	Val tmconsensus.Validator

	Signer gcrypto.Signer
}

type PrivVals []PrivVal

func (vs PrivVals) Vals() []tmconsensus.Validator {
	out := make([]tmconsensus.Validator, len(vs))
	for i, v := range vs {
		out[i] = v.Val
	}
	return out
}

func (vs PrivVals) PubKeys() []gcrypto.PubKey {
	out := make([]gcrypto.PubKey, len(vs))
	for i, v := range vs {
		out[i] = v.Signer.PubKey()
	}
	return out
}
