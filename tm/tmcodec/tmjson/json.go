package tmjson

import (
	"fmt"

	"github.com/rollchains/gordian/gcrypto"
	"github.com/rollchains/gordian/tm/tmconsensus"
)

// jsonProposedBlock is a converted [tmconsensus.ProposedBlock]
// that can be safely marshalled as JSON.
type jsonProposedBlock struct {
	Block jsonBlock

	Round uint32

	ProposerPubKey []byte

	Signature []byte

	UserAnnotation, DriverAnnotation []byte
}

func (jpb jsonProposedBlock) ToProposedBlock(
	reg *gcrypto.Registry,
) (tmconsensus.ProposedBlock, error) {
	b, err := jpb.Block.ToBlock(reg)
	if err != nil {
		return tmconsensus.ProposedBlock{}, fmt.Errorf(
			"failed to unmarshal proposed block: %w", err,
		)
	}

	pubKey, err := reg.Unmarshal(jpb.ProposerPubKey)
	if err != nil {
		return tmconsensus.ProposedBlock{}, fmt.Errorf(
			"failed to unmarshal proposer pubkey: %w", err,
		)
	}

	return tmconsensus.ProposedBlock{
		Block:          b,
		Round:          jpb.Round,
		ProposerPubKey: pubKey,
		Signature:      jpb.Signature,
		Annotations: tmconsensus.Annotations{
			User:   jpb.UserAnnotation,
			Driver: jpb.DriverAnnotation,
		},
	}, nil
}

func toJSONProposedBlock(pb tmconsensus.ProposedBlock, reg *gcrypto.Registry) jsonProposedBlock {
	return jsonProposedBlock{
		Block:          toJSONBlock(pb.Block, reg),
		Round:          pb.Round,
		ProposerPubKey: reg.Marshal(pb.ProposerPubKey),
		Signature:      pb.Signature,

		UserAnnotation:   pb.Annotations.User,
		DriverAnnotation: pb.Annotations.Driver,
	}
}

// jsonProposedBlock is a converted [tmconsensus.Block]
// that can be safely marshalled as JSON.
type jsonBlock struct {
	Hash          []byte
	PrevBlockHash []byte

	Height uint64

	PrevCommitProof jsonCommitProof

	Validators     []jsonValidator
	NextValidators []jsonValidator

	DataID           []byte
	PrevAppStateHash []byte

	UserAnnotation, DriverAnnotation []byte
}

func (jb jsonBlock) ToBlock(
	reg *gcrypto.Registry,
) (tmconsensus.Block, error) {
	validators := make([]tmconsensus.Validator, len(jb.Validators))
	for i, jv := range jb.Validators {
		var err error
		validators[i], err = jv.ToValidator(reg)
		if err != nil {
			return tmconsensus.Block{}, fmt.Errorf(
				"failed to unmarshal to validator at index %d: %w",
				i, err,
			)
		}
	}

	nextValidators := make([]tmconsensus.Validator, len(jb.NextValidators))
	for i, jv := range jb.NextValidators {
		var err error
		nextValidators[i], err = jv.ToValidator(reg)
		if err != nil {
			return tmconsensus.Block{}, fmt.Errorf(
				"failed to unmarshal to NextValidator at index %d: %w",
				i, err,
			)
		}
	}

	allPubKeys := make([]gcrypto.PubKey, len(validators))
	for i, v := range validators {
		allPubKeys[i] = v.PubKey
	}

	var proof tmconsensus.CommitProof
	if len(jb.PrevCommitProof.PubKeyHash) > 0 {
		var err error
		proof, err = jb.PrevCommitProof.ToCommitProof()
		if err != nil {
			return tmconsensus.Block{}, fmt.Errorf(
				"failed to build PrevCommitProof: %w", err,
			)
		}
	}

	return tmconsensus.Block{
		Hash:          jb.Hash,
		PrevBlockHash: jb.PrevBlockHash,

		Height: jb.Height,

		PrevCommitProof: proof,

		Validators:     validators,
		NextValidators: nextValidators,

		DataID:           jb.DataID,
		PrevAppStateHash: jb.PrevAppStateHash,

		Annotations: tmconsensus.Annotations{
			User:   jb.UserAnnotation,
			Driver: jb.DriverAnnotation,
		},
	}, nil
}

func toJSONBlock(b tmconsensus.Block, reg *gcrypto.Registry) jsonBlock {
	jValidators := make([]jsonValidator, len(b.Validators))
	for i, v := range b.Validators {
		jValidators[i] = toJSONValidator(v, reg)
	}

	jNextValidators := make([]jsonValidator, len(b.NextValidators))
	for i, v := range b.NextValidators {
		jNextValidators[i] = toJSONValidator(v, reg)
	}

	return jsonBlock{
		Hash:          b.Hash,
		PrevBlockHash: b.PrevBlockHash,

		Height: b.Height,

		PrevCommitProof: toJSONCommitProof(b.PrevCommitProof),

		Validators:     jValidators,
		NextValidators: jNextValidators,

		DataID:           b.DataID,
		PrevAppStateHash: b.PrevAppStateHash,

		UserAnnotation:   b.Annotations.User,
		DriverAnnotation: b.Annotations.Driver,
	}
}

// jsonProposedBlock is a converted [tmconsensus.Validator]
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
