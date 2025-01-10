package tmmirror

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"runtime/trace"

	"github.com/gordian-engine/gordian/gassert"
	"github.com/gordian-engine/gordian/gcrypto"
	"github.com/gordian-engine/gordian/gwatchdog"
	"github.com/gordian-engine/gordian/internal/gchan"
	"github.com/gordian-engine/gordian/internal/glog"
	"github.com/gordian-engine/gordian/tm/tmconsensus"
	"github.com/gordian-engine/gordian/tm/tmengine/internal/tmeil"
	"github.com/gordian-engine/gordian/tm/tmengine/internal/tmemetrics"
	"github.com/gordian-engine/gordian/tm/tmengine/internal/tmmirror/internal/tmi"
	"github.com/gordian-engine/gordian/tm/tmengine/tmelink"
	"github.com/gordian-engine/gordian/tm/tmstore"
)

// Mirror maintains a read-only view of the chain state,
// based on inputs from the network.
type Mirror struct {
	log *slog.Logger

	k *tmi.Kernel

	initialHeight uint64

	hashScheme tmconsensus.HashScheme
	sigScheme  tmconsensus.SignatureScheme
	cmspScheme gcrypto.CommonMessageSignatureProofScheme

	snapshotRequests   chan<- tmi.SnapshotRequest
	viewLookupRequests chan<- tmi.ViewLookupRequest

	phCheckRequests chan<- tmi.PHCheckRequest

	addPHRequests        chan<- tmconsensus.ProposedHeader
	addPrevoteRequests   chan<- tmi.AddPrevoteRequest
	addPrecommitRequests chan<- tmi.AddPrecommitRequest

	assertEnv gassert.Env
}

// MirrorConfig holds the configuration required to start a [Mirror].
type MirrorConfig struct {
	Store                tmstore.MirrorStore
	CommittedHeaderStore tmstore.CommittedHeaderStore
	RoundStore           tmstore.RoundStore
	ValidatorStore       tmstore.ValidatorStore

	InitialHeight       uint64
	InitialValidatorSet tmconsensus.ValidatorSet

	HashScheme                        tmconsensus.HashScheme
	SignatureScheme                   tmconsensus.SignatureScheme
	CommonMessageSignatureProofScheme gcrypto.CommonMessageSignatureProofScheme

	ProposedHeaderFetcher tmelink.ProposedHeaderFetcher

	ReplayedHeadersIn <-chan tmelink.ReplayedHeaderRequest
	GossipStrategyOut chan<- tmelink.NetworkViewUpdate
	LagStateOut       chan<- tmelink.LagState

	StateMachineRoundEntranceIn <-chan tmeil.StateMachineRoundEntrance
	StateMachineRoundViewOut    chan<- tmeil.StateMachineRoundView

	MetricsCollector *tmemetrics.Collector

	Watchdog *gwatchdog.Watchdog

	AssertEnv gassert.Env
}

