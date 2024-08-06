package tmmirror

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"runtime/trace"

	"github.com/rollchains/gordian/gcrypto"
	"github.com/rollchains/gordian/gwatchdog"
	"github.com/rollchains/gordian/internal/gchan"
	"github.com/rollchains/gordian/internal/glog"
	"github.com/rollchains/gordian/tm/tmconsensus"
	"github.com/rollchains/gordian/tm/tmengine/internal/tmeil"
	"github.com/rollchains/gordian/tm/tmengine/internal/tmemetrics"
	"github.com/rollchains/gordian/tm/tmengine/internal/tmmirror/internal/tmi"
	"github.com/rollchains/gordian/tm/tmengine/tmelink"
	"github.com/rollchains/gordian/tm/tmstore"
)

// Mirror maintains a read-only view of the chain state,
// based on inputs from the network.
type Mirror struct {
	log *slog.Logger

	k *tmi.Kernel

	hashScheme tmconsensus.HashScheme
	sigScheme  tmconsensus.SignatureScheme
	cmspScheme gcrypto.CommonMessageSignatureProofScheme

	snapshotRequests   chan<- tmi.SnapshotRequest
	viewLookupRequests chan<- tmi.ViewLookupRequest

	pbCheckRequests chan<- tmi.PBCheckRequest

	addPBRequests        chan<- tmconsensus.ProposedBlock
	addPrevoteRequests   chan<- tmi.AddPrevoteRequest
	addPrecommitRequests chan<- tmi.AddPrecommitRequest
}

// MirrorConfig holds the configuration required to start a [Mirror].
type MirrorConfig struct {
	Store          tmstore.MirrorStore
	BlockStore     tmstore.BlockStore
	RoundStore     tmstore.RoundStore
	ValidatorStore tmstore.ValidatorStore

	InitialHeight     uint64
	InitialValidators []tmconsensus.Validator

	HashScheme                        tmconsensus.HashScheme
	SignatureScheme                   tmconsensus.SignatureScheme
	CommonMessageSignatureProofScheme gcrypto.CommonMessageSignatureProofScheme

	ProposedBlockFetcher tmelink.ProposedBlockFetcher

	GossipStrategyOut chan<- tmelink.NetworkViewUpdate

	StateMachineRoundViewOut chan<- tmeil.StateMachineRoundView

	StateMachineRoundEntranceIn <-chan tmeil.StateMachineRoundEntrance

	MetricsCollector *tmemetrics.Collector

	Watchdog *gwatchdog.Watchdog
}

// toKernelConfig copies the fields from c that are duplicated in the kernel config.
func (c MirrorConfig) toKernelConfig() tmi.KernelConfig {
	return tmi.KernelConfig{
		Store:          c.Store,
		BlockStore:     c.BlockStore,
		RoundStore:     c.RoundStore,
		ValidatorStore: c.ValidatorStore,

		HashScheme:                        c.HashScheme,
		SignatureScheme:                   c.SignatureScheme,
		CommonMessageSignatureProofScheme: c.CommonMessageSignatureProofScheme,

		InitialHeight:     c.InitialHeight,
		InitialValidators: c.InitialValidators,

		ProposedBlockFetcher: c.ProposedBlockFetcher,

		GossipStrategyOut: c.GossipStrategyOut,

		StateMachineRoundEntranceIn: c.StateMachineRoundEntranceIn,
		StateMachineRoundViewOut:    c.StateMachineRoundViewOut,

		MetricsCollector: c.MetricsCollector,

		Watchdog: c.Watchdog,
	}
}

