package tmjson

import (
	"bytes"
	"encoding/json"
	"slices"

	"github.com/gordian-engine/gordian/gcrypto"
	"github.com/gordian-engine/gordian/tm/tmcodec"
	"github.com/gordian-engine/gordian/tm/tmconsensus"
)

// MarshalCodec is a [tmcodec.MarshalCodec] that
// translates tmconsensus values to and from JSON.
type MarshalCodec struct {
	CryptoRegistry *gcrypto.Registry
}

func (c MarshalCodec) MarshalHeader(b tmconsensus.Header) ([]byte, error) {
	jh := toJSONHeader(b, c.CryptoRegistry)
	return json.Marshal(jh)
}

func (c MarshalCodec) UnmarshalHeader(b []byte, header *tmconsensus.Header) error {
	var jh jsonHeader
	err := json.Unmarshal(b, &jh)
	if err != nil {
		return err
	}

	*header, err = jh.ToHeader(c.CryptoRegistry)
	return err
}

func (c MarshalCodec) MarshalProposedHeader(ph tmconsensus.ProposedHeader) ([]byte, error) {
	jph := toJSONProposedHeader(ph, c.CryptoRegistry)
	return json.Marshal(jph)
}

func (c MarshalCodec) UnmarshalProposedHeader(b []byte, ph *tmconsensus.ProposedHeader) error {
	var jpb jsonProposedHeader
	err := json.Unmarshal(b, &jpb)
	if err != nil {
		return err
	}

	*ph, err = jpb.ToProposedHeader(
		c.CryptoRegistry,
	)
	return err
}

func (c MarshalCodec) MarshalCommittedHeader(ch tmconsensus.CommittedHeader) ([]byte, error) {
	jch := toJSONCommittedHeader(ch, c.CryptoRegistry)
	return json.Marshal(jch)
}

func (c MarshalCodec) UnmarshalCommittedHeader(b []byte, ch *tmconsensus.CommittedHeader) error {
	var jch jsonCommittedHeader
	err := json.Unmarshal(b, &jch)
	if err != nil {
		return err
	}

	*ch, err = jch.ToCommittedHeader(c.CryptoRegistry)
	return err
}

type jsonSparseProof struct {
	Height     uint64
	Round      uint32
	PubKeyHash []byte // Has to be a byte slice for JSON round trips.
	Proofs     []jsonProofEntry
}

type jsonProofEntry struct {
	BlockHash  []byte // Normally encoded as string entry in map.
	Signatures []gcrypto.SparseSignature
}

func (c MarshalCodec) MarshalPrevoteProof(p tmconsensus.PrevoteSparseProof) ([]byte, error) {
	jsp := jsonSparseProof{
		Height:     p.Height,
		Round:      p.Round,
		PubKeyHash: []byte(p.PubKeyHash),
		Proofs:     make([]jsonProofEntry, 0, len(p.Proofs)),
	}

	for blockHash, sigs := range p.Proofs {
		jsp.Proofs = append(jsp.Proofs, jsonProofEntry{
			BlockHash:  []byte(blockHash),
			Signatures: sigs,
		})
	}

	// Because we are translating a map to a slice,
	// and because the codec compliance tests expect determinism in codec output,
	// we sort the proofs slice by block hash.
	slices.SortFunc(jsp.Proofs, func(a, b jsonProofEntry) int {
		return bytes.Compare(a.BlockHash, b.BlockHash)
	})

	return json.Marshal(jsp)
}

func (c MarshalCodec) UnmarshalPrevoteProof(b []byte, p *tmconsensus.PrevoteSparseProof) error {
	var jsp jsonSparseProof

	if err := json.Unmarshal(b, &jsp); err != nil {
		return err
	}

	*p = tmconsensus.PrevoteSparseProof{
		Height:     jsp.Height,
		Round:      jsp.Round,
		PubKeyHash: string(jsp.PubKeyHash),
		Proofs:     make(map[string][]gcrypto.SparseSignature, len(jsp.Proofs)),
	}

	for _, e := range jsp.Proofs {
		p.Proofs[string(e.BlockHash)] = e.Signatures
	}

	return nil
}

