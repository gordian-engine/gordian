package tmconsensustest

import (
	"bytes"
	"context"
	"fmt"

	"github.com/gordian-engine/gordian/gcrypto"
	"github.com/gordian-engine/gordian/tm/tmconsensus"
	"github.com/gordian-engine/gordian/tm/tmstore/tmmemstore"
)

// StandardFixture is a set of values used for typical test flows
// involving validators and voting,
// with some convenience methods for common test actions.
//
// It is named StandardFixture with the expectation that there will be
// some non-standard fixtures later.
type StandardFixture struct {
	PrivVals PrivValsEd25519

	// The signature scheme to use when constructing signatures.
	// May be safely reassigned before using the fixture.
	SignatureScheme tmconsensus.SignatureScheme

	// The hash scheme to use when calculating hashes.
	// May be safely reassigned before using the fixture.
	HashScheme tmconsensus.HashScheme

	// How to construct signature proofs.
	// May be safely reassigned before using the fixture.
	CommonMessageSignatureProofScheme gcrypto.CommonMessageSignatureProofScheme

	// Chain genesis value.
	// Set on a call to DefaultGenesis, or can be manually set
	// before a call to NextProposedHeader.
	Genesis tmconsensus.Genesis

	prevCommitProof  tmconsensus.CommitProof
	prevAppStateHash []byte
	prevBlockHash    []byte
	prevBlockHeight  uint64
}

// NewStandardFixture returns an initialized StandardFixture
// with the given number of determinstic ed25519 validators,
// a [SimpleSignatureScheme], and a [SimpleHashScheme].
//
// See the StandardFixture docs for other fields that
// may be set to default values but which may be overridden before use.
func NewStandardFixture(numVals int) *StandardFixture {
	return &StandardFixture{
		PrivVals: DeterministicValidatorsEd25519(numVals),

		SignatureScheme: SimpleSignatureScheme{},

		CommonMessageSignatureProofScheme: gcrypto.SimpleCommonMessageSignatureProofScheme{},

		HashScheme: SimpleHashScheme{},

		prevCommitProof: tmconsensus.CommitProof{
			// This map is expected to be empty, not nil, for the initial height.
			// TODO: why though? the stores return nil proofs when looking up the initial height,
			// and things appear to work fine that way.
			// And the nil-empty mismatch clutters up a lot of tests.
			Proofs: map[string][]gcrypto.SparseSignature{},
		},

		prevAppStateHash: []byte("uninitialized"),
	}
}

// Vals returns the sorted list of f's Validators.
func (f *StandardFixture) Vals() []tmconsensus.Validator {
	vals := f.PrivVals.Vals()
	tmconsensus.SortValidators(vals)
	return vals
}

func (f *StandardFixture) ValSet() tmconsensus.ValidatorSet {
	vs, err := tmconsensus.NewValidatorSet(f.Vals(), f.HashScheme)
	if err != nil {
		panic(fmt.Errorf("error building new validator set: %w", err))
	}
	return vs
}

func (f *StandardFixture) ValidatorHashes() (pubKeyHash, powHash string) {
	vals := f.Vals()
	pubKeys := tmconsensus.ValidatorsToPubKeys(vals)
	bPubKeyHash, err := f.HashScheme.PubKeys(pubKeys)
	if err != nil {
		panic(fmt.Errorf("error getting pub key hash: %w", err))
	}

	bPowHash, err := f.HashScheme.VotePowers(tmconsensus.ValidatorsToVotePowers(vals))
	if err != nil {
		panic(fmt.Errorf("error getting vote powers hash: %w", err))
	}

	return string(bPubKeyHash), string(bPowHash)
}

func (f *StandardFixture) NewMemActionStore() *tmmemstore.ActionStore {
	return tmmemstore.NewActionStore()
}

func (f *StandardFixture) NewMemValidatorStore() *tmmemstore.ValidatorStore {
	return tmmemstore.NewValidatorStore(f.HashScheme)
}