// NewMirror returns a new Mirror based on the given MirrorConfig.
//
// The Mirror runs background goroutines associated with ctx.
// The Mirror can be stopped by canceling the context
// and calling its Wait method.
func NewMirror(
	ctx context.Context,
	log *slog.Logger,
	cfg MirrorConfig,
) (*Mirror, error) {
	kCfg := cfg.toKernelConfig()

	// 1-buffered because it is possible that the caller
	// may initiate the request and do work before reading the response.
	snapshotRequests := make(chan tmi.SnapshotRequest, 1)
	viewLookupRequests := make(chan tmi.ViewLookupRequest, 1)
	kCfg.SnapshotRequests = snapshotRequests
	kCfg.ViewLookupRequests = viewLookupRequests

	// No work to do after initiating these requests.
	pbCheckRequests := make(chan tmi.PBCheckRequest)
	kCfg.PBCheckRequests = pbCheckRequests

	// Arbitrarily sized to allow some concurrent requests,
	// with low likelihood of blocking.
	addPBRequests := make(chan tmconsensus.ProposedBlock, 8)
	kCfg.AddPBRequests = addPBRequests

	// The calling method blocks on the response regardless,
	// so no point in buffering these.
	addPrevoteRequests := make(chan tmi.AddPrevoteRequest)
	addPrecommitRequests := make(chan tmi.AddPrecommitRequest)
	kCfg.AddPrevoteRequests = addPrevoteRequests
	kCfg.AddPrecommitRequests = addPrecommitRequests

	k, err := tmi.NewKernel(ctx, log.With("m_sys", "kernel"), kCfg)
	if err != nil {
		// Assuming the error format doesn't need additional detail.
		return nil, err
	}

	m := &Mirror{
		log: log,

		k: k,

		hashScheme: cfg.HashScheme,
		sigScheme:  cfg.SignatureScheme,
		cmspScheme: cfg.CommonMessageSignatureProofScheme,

		snapshotRequests:   snapshotRequests,
		viewLookupRequests: viewLookupRequests,
		pbCheckRequests:    pbCheckRequests,

		addPBRequests:        addPBRequests,
		addPrevoteRequests:   addPrevoteRequests,
		addPrecommitRequests: addPrecommitRequests,
	}

	return m, nil
}

func (m *Mirror) Wait() {
	m.k.Wait()
}

// NetworkHeightRound is an alias into the internal package.
// TBD if this is worth keeping; if so it may be better to duplicate the type,
// as navigating to an internal package to find a definition
// is usually a poor, clunky experience.
type NetworkHeightRound = tmi.NetworkHeightRound

func (m *Mirror) HandleProposedBlock(ctx context.Context, pb tmconsensus.ProposedBlock) tmconsensus.HandleProposedBlockResult {
	defer trace.StartRegion(ctx, "HandleProposedBlock").End()

RESTART:
	req := tmi.PBCheckRequest{
		PB:   pb,
		Resp: make(chan tmi.PBCheckResponse, 1),
	}
	resp, ok := gchan.ReqResp(
		ctx, m.log,
		m.pbCheckRequests, req,
		req.Resp,
		"HandleProposedBlock:PBCheck",
	)
	if !ok {
		return tmconsensus.HandleProposedBlockInternalError
	}

	if resp.Status == tmi.PBCheckAlreadyHaveSignature {
		// Easy early return case.
		// We will say it's already stored.
		// Note, this is only a lightweight signature comparison,
		// so a maliciously crafted proposed block matching an existing signature
		// may be propagated through the network.
		// TODO: do a deep comparison to see if the proposed block matches,
		// and possibly return a new status if the signature is forged.
		return tmconsensus.HandleProposedBlockAlreadyStored
	}

	switch resp.Status {
	case tmi.PBCheckAcceptable:
		// Okay.
	case tmi.PBCheckSignerUnrecognized:
		// Cannot continue.
		return tmconsensus.HandleProposedBlockSignerUnrecognized
	case tmi.PBCheckNextHeight:
		// Special case: we make an additional request to the kernel if the PB is for the next height.
		m.backfillCommitForNextHeightPB(ctx, req.PB, *resp.VotingRoundView)
		goto RESTART // TODO: find a cleaner way to apply the proposed block after backfilling commit.
	case tmi.PBCheckRoundTooOld:
		return tmconsensus.HandleProposedBlockRoundTooOld
	case tmi.PBCheckRoundTooFarInFuture:
		return tmconsensus.HandleProposedBlockRoundTooFarInFuture
	default:
		panic(fmt.Errorf("TODO: handle PBCheck status %s", resp.Status))
	}

	// Arbitrarily choosing to validate the block hash before the signature.
	wantHash, err := m.hashScheme.Block(pb.Block)
	if err != nil {
		return tmconsensus.HandleProposedBlockInternalError
	}

	if !bytes.Equal(wantHash, pb.Block.Hash) {
		// Actual hash didn't match expected hash:
		// this message should not be on the network.
		return tmconsensus.HandleProposedBlockBadBlockHash
	}

	// Validate the signature based on the public key the kernel reported.
	signContent, err := tmconsensus.ProposalSignBytes(pb.Block, pb.Round, pb.Annotations, m.sigScheme)
	if err != nil {
		return tmconsensus.HandleProposedBlockInternalError
	}
	if !resp.ProposerPubKey.Verify(signContent, pb.Signature) {
		return tmconsensus.HandleProposedBlockBadSignature
	}

	// The hash matches and the proposed block was signed by a validator we know,
	// so we can accept the message.

	// Fire-and-forget a request to the kernel, to add this proposed block.
	// The m.addPBRequests channel has a larger buffer
	// for a relative guarantee that this send won't block.
	// But if it does, that's okay, it's effective backpressure at that point.
	_ = gchan.SendC(
		ctx, m.log,
		m.addPBRequests, pb,
		"requesting proposed block to be added",
	)

	// Is accepting here sufficient?
	// We could adjust the addPBRequests channel to respond with a value if needed.
	return tmconsensus.HandleProposedBlockAccepted
}

