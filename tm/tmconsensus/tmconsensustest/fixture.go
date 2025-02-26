package tmconsensustest

import (
	"bytes"
	"context"
	"fmt"

	"github.com/gordian-engine/gordian/gcrypto"
	"github.com/gordian-engine/gordian/tm/tmconsensus"
	"github.com/gordian-engine/gordian/tm/tmstore/tmmemstore"
)

// Fixture is a set of values used for typical test flows
// involving validators and voting,
// with some convenience methods for common test actions.
//
// Most Gordian core tests will use the [NewEd25519Fixture] method.
// This type is uncoupled from any particular concrete implementations
// in order to facilitate tests with external signer types.
type Fixture struct {
	PrivVals PrivVals

	SignatureScheme tmconsensus.SignatureScheme

	HashScheme tmconsensus.HashScheme

	CommonMessageSignatureProofScheme gcrypto.CommonMessageSignatureProofScheme

	Genesis tmconsensus.Genesis

	Registry gcrypto.Registry

	prevCommitProof  tmconsensus.CommitProof
	prevAppStateHash []byte
	prevBlockHash    []byte
	prevBlockHeight  uint64
}

// NewBareFixture returns a Fixture with the default testing schemes,
// and with appropriately initialized unexported fields.
//
// Callers must still set the Registry and PrivVals fields,
// and they may optionally override any of the Scheme fields.
//
// This function is preferred over direct instantiation of a Fixture due to the unexported fields.
func NewBareFixture() *Fixture {
	return &Fixture{
		SignatureScheme: SimpleSignatureScheme{},

		CommonMessageSignatureProofScheme: gcrypto.SimpleCommonMessageSignatureProofScheme{},

		HashScheme: SimpleHashScheme{},

		prevCommitProof: tmconsensus.CommitProof{
			// Store implementations like tmsqlite expect the Proofs map
			// to be empty but non-nil at the initial height.
			Proofs: map[string][]gcrypto.SparseSignature{},
		},

		// Various parts of the codebase assume this is non-empty.
		// The "uninitialized" string is obviously a bit meaningless.
		// Calling DefaultGenesis sets this field to the genesis app hash,
		// matching the behavior in production.
		prevAppStateHash: []byte("uninitialized"),
	}
}

func (f *Fixture) Vals() []tmconsensus.Validator {
	// NOTE: the StandardFixture used to sort the validators,
	// but based on how the deterministic validators were generated,
	// that should not have still been necessary.
	return f.PrivVals.Vals()
}

func (f *Fixture) ValSet() tmconsensus.ValidatorSet {
	vs, err := tmconsensus.NewValidatorSet(f.PrivVals.Vals(), f.HashScheme)
	if err != nil {
		panic(fmt.Errorf("error building new validator set: %w", err))
	}
	return vs
}

func (f *Fixture) ValidatorHashes() (pubKeyHash, powHash string) {
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

// Skipped: NewMemActionStore

func (f *Fixture) NewMemValidatorStore() *tmmemstore.ValidatorStore {
	return tmmemstore.NewValidatorStore(f.HashScheme)
}

// DefaultGenesis returns a simple genesis suitable for basic tests.
func (f *Fixture) DefaultGenesis() tmconsensus.Genesis {
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
			panic(fmt.Errorf("(*Fixture).DefaultGenesis: error calling (Genesis).Header: %w", err))
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
func (f *Fixture) NextProposedHeader(appDataID []byte, valIdx int) tmconsensus.ProposedHeader {
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
func (f *Fixture) SignProposal(ctx context.Context, ph *tmconsensus.ProposedHeader, valIdx int) {
	v := f.PrivVals[valIdx]

	b, err := tmconsensus.ProposalSignBytes(ph.Header, ph.Round, ph.Annotations, f.SignatureScheme)
	if err != nil {
		panic(fmt.Errorf("failed to get sign bytes for proposal %#v: %w", ph, err))
	}

	ph.Signature, err = v.Signer.Sign(ctx, b)
	if err != nil {
		panic(fmt.Errorf("failed to sign proposal: %w", err))
	}

	ph.ProposerPubKey = v.Val.PubKey
}

// PrevoteSignature returns the signature for the validator at valIdx
// against the given vote target,
// respecting vt.BlockHash in deciding whether the vote is active or nil.
func (f *Fixture) PrevoteSignature(
	ctx context.Context,
	vt tmconsensus.VoteTarget,
	valIdx int,
) []byte {
	return f.voteSignature(ctx, vt, valIdx, tmconsensus.PrevoteSignBytes)
}

// PrecommitSignature returns the signature for the validator at valIdx
// against the given vote target,
// respecting vt.BlockHash in deciding whether the vote is active or nil.
func (f *Fixture) PrecommitSignature(
	ctx context.Context,
	vt tmconsensus.VoteTarget,
	valIdx int,
) []byte {
	return f.voteSignature(ctx, vt, valIdx, tmconsensus.PrecommitSignBytes)
}

func (f *Fixture) voteSignature(
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
func (f *Fixture) PrevoteSignatureProof(
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
func (f *Fixture) PrecommitSignatureProof(
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
func (f *Fixture) PrevoteProofMap(
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
func (f *Fixture) SparsePrevoteProofMap(
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

func (f *Fixture) SparsePrevoteSignatureCollection(
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
func (f *Fixture) PrecommitProofMap(
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
func (f *Fixture) SparsePrecommitProofMap(
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

func (f *Fixture) SparsePrecommitSignatureCollection(
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
func (f *Fixture) CommitBlock(h tmconsensus.Header, appStateHash []byte, round uint32, commit map[string]gcrypto.CommonMessageSignatureProof) {
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

func (f *Fixture) ValidatorPubKey(idx int) gcrypto.PubKey {
	return f.PrivVals[idx].Val.PubKey
}

func (f *Fixture) ValidatorPubKeyString(idx int) string {
	return string(f.ValidatorPubKey(idx).PubKeyBytes())
}

// RecalculateHash modifies h.Hash using f.HashScheme.
// This is useful if a block is modified by hand for any reason.
// If calculating the hash results in an error, this method panics.
func (f *Fixture) RecalculateHash(h *tmconsensus.Header) {
	newHash, err := f.HashScheme.Block(*h)
	if err != nil {
		panic(fmt.Errorf("failed to calculate block hash: %w", err))
	}

	h.Hash = newHash
}

// UpdateVRVPrevotes returns a clone of vrv, with its version incremented and with all its prevote information
// updated to match the provided voteMap (which is a map of block hashes to voting validator indices).
func (f *Fixture) UpdateVRVPrevotes(
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
func (f *Fixture) UpdateVRVPrecommits(
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