// DefaultGenesis returns a simple genesis suitable for basic tests.
func (f *StandardFixture) DefaultGenesis() tmconsensus.Genesis {
	g := tmconsensus.Genesis{
		ChainID: "my-chain",

		InitialHeight: 1,

		CurrentAppStateHash: []byte{0}, // This will probably be something different later.

		ValidatorSet: f.ValSet(),
	}

	f.Genesis = g
	if len(f.prevBlockHash) == 0 {
		h, err := g.Header(f.HashScheme)
		if err != nil {
			panic(fmt.Errorf("(*StandardFixture).DefaultGenesis: error calling (Genesis).Header: %w", err))
		}

		f.prevBlockHash = h.Hash
	}

	f.prevAppStateHash = g.CurrentAppStateHash

	return g
}

// NextProposedHeader returns a proposed header, with height set to the last committed height + 1,
// at Round zero, and with the previous block hash set to the last committed block's height.
//
// The valIdx parameter indicates which of f's validators to set as ProposerID.
// The returned proposal is unsigned; use f.SignProposal to set a valid signature.
//
// Both Validators and NextValidators are set to f.Vals().
// These and other fields may be overridden,
// in which case you should call f.RecalculateHash and f.SignProposal again.
func (f *StandardFixture) NextProposedHeader(appDataID []byte, valIdx int) tmconsensus.ProposedHeader {
	vs := f.ValSet()

	h := tmconsensus.Header{
		Height: f.prevBlockHeight + 1,

		PrevBlockHash: bytes.Clone(f.prevBlockHash),

		PrevCommitProof: f.prevCommitProof,

		ValidatorSet:     vs,
		NextValidatorSet: vs,

		DataID: appDataID,

		PrevAppStateHash: f.prevAppStateHash,
	}

	f.RecalculateHash(&h)

	return tmconsensus.ProposedHeader{Header: h}
}

// SignProposal sets the signature on the proposed header ph,
// using the validator at f.PrivVals[valIdx].
// On error, SignProposal panics.
func (f *StandardFixture) SignProposal(ctx context.Context, ph *tmconsensus.ProposedHeader, valIdx int) {
	v := f.PrivVals[valIdx]

	b, err := tmconsensus.ProposalSignBytes(ph.Header, ph.Round, ph.Annotations, f.SignatureScheme)
	if err != nil {
		panic(fmt.Errorf("failed to get sign bytes for proposal %#v: %w", ph, err))
	}

	ph.Signature, err = v.Signer.Sign(ctx, b)
	if err != nil {
		panic(fmt.Errorf("failed to sign proposal: %w", err))
	}

	ph.ProposerPubKey = v.CVal.PubKey
}

// PrevoteSignature returns the signature for the validator at valIdx
// against the given vote target,
// respecting vt.BlockHash in deciding whether the vote is active or nil.
func (f *StandardFixture) PrevoteSignature(
	ctx context.Context,
	vt tmconsensus.VoteTarget,
	valIdx int,
) []byte {
	return f.voteSignature(ctx, vt, valIdx, tmconsensus.PrevoteSignBytes)
}

// PrecommitSignature returns the signature for the validator at valIdx
// against the given vote target,
// respecting vt.BlockHash in deciding whether the vote is active or nil.
func (f *StandardFixture) PrecommitSignature(
	ctx context.Context,
	vt tmconsensus.VoteTarget,
	valIdx int,
) []byte {
	return f.voteSignature(ctx, vt, valIdx, tmconsensus.PrecommitSignBytes)
}

func (f *StandardFixture) voteSignature(
	ctx context.Context,
	vt tmconsensus.VoteTarget,
	valIdx int,
	signBytesFn func(tmconsensus.VoteTarget, tmconsensus.SignatureScheme) ([]byte, error),
) []byte {
	signContent, err := signBytesFn(vt, f.SignatureScheme)
	if err != nil {
		panic(fmt.Errorf("failed to generate signing content: %w", err))
	}

	sigBytes, err := f.PrivVals[valIdx].Signer.Sign(ctx, signContent)
	if err != nil {
		panic(fmt.Errorf("failed to sign content: %w", err))
	}

	return sigBytes
}