// toKernelConfig copies the fields from c that are duplicated in the kernel config.
func (c MirrorConfig) toKernelConfig() tmi.KernelConfig {
	return tmi.KernelConfig{
		Store:                c.Store,
		CommittedHeaderStore: c.CommittedHeaderStore,
		RoundStore:           c.RoundStore,
		ValidatorStore:       c.ValidatorStore,

		HashScheme:                        c.HashScheme,
		SignatureScheme:                   c.SignatureScheme,
		CommonMessageSignatureProofScheme: c.CommonMessageSignatureProofScheme,

		InitialHeight:       c.InitialHeight,
		InitialValidatorSet: c.InitialValidatorSet,

		ProposedHeaderFetcher: c.ProposedHeaderFetcher,

		ReplayedHeadersIn: c.ReplayedHeadersIn,
		GossipStrategyOut: c.GossipStrategyOut,
		LagStateOut:       c.LagStateOut,

		StateMachineRoundEntranceIn: c.StateMachineRoundEntranceIn,
		StateMachineRoundViewOut:    c.StateMachineRoundViewOut,

		MetricsCollector: c.MetricsCollector,

		Watchdog: c.Watchdog,

		AssertEnv: c.AssertEnv,
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
	phCheckRequests := make(chan tmi.PHCheckRequest)
	kCfg.PHCheckRequests = phCheckRequests

	// Arbitrarily sized to allow some concurrent requests,
	// with low likelihood of blocking.
	addPHRequests := make(chan tmconsensus.ProposedHeader, 8)
	kCfg.AddPHRequests = addPHRequests

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

		initialHeight: cfg.InitialHeight,

		hashScheme: cfg.HashScheme,
		sigScheme:  cfg.SignatureScheme,
		cmspScheme: cfg.CommonMessageSignatureProofScheme,

		snapshotRequests:   snapshotRequests,
		viewLookupRequests: viewLookupRequests,
		phCheckRequests:    phCheckRequests,

		addPHRequests:        addPHRequests,
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

// HandleProposedHeader satisfies the [tmconsensus.ConsensusHandler] interface.
//
// The [tmengine.Engine] also has a HandleProposedHeader method with a matching signature;
// calling that method on the Engine just delegates to the engine's mirror,
// i.e. this method.
//
// This method first makes a "check proposed header" request to the kernel
// to do some very lightweight validation determining whether the
// proposed header may be applied.
// If that lightweight validation passes, this method does a more thorough check,
// confirming correct signatures, before requesting that the kernel
// actually adds the proposed header.
// This minimizes time spent in the kernel's main loop,
// by spending the time in this method instead.
func (m *Mirror) HandleProposedHeader(ctx context.Context, ph tmconsensus.ProposedHeader) tmconsensus.HandleProposedHeaderResult {
	defer trace.StartRegion(ctx, "HandleProposedHeader").End()

RESTART:
	req := tmi.PHCheckRequest{
		PH:   ph,
		Resp: make(chan tmi.PHCheckResponse, 1),
	}
	checkResp, ok := gchan.ReqResp(
		ctx, m.log,
		m.phCheckRequests, req,
		req.Resp,
		"HandleProposedHeader:PHCheck",
	)
	if !ok {
		return tmconsensus.HandleProposedHeaderInternalError
	}

	if checkResp.Status == tmi.PHCheckAlreadyHaveSignature {
		// Easy early return case.
		// We will say it's already stored.
		// Note, this is only a lightweight signature comparison,
		// so a maliciously crafted proposed block matching an existing signature
		// may be propagated through the network.
		// TODO: do a deep comparison to see if the proposed block matches,
		// and possibly return a new status if the signature is forged.
		return tmconsensus.HandleProposedHeaderAlreadyStored
	}

	switch checkResp.Status {
	case tmi.PHCheckAcceptable:
		// Okay.
	case tmi.PHCheckSignerUnrecognized:
		// Cannot continue.
		return tmconsensus.HandleProposedHeaderSignerUnrecognized
	case tmi.PHCheckNextHeight:
		// Special case: we make an additional request to the kernel if the PH is for the next height.
		m.backfillCommitForNextHeightPE(ctx, req.PH)
		goto RESTART // TODO: find a cleaner way to apply the proposed block after backfilling commit.
	case tmi.PHCheckRoundTooOld:
		return tmconsensus.HandleProposedHeaderRoundTooOld
	case tmi.PHCheckRoundTooFarInFuture:
		return tmconsensus.HandleProposedHeaderRoundTooFarInFuture
	default:
		panic(fmt.Errorf("TODO: handle PHCheck status %s", checkResp.Status))
	}

	// Arbitrarily choosing to validate the block hash before the signature.
	wantHash, err := m.hashScheme.Block(ph.Header)
	if err != nil {
		return tmconsensus.HandleProposedHeaderInternalError
	}

	if !bytes.Equal(wantHash, ph.Header.Hash) {
		// Actual hash didn't match expected hash:
		// this message should not be on the network.
		return tmconsensus.HandleProposedHeaderBadBlockHash
	}

	// Validate the signature based on the public key the kernel reported.
	signContent, err := tmconsensus.ProposalSignBytes(ph.Header, ph.Round, ph.Annotations, m.sigScheme)
	if err != nil {
		return tmconsensus.HandleProposedHeaderInternalError
	}
	if !checkResp.ProposerPubKey.Verify(signContent, ph.Signature) {
		return tmconsensus.HandleProposedHeaderBadSignature
	}

	// Now, make sure that the proposed header's PrevCommitProof matches
	// what we think the previous commit is supposed to be.
	// The easiest thing to check first is the validator hash.
	if string(checkResp.PrevValidatorSet.PubKeyHash) != ph.Header.PrevCommitProof.PubKeyHash {
		return tmconsensus.HandleProposedHeaderBadPrevCommitProofPubKeyHash
	}

	// Now confirm that every signature is valid.
	// This approach is potentially expensive,
	// and I don't like that it happens on an uncontrolled path.
	// There are likely some optimizations we can make to only do this work once
	// and cache the results.
	rawProofs := make(map[string]gcrypto.CommonMessageSignatureProof, len(ph.Header.PrevCommitProof.Proofs))
	pubKeys := tmconsensus.ValidatorsToPubKeys(checkResp.PrevValidatorSet.Validators)
	for hash, sigs := range ph.Header.PrevCommitProof.Proofs {
		// TODO: should we reject if len(sigs) == 0? Probably yes?

		vt := tmconsensus.VoteTarget{
			Height: ph.Header.Height - 1,

			// We just pull this from the incoming previous commit proof,
			// as opposed to getting it from the kernel somehow,
			// because it is part of the signature.
			// If the round is wrong then the signatures will be invalid.
			Round: ph.Header.PrevCommitProof.Round,

			BlockHash: hash,
		}
		msg, err := tmconsensus.PrecommitSignBytes(vt, m.sigScheme)
		if err != nil {
			m.log.Warn(
				"Failed to build precommit sign bytes",
				// TODO: what fields would add pertinent information here?
				"err", err,
			)
			return tmconsensus.HandleProposedHeaderInternalError
		}
		proof, err := m.cmspScheme.New(msg, pubKeys, string(checkResp.PrevValidatorSet.PubKeyHash))
		if err != nil {
			m.log.Warn(
				"Failed to build common message signature proof when handling proposed header",
				// TODO: what fields would add pertinent information here?
				"err", err,
			)
			return tmconsensus.HandleProposedHeaderInternalError
		}

		sparseProof := gcrypto.SparseSignatureProof{
			PubKeyHash: ph.Header.PrevCommitProof.PubKeyHash,
			Signatures: sigs,
		}
		if res := proof.MergeSparse(sparseProof); !res.AllValidSignatures {
			m.log.Warn(
				"Failed to merge sparse proof",
				"prev_pub_key_hash", glog.Hex(checkResp.PrevValidatorSet.PubKeyHash),
				"incoming_pub_key_hash", glog.Hex(ph.Header.PrevCommitProof.PubKeyHash),
			)
			return tmconsensus.HandleProposedHeaderBadPrevCommitProofSignature
		}

		rawProofs[hash] = proof
	}

	// TODO: confirm that we have majority voting power on the previous block hash.

	// The hash matches and the proposed header was signed by a validator we know,
	// so we can accept the message.

	// Fire-and-forget a request to the kernel, to add this proposed block.
	// The m.addPHRequests channel has a larger buffer
	// for a relative guarantee that this send won't block.
	// But if it does, that's okay, it's effective backpressure at that point.
	_ = gchan.SendC(
		ctx, m.log,
		m.addPHRequests, ph,
		"requesting proposed header to be added",
	)

	// Is accepting here sufficient?
	// We could adjust the addPHRequests channel to respond with a value if needed.
	return tmconsensus.HandleProposedHeaderAccepted
}

func (m *Mirror) backfillCommitForNextHeightPE(
	ctx context.Context,
	ph tmconsensus.ProposedHeader,
) backfillCommitStatus {
	defer trace.StartRegion(ctx, "backfillCommitForNextHeightPE").End()

	res := m.handlePrecommitProofs(ctx, tmconsensus.PrecommitSparseProof{
		Height: ph.Header.Height - 1,
		Round:  ph.Header.PrevCommitProof.Round,

		PubKeyHash: ph.Header.PrevCommitProof.PubKeyHash,

		Proofs: ph.Header.PrevCommitProof.Proofs,
	}, "(*Mirror).backfillCommitForNextHeightPE")

	if res != tmconsensus.HandleVoteProofsAccepted {
		return backfillCommitRejected
	}

	return backfillCommitAccepted
}

func (m *Mirror) HandlePrevoteProofs(ctx context.Context, p tmconsensus.PrevoteSparseProof) tmconsensus.HandleVoteProofsResult {
	defer trace.StartRegion(ctx, "HandlePrevoteProofs").End()

	// NOTE: keep changes to this method synchronized with handlePrecommitProofs --
	// yes, the unexported version.

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

	if p.PubKeyHash != string(curPrevoteState.ValidatorSet.PubKeyHash) {
		// We assume our view of the network is correct,
		// and so we refuse to continue propagating this message
		// containing a validator hash mismatch.
		return tmconsensus.HandleVoteProofsBadPubKeyHash
	}

	curProofs := curPrevoteState.PrevoteProofs
	sigsToAdd := m.getSignaturesToAdd(curProofs, p.Proofs, vlReq.VRV.ValidatorSet)

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
			emptyProof, ok := m.makeNewPrevoteProof(
				p.Height, p.Round, blockHash, curPrevoteState.ValidatorSet,
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

// handlePrecommitProofs is the main logic for accepting precommit proofs.
// Unlike HandlePrevoteProofs, this is called from both
// the exported HandlePrecommitProofs for handling incoming gossip messages,
// but also from backfilling precommits due to seeing a valid proposed header
// earlier than expected.
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
			"TODO: handle precommits for views other than committing, voting, or next round (got %s)",
			vlResp.ID,
		))
	}

	if p.PubKeyHash != string(curPrecommitState.ValidatorSet.PubKeyHash) {
		// We assume our view of the network is correct,
		// and so we refuse to continue propagating this message
		// containing a validator hash mismatch.
		return tmconsensus.HandleVoteProofsBadPubKeyHash
	}

	curProofs := curPrecommitState.PrecommitProofs
	sigsToAdd := m.getSignaturesToAdd(curProofs, p.Proofs, vlReq.VRV.ValidatorSet)

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
			emptyProof, ok := m.makeNewPrecommitProof(
				p.Height, p.Round, blockHash, curPrecommitState.ValidatorSet,
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
// The higher-level mirror handles this, in a goroutine independent from the mirror kernel,
// in order to minimize kernel load.
//
// This is part of HandlePrevoteProofs and HandlePrecommitProofs.
func (m *Mirror) getSignaturesToAdd(
	curProofs map[string]gcrypto.CommonMessageSignatureProof,
	incomingSparseProofs map[string][]gcrypto.SparseSignature,
	valSet tmconsensus.ValidatorSet,
) map[string][]gcrypto.SparseSignature {
	var toAdd map[string][]gcrypto.SparseSignature

	var keyIDChecker gcrypto.KeyIDChecker

	for blockHash, signatures := range incomingSparseProofs {
		fullProof := curProofs[blockHash]
		var sigsToAdd []gcrypto.SparseSignature

		if fullProof == nil {
			// We don't have a full proof to consult, so go through the scheme.
			// We could probably pick an arbitrary full proof, if we have any, to check validity.
			// But if we don't we have to use the scheme anyway.

			if keyIDChecker == nil {
				// Only do this allocation once.
				pubKeys := tmconsensus.ValidatorsToPubKeys(valSet.Validators)
				keyIDChecker = m.cmspScheme.KeyIDChecker(pubKeys)
			}

			for _, sig := range signatures {
				// TODO: this is the only time we need the pubkeys,
				// and in the case of aggregated signatures,
				// this can be considerably expensive.
				// So, the CommonMessageSignatureProofScheme interface
				// needs to change so that we can produce a key ID validator only once.
				if !keyIDChecker.IsValid(sig.KeyID) {
					continue
				}

				sigsToAdd = append(sigsToAdd, sig)
			}
		} else {
			// We have an existing full proof, so we can use that to validate the key ID.
			for _, sig := range signatures {
				has, valid := fullProof.HasSparseKeyID(sig.KeyID)
				if valid && !has {
					sigsToAdd = append(sigsToAdd, sig)
				}
			}
		}

		if len(sigsToAdd) == 0 {
			// We already had the provided signatures for this block hash,
			// or there were unrecognized keys that we skipped.
			continue
		}

		// Now we have at least one signature that needs to be added.
		if toAdd == nil {
			toAdd = make(map[string][]gcrypto.SparseSignature)
		}
		toAdd[blockHash] = sigsToAdd
	}

	return toAdd
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
	valSet tmconsensus.ValidatorSet,
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
	emptyProof, err := m.cmspScheme.New(
		signContent,
		tmconsensus.ValidatorsToPubKeys(valSet.Validators),
		string(valSet.PubKeyHash),
	)
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
	valSet tmconsensus.ValidatorSet,
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
	emptyProof, err := m.cmspScheme.New(
		signContent,
		tmconsensus.ValidatorsToPubKeys(valSet.Validators),
		string(valSet.PubKeyHash),
	)
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