func (m *Mirror) backfillCommitForNextHeightPB(
	ctx context.Context,
	pb tmconsensus.ProposedBlock,
	rv tmconsensus.RoundView,
) backfillCommitStatus {
	defer trace.StartRegion(ctx, "backfillCommitForNextHeightPB").End()

	res := m.handlePrecommitProofs(ctx, tmconsensus.PrecommitSparseProof{
		Height: pb.Block.Height - 1,
		Round:  pb.Block.PrevCommitProof.Round,

		PubKeyHash: pb.Block.PrevCommitProof.PubKeyHash,

		Proofs: pb.Block.PrevCommitProof.Proofs,
	}, "(*Mirror).backfillCommitForNextHeightPB")

	if res != tmconsensus.HandleVoteProofsAccepted {
		return backfillCommitRejected
	}

	return backfillCommitAccepted
}

func (m *Mirror) HandlePrevoteProofs(ctx context.Context, p tmconsensus.PrevoteSparseProof) tmconsensus.HandleVoteProofsResult {
	defer trace.StartRegion(ctx, "HandlePrevoteProofs").End()

	// NOTE: keep changes to this method synchronized with HandlePrecommitProofs.

	if len(p.Proofs) == 0 {
		// Why was this even sent?
		return tmconsensus.HandleVoteProofsEmpty
	}

	try := 1

	var curPrevoteState tmconsensus.VersionedRoundView
	vlReq := tmi.ViewLookupRequest{
		H: p.Height,
		R: p.Round,

		VRV: &curPrevoteState,

		Fields: tmi.RVValidators | tmi.RVPrevotes,

		Reason: "(*Mirror).HandlePrevoteProofs",

		Resp: make(chan tmi.ViewLookupResponse, 1),
	}

RETRY:
	vlResp, ok := gchan.ReqResp(
		ctx, m.log,
		m.viewLookupRequests, vlReq,
		vlReq.Resp,
		"HandlePrevoteProofs",
	)
	if !ok {
		return tmconsensus.HandleVoteProofsInternalError
	}

	if vlResp.Status != tmi.ViewFound {
		// TODO: consider future view.
		// TODO: this return value is not quite right.
		return tmconsensus.HandleVoteProofsRoundTooOld
	}
	switch vlResp.ID {
	case tmi.ViewIDVoting, tmi.ViewIDCommitting, tmi.ViewIDNextRound:
		// Okay.
	default:
		panic(fmt.Errorf(
			"TODO: handle prevotes for views other than committing, voting, or next round (got %s)",
			vlResp.ID,
		))
	}

	if p.PubKeyHash != curPrevoteState.ValidatorPubKeyHash {
		// We assume our view of the network is correct,
		// and so we refuse to continue propagating this message
		// containing a validator hash mismatch.
		return tmconsensus.HandleVoteProofsBadPubKeyHash
	}

	curProofs := curPrevoteState.PrevoteProofs
	sigsToAdd := m.getSignaturesToAdd(curProofs, p.Proofs)

	if len(sigsToAdd) == 0 {
		// Maybe the message had some valid signatures.
		// Or this could happen if we received an identical or overlapping proof concurrently.
		return tmconsensus.HandleVoteProofsNoNewSignatures
	}

	// There is at least one signature we need to add.
	// Attempt to add it here, so we avoid doing unnecessary work in the kernel.
	voteUpdates := make(map[string]tmi.VoteUpdate, len(sigsToAdd))
	allValidSignatures := true
	for blockHash, sigs := range sigsToAdd {
		fullProof, ok := curProofs[blockHash]
		if !ok {
			if blockHash == "" {
				// The system requires the nil proof to always be part of current proofs.
				// If it is missing, then we have a bug somewhere.
				panic(fmt.Errorf(
					"BUG: did not have nil prevote proof when handling prevotes at height=%d/round=%d",
					p.Height, p.Round,
				))
			}
			emptyProof, ok := m.makeNewPrevoteProof(
				p.Height, p.Round, blockHash, curPrevoteState.Validators, curPrevoteState.ValidatorPubKeyHash,
			)
			if !ok {
				// Already logged.
				continue
			}
			fullProof = emptyProof
		}

		sparseProof := gcrypto.SparseSignatureProof{
			PubKeyHash: string(fullProof.PubKeyHash()),
			Signatures: sigs,
		}
		res := fullProof.MergeSparse(sparseProof)
		allValidSignatures = allValidSignatures && res.AllValidSignatures
		voteUpdates[blockHash] = tmi.VoteUpdate{
			Proof:       fullProof,
			PrevVersion: curPrevoteState.PrevoteBlockVersions[blockHash],
		}
	}

	if len(voteUpdates) == 0 {
		// We must have been unable to build the sign bytes or signature proof.
		// Ignore the message for now.
		return tmconsensus.HandleVoteProofsNoNewSignatures
	}

	// Now we have our updated proofs, so we can make a kernel request.
	resp := make(chan tmi.AddVoteResult, 1)
	addReq := tmi.AddPrevoteRequest{
		H: p.Height,
		R: p.Round,

		PrevoteUpdates: voteUpdates,

		Response: resp,
	}

	result, ok := gchan.ReqResp(
		ctx, m.log,
		m.addPrevoteRequests, addReq,
		resp,
		"AddPrevote",
	)
	if !ok {
		return tmconsensus.HandleVoteProofsInternalError
	}

	switch result {
	case tmi.AddVoteAccepted:
		// We are done.
		return tmconsensus.HandleVoteProofsAccepted
	case tmi.AddVoteConflict:
		// Try all over again!
		if try > 3 {
			m.log.Info("Conflict when applying prevote, retrying", "tries", try)
		}
		try++

		// Clear out the snapshot so it can be repopulated
		// with reduced allocations.
		curPrevoteState.Reset()

		// For how long this function is, and the fact that we are jumping back near the top,
		// a goto call seems perfectly reasonable here.
		goto RETRY
	case tmi.AddVoteOutOfDate:
		// The round changed while we were processing the request.
		// Just give up now.
		return tmconsensus.HandleVoteProofsRoundTooOld
	default:
		panic(fmt.Errorf(
			"BUG: received unknown AddVoteResult %d", result,
		))
	}
}