// PrevoteSignatureProof returns a CommonMessageSignatureProof for a prevote
// represented by the VoteTarget.
//
// If blockVals is nil, use f's Validators.
// If the block has a different set of validators from f, explicitly set blockVals.
//
// valIdxs is the set of indices of f's Validators, whose signatures should be part of the proof.
// These indices refer to f's Validators, and are not necessarily related to blockVals.
func (f *StandardFixture) PrevoteSignatureProof(
	ctx context.Context,
	vt tmconsensus.VoteTarget,
	blockVals []tmconsensus.Validator, // If nil, use f's validators.
	valIdxs []int, // The indices within f's validators.
) gcrypto.CommonMessageSignatureProof {
	signContent, err := tmconsensus.PrevoteSignBytes(vt, f.SignatureScheme)
	if err != nil {
		panic(fmt.Errorf("failed to generate signing content: %w", err))
	}

	if blockVals == nil {
		blockVals = f.Vals()
	}

	pubKeys := tmconsensus.ValidatorsToPubKeys(blockVals)
	bValPubKeyHash, err := f.HashScheme.PubKeys(pubKeys)
	if err != nil {
		panic(fmt.Errorf("failed to build validator public key hash: %w", err))
	}

	proof, err := f.CommonMessageSignatureProofScheme.New(signContent, pubKeys, string(bValPubKeyHash))
	if err != nil {
		panic(fmt.Errorf("failed to construct signature proof: %w", err))
	}

	for _, idx := range valIdxs {
		sigBytes, err := f.PrivVals[idx].Signer.Sign(ctx, signContent)
		if err != nil {
			panic(fmt.Errorf("failed to sign content with validator at index %d: %w", idx, err))
		}

		if err := proof.AddSignature(sigBytes, f.PrivVals[idx].Signer.PubKey()); err != nil {
			panic(fmt.Errorf("failed to add signature from validator at index %d: %w", idx, err))
		}
	}

	return proof
}

// PrecommitSignatureProof returns a CommonMessageSignatureProof for a precommit
// represented by the VoteTarget.
//
// If blockVals is nil, use f's Validators.
// If the block has a different set of validators from f, explicitly set blockVals.
//
// valIdxs is the set of indices of f's Validators, whose signatures should be part of the proof.
// These indices refer to f's Validators, and are not necessarily related to blockVals.
func (f *StandardFixture) PrecommitSignatureProof(
	ctx context.Context,
	vt tmconsensus.VoteTarget,
	blockVals []tmconsensus.Validator, // If nil, use f's validators.
	valIdxs []int, // The indices within f's validators.
) gcrypto.CommonMessageSignatureProof {
	signContent, err := tmconsensus.PrecommitSignBytes(vt, f.SignatureScheme)
	if err != nil {
		panic(fmt.Errorf("failed to generate signing content: %w", err))
	}

	if blockVals == nil {
		blockVals = f.Vals()
	}

	pubKeys := tmconsensus.ValidatorsToPubKeys(blockVals)
	bValPubKeyHash, err := f.HashScheme.PubKeys(pubKeys)
	if err != nil {
		panic(fmt.Errorf("failed to build validator public key hash: %w", err))
	}

	proof, err := f.CommonMessageSignatureProofScheme.New(signContent, pubKeys, string(bValPubKeyHash))
	if err != nil {
		panic(fmt.Errorf("failed to construct signature proof: %w", err))
	}

	for _, idx := range valIdxs {
		sigBytes, err := f.PrivVals[idx].Signer.Sign(ctx, signContent)
		if err != nil {
			panic(fmt.Errorf("failed to sign content with validator at index %d: %w", idx, err))
		}

		if err := proof.AddSignature(sigBytes, f.PrivVals[idx].Signer.PubKey()); err != nil {
			panic(fmt.Errorf("failed to add signature from validator at index %d: %w", idx, err))
		}
	}

	return proof
}