func (c MarshalCodec) MarshalPrecommitProof(p tmconsensus.PrecommitSparseProof) ([]byte, error) {
	jsp := jsonSparseProof{
		Height:     p.Height,
		Round:      p.Round,
		PubKeyHash: []byte(p.PubKeyHash),
		Proofs:     make([]jsonProofEntry, 0, len(p.Proofs)),
	}

	for blockHash, sigs := range p.Proofs {
		jsp.Proofs = append(jsp.Proofs, jsonProofEntry{
			BlockHash:  []byte(blockHash),
			Signatures: sigs,
		})
	}

	// Because we are translating a map to a slice,
	// and because the codec compliance tests expect determinism in codec output,
	// we sort the proofs slice by block hash.
	slices.SortFunc(jsp.Proofs, func(a, b jsonProofEntry) int {
		return bytes.Compare(a.BlockHash, b.BlockHash)
	})

	return json.Marshal(jsp)
}

func (c MarshalCodec) UnmarshalPrecommitProof(b []byte, p *tmconsensus.PrecommitSparseProof) error {
	var jsp jsonSparseProof

	if err := json.Unmarshal(b, &jsp); err != nil {
		return err
	}

	*p = tmconsensus.PrecommitSparseProof{
		Height:     jsp.Height,
		Round:      jsp.Round,
		PubKeyHash: string(jsp.PubKeyHash),
		Proofs:     make(map[string][]gcrypto.SparseSignature, len(jsp.Proofs)),
	}

	for _, e := range jsp.Proofs {
		p.Proofs[string(e.BlockHash)] = e.Signatures
	}

	return nil
}

type jsonConsensusMessage struct {
	ProposedHeader, PrevoteProof, PrecommitProof json.RawMessage `json:",omitempty"`
}

func (c MarshalCodec) MarshalConsensusMessage(m tmcodec.ConsensusMessage) ([]byte, error) {
	var jcm jsonConsensusMessage
	switch {
	case m.ProposedHeader != nil:
		b, err := c.MarshalProposedHeader(*m.ProposedHeader)
		if err != nil {
			return nil, err
		}
		jcm.ProposedHeader = json.RawMessage(b)
	case m.PrevoteProof != nil:
		b, err := c.MarshalPrevoteProof(*m.PrevoteProof)
		if err != nil {
			return nil, err
		}
		jcm.PrevoteProof = json.RawMessage(b)
	case m.PrecommitProof != nil:
		b, err := c.MarshalPrecommitProof(*m.PrecommitProof)
		if err != nil {
			return nil, err
		}
		jcm.PrecommitProof = json.RawMessage(b)
	}

	return json.Marshal(jcm)
}

func (c MarshalCodec) UnmarshalConsensusMessage(b []byte, m *tmcodec.ConsensusMessage) error {
	var jcm jsonConsensusMessage
	if err := json.Unmarshal(b, &jcm); err != nil {
		return err
	}

	switch {
	case jcm.ProposedHeader != nil:
		var ph tmconsensus.ProposedHeader
		if err := c.UnmarshalProposedHeader(jcm.ProposedHeader, &ph); err != nil {
			return err
		}
		m.ProposedHeader = &ph
	case jcm.PrevoteProof != nil:
		var proof tmconsensus.PrevoteSparseProof
		if err := c.UnmarshalPrevoteProof(jcm.PrevoteProof, &proof); err != nil {
			return err
		}
		m.PrevoteProof = &proof
	case jcm.PrecommitProof != nil:
		var proof tmconsensus.PrecommitSparseProof
		if err := c.UnmarshalPrecommitProof(jcm.PrecommitProof, &proof); err != nil {
			return err
		}
		m.PrecommitProof = &proof
	}

	return nil
}