func (m *Mirror) HandlePrecommitProofs(ctx context.Context, p tmconsensus.PrecommitSparseProof) tmconsensus.HandleVoteProofsResult {
	defer trace.StartRegion(ctx, "HandlePrecommitProofs").End()

	return m.handlePrecommitProofs(ctx, p, "(*Mirror).HandlePrecommitProofs")
}

func (m *Mirror) handlePrecommitProofs(ctx context.Context, p tmconsensus.PrecommitSparseProof, reason string) tmconsensus.HandleVoteProofsResult {
	defer trace.StartRegion(ctx, "handlePrecommitProofs").End()
	// NOTE: keep changes to this method synchronized with HandlePrevoteProofs.

	if len(p.Proofs) == 0 {
		// Why was this even sent?
		return tmconsensus.HandleVoteProofsEmpty
	}

	try := 1

	var curPrecommitState tmconsensus.VersionedRoundView
	vlReq := tmi.ViewLookupRequest{
		H: p.Height,
		R: p.Round,

		VRV: &curPrecommitState,

		Fields: tmi.RVValidators | tmi.RVPrecommits,

		Reason: reason,

		Resp: make(chan tmi.ViewLookupResponse, 1),
	}
RETRY:
	vlResp, ok := gchan.ReqResp(
		ctx, m.log,
		m.viewLookupRequests, vlReq,
		vlReq.Resp,
		"HandlePrecommitProofs",
	)
	if !ok {
		return tmconsensus.HandleVoteProofsInternalError
	}

	if vlResp.Status != tmi.ViewFound {
		// TODO: consider future view.
		// TODO: this return value is not quite right.
		return tmconsensus.HandleVoteProofsRoundTooOld
	}
	switch vlResp.ID {
	case tmi.ViewIDVoting, tmi.ViewIDCommitting, tmi.ViewIDNextRound:
		// Okay.
	default:
		panic(fmt.Errorf(
			"TODO: handle prevotes for views other than committing, voting, or next round (got %s)",
			vlResp.ID,
		))
	}

	if p.PubKeyHash != curPrecommitState.ValidatorPubKeyHash {
		// We assume our view of the network is correct,
		// and so we refuse to continue propagating this message
		// containing a validator hash mismatch.
		return tmconsensus.HandleVoteProofsBadPubKeyHash
	}

	curProofs := curPrecommitState.PrecommitProofs
	sigsToAdd := m.getSignaturesToAdd(curProofs, p.Proofs)

	if len(sigsToAdd) == 0 {
		// Maybe the message had some valid signatures.
		// Or this could happen if we received an identical or overlapping proof concurrently.
		return tmconsensus.HandleVoteProofsNoNewSignatures
	}

	// There is at least one signature we need to add.
	// Attempt to add it here, so we avoid doing unnecessary work in the kernel.
	voteUpdates := make(map[string]tmi.VoteUpdate, len(sigsToAdd))
	allValidSignatures := true
	for blockHash, sigs := range sigsToAdd {
		fullProof, ok := curProofs[blockHash]
		if !ok {
			if blockHash == "" {
				// The system requires the nil proof to always be part of current proofs.
				// If it is missing, then we have a bug somewhere.
				panic(fmt.Errorf(
					"BUG: did not have nil precommit proof when handling precommits at height=%d/round=%d",
					p.Height, p.Round,
				))
			}
			emptyProof, ok := m.makeNewPrecommitProof(
				p.Height, p.Round, blockHash, curPrecommitState.Validators, curPrecommitState.ValidatorPubKeyHash,
			)
			if !ok {
				// Already logged.
				continue
			}
			fullProof = emptyProof
		}

		sparseProof := gcrypto.SparseSignatureProof{
			PubKeyHash: string(fullProof.PubKeyHash()),
			Signatures: sigs,
		}
		res := fullProof.MergeSparse(sparseProof)
		allValidSignatures = allValidSignatures && res.AllValidSignatures
		voteUpdates[blockHash] = tmi.VoteUpdate{
			Proof:       fullProof,
			PrevVersion: curPrecommitState.PrecommitBlockVersions[blockHash],
		}
	}

	if len(voteUpdates) == 0 {
		// We must have been unable to build the sign bytes or signature proof.
		// Ignore the message for now.
		return tmconsensus.HandleVoteProofsNoNewSignatures
	}

	// Now we have our updated proofs, so we can make a kernel request.
	resp := make(chan tmi.AddVoteResult, 1)
	addReq := tmi.AddPrecommitRequest{
		H: p.Height,
		R: p.Round,

		PrecommitUpdates: voteUpdates,

		Response: resp,
	}

	result, ok := gchan.ReqResp(
		ctx, m.log,
		m.addPrecommitRequests, addReq,
		resp,
		"AddPrecommit",
	)
	if !ok {
		return tmconsensus.HandleVoteProofsInternalError
	}

	switch result {
	case tmi.AddVoteAccepted:
		// We are done.
		return tmconsensus.HandleVoteProofsAccepted
	case tmi.AddVoteConflict:
		// Try all over again!
		if try > 3 {
			m.log.Info("Conflict when applying precommit, retrying", "tries", try)
		}
		try++

		// Clear out the snapshot so it can be repopulated
		// with reduced allocations.
		curPrecommitState.Reset()

		// For how long this function is, and the fact that we are jumping back near the top,
		// a goto call seems perfectly reasonable here.
		goto RETRY
	case tmi.AddVoteOutOfDate:
		// The round changed while we were processing the request.
		// Just give up now.
		return tmconsensus.HandleVoteProofsRoundTooOld
	default:
		panic(fmt.Errorf(
			"BUG: received unknown AddVoteResult %d", result,
		))
	}
}