// PrevoteProofMap creates a map of prevote signatures that can be passed
// directly to [tmstore.ConsensusStore.OverwritePrevoteProof].
func (f *StandardFixture) PrevoteProofMap(
	ctx context.Context,
	height uint64,
	round uint32,
	voteMap map[string][]int, // Map of block hash to prevote, to validator indices.
) map[string]gcrypto.CommonMessageSignatureProof {
	vt := tmconsensus.VoteTarget{
		Height: height,
		Round:  round,
	}

	out := make(map[string]gcrypto.CommonMessageSignatureProof, len(voteMap))

	for hash, valIdxs := range voteMap {
		vt.BlockHash = hash
		out[hash] = f.PrevoteSignatureProof(
			ctx,
			vt,
			nil,
			valIdxs,
		)
	}

	return out
}

// SparsePrevoteProofMap returns a map of block hashes to sparse signature lists,
// which can be used to populate a PrevoteSparseProof.
func (f *StandardFixture) SparsePrevoteProofMap(
	ctx context.Context,
	height uint64,
	round uint32,
	voteMap map[string][]int, // Map of block hash to prevote, to validator indices.
) map[string][]gcrypto.SparseSignature {
	fullProof := f.PrevoteProofMap(ctx, height, round, voteMap)
	out := make(map[string][]gcrypto.SparseSignature, len(fullProof))

	for blockHash, p := range fullProof {
		out[blockHash] = p.AsSparse().Signatures
	}
	return out
}

func (f *StandardFixture) SparsePrevoteSignatureCollection(
	ctx context.Context,
	height uint64,
	round uint32,
	voteMap map[string][]int, // Map of block hash to prevote, to validator indices.
) tmconsensus.SparseSignatureCollection {
	fullProof := f.PrevoteProofMap(ctx, height, round, voteMap)
	out := tmconsensus.SparseSignatureCollection{
		BlockSignatures: make(map[string][]gcrypto.SparseSignature, len(voteMap)),
	}

	pubKeys := tmconsensus.ValidatorsToPubKeys(f.Vals())
	pubKeyHash, err := f.HashScheme.PubKeys(pubKeys)
	if err != nil {
		panic(fmt.Errorf("error getting pub key hash: %w", err))
	}
	out.PubKeyHash = pubKeyHash

	for blockHash, p := range fullProof {
		out.BlockSignatures[blockHash] = p.AsSparse().Signatures
	}
	return out
}

// PrecommitProofMap creates a map of precommit signatures that can be passed
// directly to [tmstore.ConsensusStore.OverwritePrecommitProof].
func (f *StandardFixture) PrecommitProofMap(
	ctx context.Context,
	height uint64,
	round uint32,
	voteMap map[string][]int, // Map of block hash to prevote, to validator indices.
) map[string]gcrypto.CommonMessageSignatureProof {
	vt := tmconsensus.VoteTarget{
		Height: height,
		Round:  round,
	}

	out := make(map[string]gcrypto.CommonMessageSignatureProof, len(voteMap))

	for hash, valIdxs := range voteMap {
		vt.BlockHash = hash
		out[hash] = f.PrecommitSignatureProof(
			ctx,
			vt,
			nil,
			valIdxs,
		)
	}

	return out
}

// SparsePrecommitProofMap returns a map of block hashes to sparse signature lists,
// which can be used to populate a CommitProof.
func (f *StandardFixture) SparsePrecommitProofMap(
	ctx context.Context,
	height uint64,
	round uint32,
	voteMap map[string][]int, // Map of block hash to prevote, to validator indices.
) map[string][]gcrypto.SparseSignature {
	fullProof := f.PrecommitProofMap(ctx, height, round, voteMap)
	out := make(map[string][]gcrypto.SparseSignature, len(fullProof))

	for blockHash, p := range fullProof {
		out[blockHash] = p.AsSparse().Signatures
	}
	return out
}

func (f *StandardFixture) SparsePrecommitSignatureCollection(
	ctx context.Context,
	height uint64,
	round uint32,
	voteMap map[string][]int, // Map of block hash to precommit, to validator indices.
) tmconsensus.SparseSignatureCollection {
	fullProof := f.PrecommitProofMap(ctx, height, round, voteMap)
	out := tmconsensus.SparseSignatureCollection{
		BlockSignatures: make(map[string][]gcrypto.SparseSignature, len(voteMap)),
	}

	pubKeys := tmconsensus.ValidatorsToPubKeys(f.Vals())
	pubKeyHash, err := f.HashScheme.PubKeys(pubKeys)
	if err != nil {
		panic(fmt.Errorf("error getting pub key hash: %w", err))
	}
	out.PubKeyHash = pubKeyHash

	for blockHash, p := range fullProof {
		out.BlockSignatures[blockHash] = p.AsSparse().Signatures
	}
	return out
}

