package tmjson

import (
	"fmt"

	"github.com/gordian-engine/gordian/gcrypto"
	"github.com/gordian-engine/gordian/tm/tmconsensus"
)

// jsonProposedHeader is a converted [tmconsensus.ProposedHeader]
// that can be safely marshalled as JSON.
type jsonProposedHeader struct {
	Header jsonHeader

	Round uint32

	ProposerPubKey []byte

	Signature []byte

	UserAnnotation, DriverAnnotation []byte
}

func (jph jsonProposedHeader) ToProposedHeader(
	reg *gcrypto.Registry,
) (tmconsensus.ProposedHeader, error) {
	h, err := jph.Header.ToHeader(reg)
	if err != nil {
		return tmconsensus.ProposedHeader{}, fmt.Errorf(
			"failed to unmarshal proposed header: %w", err,
		)
	}

	var pubKey gcrypto.PubKey
	if jph.ProposerPubKey != nil {
		pubKey, err = reg.Unmarshal(jph.ProposerPubKey)
		if err != nil {
			return tmconsensus.ProposedHeader{}, fmt.Errorf(
				"failed to unmarshal proposer pubkey: %w", err,
			)
		}
	}

	return tmconsensus.ProposedHeader{
		Header:         h,
		Round:          jph.Round,
		ProposerPubKey: pubKey,
		Signature:      jph.Signature,
		Annotations: tmconsensus.Annotations{
			User:   jph.UserAnnotation,
			Driver: jph.DriverAnnotation,
		},
	}, nil
}

func toJSONProposedHeader(ph tmconsensus.ProposedHeader, reg *gcrypto.Registry) jsonProposedHeader {
	jph := jsonProposedHeader{
		Header:    toJSONHeader(ph.Header, reg),
		Round:     ph.Round,
		Signature: ph.Signature,

		UserAnnotation:   ph.Annotations.User,
		DriverAnnotation: ph.Annotations.Driver,
	}
	if ph.ProposerPubKey != nil {
		jph.ProposerPubKey = reg.Marshal(ph.ProposerPubKey)
	}
	return jph
}

// jsonCommittedHeader is a converted [tmconsensus.CommittedHeader]
// that can be safely marshalled as JSON.
type jsonCommittedHeader struct {
	Header jsonHeader

	Proof jsonCommitProof
}

func (jch jsonCommittedHeader) ToCommittedHeader(
	reg *gcrypto.Registry,
) (tmconsensus.CommittedHeader, error) {
	h, err := jch.Header.ToHeader(reg)
	if err != nil {
		return tmconsensus.CommittedHeader{}, fmt.Errorf(
			"failed to unmarshal committed header: %w", err,
		)
	}
	p, err := jch.Proof.ToCommitProof()
	if err != nil {
		return tmconsensus.CommittedHeader{}, fmt.Errorf(
			"failed to unmarshal committed header's proof: %w", err,
		)
	}

	return tmconsensus.CommittedHeader{
		Header: h,
		Proof:  p,
	}, nil
}

func toJSONCommittedHeader(
	ch tmconsensus.CommittedHeader, reg *gcrypto.Registry,
) jsonCommittedHeader {
	return jsonCommittedHeader{
		Header: toJSONHeader(ch.Header, reg),
		Proof:  toJSONCommitProof(ch.Proof),
	}
}

// jsonProposedHeader is a converted [tmconsensus.Header]
// that can be safely marshalled as JSON.
type jsonHeader struct {
	Hash          []byte
	PrevBlockHash []byte

	Height uint64

	PrevCommitProof jsonCommitProof

	ValidatorSet, NextValidatorSet struct {
		Validators    []jsonValidator
		PubKeyHash    []byte
		VotePowerHash []byte
	}

	DataID           []byte
	PrevAppStateHash []byte

	UserAnnotation, DriverAnnotation []byte
}