// getSignaturesToAdd compares the current signature proofs with the incoming sparse proofs
// and extracts only the subset of proofs that are absent from the current proofs.
//
// This is part of HandlePrevoteProofs and HandlePrecommitProofs.
func (m *Mirror) getSignaturesToAdd(
	curProofs map[string]gcrypto.CommonMessageSignatureProof,
	incomingSparseProofs map[string][]gcrypto.SparseSignature,
) map[string][]gcrypto.SparseSignature {
	// The Mirror always prepopulates the nil block signature proof.
	// And we need it for a fallback if we see a signature for an unknown block,
	// to confirm valid signature key IDs.
	nilProof := curProofs[""]

	var toAdd map[string][]gcrypto.SparseSignature

	for blockHash, signatures := range incomingSparseProofs {
		fullProof := curProofs[blockHash]
		needToAdd := m.getNewSignatures(nilProof, fullProof, signatures)
		if len(needToAdd) == 0 {
			// We already had those signatures.
			continue
		}

		// Now we have a signature that needs to be added.
		if toAdd == nil {
			toAdd = make(map[string][]gcrypto.SparseSignature)
		}
		toAdd[blockHash] = needToAdd
	}

	return toAdd
}

// getNewSignatures filters incomingProofs against the given fullProof,
// returning only the signatures whose key ID is not present in fullProof.
// The nilProof argument is assumed to always be available,
// and is used as a fallback to check if the key IDs are valid,
// in the event fullProof is nil.
//
// NOTE: this could potentially change to a standalone function instead of a method.
func (m *Mirror) getNewSignatures(
	nilProof gcrypto.CommonMessageSignatureProof,
	fullProof gcrypto.CommonMessageSignatureProof,
	incomingProofs []gcrypto.SparseSignature,
) []gcrypto.SparseSignature {
	out := make([]gcrypto.SparseSignature, 0, len(incomingProofs))

	if fullProof == nil {
		// Falling back to only checking key ID validity via nilProof.
		for _, p := range incomingProofs {
			if _, valid := nilProof.HasSparseKeyID(p.KeyID); !valid {
				continue
			}

			// It is a valid key ID, so include it in the candidates to add.
			out = append(out, p)
		}

		return out
	}

	// The full proof is available, so we can use that as the source of truth.
	for _, p := range incomingProofs {
		has, valid := fullProof.HasSparseKeyID(p.KeyID)
		if valid && !has {
			out = append(out, p)
		}
	}
	return out
}