// CommitBlock uses the input arguments to set up the next call to NextProposedHeader.
// The commit parameter is the set of precommits to associate with the block being committed,
// which will then be used as the previous commit details.
func (f *StandardFixture) CommitBlock(h tmconsensus.Header, appStateHash []byte, round uint32, commit map[string]gcrypto.CommonMessageSignatureProof) {
	if len(commit) == 0 {
		panic(fmt.Errorf("BUG: cannot commit block with empty commit data"))
	}

	f.prevBlockHeight = h.Height
	f.prevBlockHash = h.Hash
	f.prevAppStateHash = appStateHash

	p := tmconsensus.CommitProof{
		Round: round,

		Proofs: make(map[string][]gcrypto.SparseSignature, len(commit)),
	}

	for hash, sigProof := range commit {
		if p.PubKeyHash == "" {
			p.PubKeyHash = string(sigProof.PubKeyHash())
		}

		p.Proofs[hash] = sigProof.AsSparse().Signatures
	}

	f.prevCommitProof = p
}

func (f *StandardFixture) ValidatorPubKey(idx int) gcrypto.PubKey {
	return f.PrivVals[idx].CVal.PubKey
}

func (f *StandardFixture) ValidatorPubKeyString(idx int) string {
	return string(f.ValidatorPubKey(idx).PubKeyBytes())
}

// RecalculateHash modifies h.Hash using f.HashScheme.
// This is useful if a block is modified by hand for any reason.
// If calculating the hash results in an error, this method panics.
func (f *StandardFixture) RecalculateHash(h *tmconsensus.Header) {
	newHash, err := f.HashScheme.Block(*h)
	if err != nil {
		panic(fmt.Errorf("failed to calculate block hash: %w", err))
	}

	h.Hash = newHash
}

// UpdateVRVPrevotes returns a clone of vrv, with its version incremented and with all its prevote information
// updated to match the provided voteMap (which is a map of block hashes to voting validator indices).
func (f *StandardFixture) UpdateVRVPrevotes(
	ctx context.Context,
	vrv tmconsensus.VersionedRoundView,
	voteMap map[string][]int,
) tmconsensus.VersionedRoundView {
	vrv = vrv.Clone()
	vrv.Version++

	prevoteMap := f.PrevoteProofMap(ctx, vrv.Height, vrv.Round, voteMap)
	vrv.PrevoteProofs = prevoteMap
	if vrv.PrevoteBlockVersions == nil {
		vrv.PrevoteBlockVersions = make(map[string]uint32, len(voteMap))
	}
	for hash := range voteMap {
		vrv.PrevoteBlockVersions[hash]++
	}

	vs := &vrv.VoteSummary
	vs.SetPrevotePowers(f.Vals(), prevoteMap)

	return vrv
}

// UpdateVRVPrecommits returns a clone of vrv, with its version incremented and with all its precommit information
// updated to match the provided voteMap (which is a map of block hashes to voting validator indices).
func (f *StandardFixture) UpdateVRVPrecommits(
	ctx context.Context,
	vrv tmconsensus.VersionedRoundView,
	voteMap map[string][]int,
) tmconsensus.VersionedRoundView {
	vrv = vrv.Clone()
	vrv.Version++

	precommitMap := f.PrecommitProofMap(ctx, vrv.Height, vrv.Round, voteMap)
	vrv.PrecommitProofs = precommitMap
	if vrv.PrecommitBlockVersions == nil {
		vrv.PrecommitBlockVersions = make(map[string]uint32, len(voteMap))
	}
	for hash := range voteMap {
		vrv.PrecommitBlockVersions[hash]++
	}

	vs := &vrv.VoteSummary
	vs.SetPrecommitPowers(f.Vals(), precommitMap)

	return vrv
}