func (jh jsonHeader) ToHeader(
	reg *gcrypto.Registry,
) (tmconsensus.Header, error) {
	validators := make([]tmconsensus.Validator, len(jh.ValidatorSet.Validators))
	curPubKeys := make([]gcrypto.PubKey, len(jh.ValidatorSet.Validators))
	for i, jv := range jh.ValidatorSet.Validators {
		var err error
		validators[i], err = jv.ToValidator(reg)
		if err != nil {
			return tmconsensus.Header{}, fmt.Errorf(
				"failed to unmarshal to validator at index %d: %w",
				i, err,
			)
		}
		curPubKeys[i] = validators[i].PubKey
	}

	nextValidators := make([]tmconsensus.Validator, len(jh.NextValidatorSet.Validators))
	nextPubKeys := make([]gcrypto.PubKey, len(jh.NextValidatorSet.Validators))
	for i, jv := range jh.NextValidatorSet.Validators {
		var err error
		nextValidators[i], err = jv.ToValidator(reg)
		if err != nil {
			return tmconsensus.Header{}, fmt.Errorf(
				"failed to unmarshal to NextValidator at index %d: %w",
				i, err,
			)
		}
		nextPubKeys[i] = nextValidators[i].PubKey
	}

	var proof tmconsensus.CommitProof
	if jh.PrevCommitProof.PubKeyHash != nil {
		// Often this would check length > 0,
		// but in this case, a non-nil map at initial height is meaningful.
		var err error
		proof, err = jh.PrevCommitProof.ToCommitProof()
		if err != nil {
			return tmconsensus.Header{}, fmt.Errorf(
				"failed to build PrevCommitProof: %w", err,
			)
		}
	}

	return tmconsensus.Header{
		Hash:          jh.Hash,
		PrevBlockHash: jh.PrevBlockHash,

		Height: jh.Height,

		PrevCommitProof: proof,

		ValidatorSet: tmconsensus.ValidatorSet{
			Validators:    validators,
			PubKeys:       curPubKeys,
			PubKeyHash:    jh.ValidatorSet.PubKeyHash,
			VotePowerHash: jh.ValidatorSet.VotePowerHash,
		},
		NextValidatorSet: tmconsensus.ValidatorSet{
			Validators:    nextValidators,
			PubKeys:       nextPubKeys,
			PubKeyHash:    jh.NextValidatorSet.PubKeyHash,
			VotePowerHash: jh.NextValidatorSet.VotePowerHash,
		},

		DataID:           jh.DataID,
		PrevAppStateHash: jh.PrevAppStateHash,

		Annotations: tmconsensus.Annotations{
			User:   jh.UserAnnotation,
			Driver: jh.DriverAnnotation,
		},
	}, nil
}

func toJSONHeader(b tmconsensus.Header, reg *gcrypto.Registry) jsonHeader {
	jValidators := make([]jsonValidator, len(b.ValidatorSet.Validators))
	for i, v := range b.ValidatorSet.Validators {
		jValidators[i] = toJSONValidator(v, reg)
	}

	jNextValidators := make([]jsonValidator, len(b.NextValidatorSet.Validators))
	for i, v := range b.NextValidatorSet.Validators {
		jNextValidators[i] = toJSONValidator(v, reg)
	}

	jh := jsonHeader{
		Hash:          b.Hash,
		PrevBlockHash: b.PrevBlockHash,

		Height: b.Height,

		PrevCommitProof: toJSONCommitProof(b.PrevCommitProof),

		DataID:           b.DataID,
		PrevAppStateHash: b.PrevAppStateHash,

		UserAnnotation:   b.Annotations.User,
		DriverAnnotation: b.Annotations.Driver,
	}

	jh.ValidatorSet.Validators = jValidators
	jh.ValidatorSet.PubKeyHash = b.ValidatorSet.PubKeyHash
	jh.ValidatorSet.VotePowerHash = b.ValidatorSet.VotePowerHash

	jh.NextValidatorSet.Validators = jNextValidators
	jh.NextValidatorSet.PubKeyHash = b.NextValidatorSet.PubKeyHash
	jh.NextValidatorSet.VotePowerHash = b.NextValidatorSet.VotePowerHash

	return jh
}

// jsonProposedHeader is a converted [tmconsensus.Validator]
// that can be safely marshalled as JSON.
type jsonValidator struct {
	PubKey []byte
	Power  uint64
}

func (jv jsonValidator) ToValidator(reg *gcrypto.Registry) (tmconsensus.Validator, error) {
	pubKey, err := reg.Unmarshal(jv.PubKey)
	if err != nil {
		return tmconsensus.Validator{}, fmt.Errorf("failed to unmarshal public key: %w", err)
	}

	return tmconsensus.Validator{
		PubKey: pubKey,
		Power:  jv.Power,
	}, nil
}

func toJSONValidator(v tmconsensus.Validator, reg *gcrypto.Registry) jsonValidator {
	pubKeyBytes := reg.Marshal(v.PubKey)

	return jsonValidator{
		PubKey: pubKeyBytes,
		Power:  v.Power,
	}
}

type jsonCommitProof struct {
	Round uint32

	PubKeyHash []byte

	Commits []jsonProofEntry
}

func (jcp jsonCommitProof) ToCommitProof() (tmconsensus.CommitProof, error) {
	p := tmconsensus.CommitProof{
		Round: jcp.Round,

		PubKeyHash: string(jcp.PubKeyHash),

		Proofs: make(map[string][]gcrypto.SparseSignature, len(jcp.Commits)),
	}

	for _, e := range jcp.Commits {
		p.Proofs[string(e.BlockHash)] = e.Signatures
	}

	return p, nil
}

func toJSONCommitProof(p tmconsensus.CommitProof) jsonCommitProof {
	out := jsonCommitProof{
		Round:      p.Round,
		PubKeyHash: []byte(p.PubKeyHash),
		Commits:    make([]jsonProofEntry, 0, len(p.Proofs)),
	}

	for hash, p := range p.Proofs {
		out.Commits = append(out.Commits, jsonProofEntry{
			BlockHash:  []byte(hash),
			Signatures: p,
		})
	}

	return out
}