// makeNewPrevoteProof returns a signature proof for the given height, round, and block hash.
// The ok parameter is false if there was any error in generating the signing content or the proof;
// and the error is logged before returning.
func (m *Mirror) makeNewPrevoteProof(
	height uint64,
	round uint32,
	blockHash string,
	// We expect that most of the time we will not be adding a new proof,
	// so use the already available slice of validators
	// and do the work of extracting the public keys in this rarely executed method.
	trustedValidators []tmconsensus.Validator,
	pubKeyHash string,
) (p gcrypto.CommonMessageSignatureProof, ok bool) {
	vt := tmconsensus.VoteTarget{
		Height:    height,
		Round:     round,
		BlockHash: blockHash,
	}
	signContent, err := tmconsensus.PrevoteSignBytes(vt, m.sigScheme)
	if err != nil {
		m.log.Warn(
			"Failed to produce prevote sign bytes",
			"block_hash", glog.Hex(blockHash),
			"err", err,
		)
		return nil, false
	}
	emptyProof, err := m.cmspScheme.New(signContent, tmconsensus.ValidatorsToPubKeys(trustedValidators), pubKeyHash)
	if err != nil {
		m.log.Warn(
			"Failed to build signature proof",
			"block_hash", glog.Hex(blockHash),
			"err", err,
		)
		return nil, false
	}

	return emptyProof, true
}

func (m *Mirror) makeNewPrecommitProof(
	height uint64,
	round uint32,
	blockHash string,
	// We expect that most of the time we will not be adding a new proof,
	// so use the already available slice of validators
	// and do the work of extracting the public keys in this rarely executed method.
	trustedValidators []tmconsensus.Validator,
	pubKeyHash string,
) (p gcrypto.CommonMessageSignatureProof, ok bool) {
	vt := tmconsensus.VoteTarget{
		Height:    height,
		Round:     round,
		BlockHash: blockHash,
	}
	signContent, err := tmconsensus.PrecommitSignBytes(vt, m.sigScheme)
	if err != nil {
		m.log.Warn(
			"Failed to produce precommit sign bytes",
			"block_hash", glog.Hex(blockHash),
			"err", err,
		)
		return nil, false
	}
	emptyProof, err := m.cmspScheme.New(signContent, tmconsensus.ValidatorsToPubKeys(trustedValidators), pubKeyHash)
	if err != nil {
		m.log.Warn(
			"Failed to build signature proof",
			"block_hash", glog.Hex(blockHash),
			"err", err,
		)
		return nil, false
	}

	return emptyProof, true
}

// VotingView overwrites v with the current state of the mirror's voting view.
// Existing slices in v will be truncated and appended,
// so that repeated requests should be able to minimize garbage creation.
func (m *Mirror) VotingView(ctx context.Context, v *tmconsensus.VersionedRoundView) error {
	defer trace.StartRegion(ctx, "VotingView").End()

	s := tmi.Snapshot{
		Voting: v,
	}
	req := tmi.SnapshotRequest{
		Snapshot: &s,
		Ready:    make(chan struct{}),

		Fields: tmi.RVAll,
	}

	if !m.getSnapshot(ctx, req, "VotingView") {
		return context.Cause(ctx)
	}

	return nil
}

// CommittingView overwrites v with the current state of the mirror's committing view.
// Existing slices in v will be truncated and appended,
// so that repeated requests should be able to minimize garbage creation.
func (m *Mirror) CommittingView(ctx context.Context, v *tmconsensus.VersionedRoundView) error {
	defer trace.StartRegion(ctx, "CommittingView").End()

	s := tmi.Snapshot{
		Committing: v,
	}
	req := tmi.SnapshotRequest{
		Snapshot: &s,
		Ready:    make(chan struct{}),

		Fields: tmi.RVAll,
	}

	if !m.getSnapshot(ctx, req, "CommittingView") {
		return context.Cause(ctx)
	}

	return nil
}

// getSnapshot is the low-level implementation to get a copy of the current kernel state.
// This is called from multiple non-kernel methods, so the requestType parameter
// is used to distinguish log messages if the context gets cancelled.
func (m *Mirror) getSnapshot(ctx context.Context, req tmi.SnapshotRequest, requestType string) (completed bool) {
	_, ok := gchan.ReqResp(
		ctx, m.log,
		m.snapshotRequests, req,
		req.Ready,
		requestType,
	)
	return ok
}
