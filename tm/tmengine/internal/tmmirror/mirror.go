package tmmirror

import (
	"bytes"
	"context"
	"errors"
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
//
// The mirror implements [tmconsensus.ConsensusHandler];
// the [github.com/gordian-engine/gordian/tm/tmengine.Engine]
// implements the same interface, simply delegating those calls to the mirror.
//
// Most of the heavy logic within the mirror is contained in the [*tmi.Kernel].
//
// Mirror methods are safe to call concurrently.
type Mirror struct {
	log *slog.Logger

	k *tmi.Kernel

	vs pubKeyLoader
	rs roundStateLoader

	initialHeight uint64

	hashScheme tmconsensus.HashScheme
	sigScheme  tmconsensus.SignatureScheme
	cmspScheme gcrypto.CommonMessageSignatureProofScheme

	snapshotRequests   chan<- tmi.SnapshotRequest
	viewLookupRequests chan<- tmi.ViewLookupRequest

	phCheckRequests chan<- tmi.PHCheckRequest

	addPHRequests              chan<- tmconsensus.ProposedHeader
	addPrevoteRequests         chan<- tmi.AddPrevoteRequest
	addPrecommitRequests       chan<- tmi.AddPrecommitRequest
	addFuturePrevoteRequests   chan<- tmi.AddFuturePrevoteRequest
	addFuturePrecommitRequests chan<- tmi.AddFuturePrecommitRequest

	assertEnv gassert.Env
}

// roundStateLoader is a subset of [tmstore.RoundStore]
// just to avoid possible misuse of what methods we "allow" in the [Mirror].
type roundStateLoader interface {
	LoadRoundState(ctx context.Context, height uint64, round uint32) (
		phs []tmconsensus.ProposedHeader,
		prevotes, precommits tmconsensus.SparseSignatureCollection,
		err error,
	)
}

// pubKeyLoader is a subset of [tmstore.ValidatorStore]
// just to avoid possible misuse of what methods we "allow" in the [Mirror].
type pubKeyLoader interface {
	LoadPubKeys(context.Context, string) ([]gcrypto.PubKey, error)
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
	// It's fine if we don't exactly get FIFO on concurrent requests.
	addPrevoteRequests := make(chan tmi.AddPrevoteRequest)
	addPrecommitRequests := make(chan tmi.AddPrecommitRequest)
	addFuturePrevoteRequests := make(chan tmi.AddFuturePrevoteRequest)
	addFuturePrecommitRequests := make(chan tmi.AddFuturePrecommitRequest)
	kCfg.AddPrevoteRequests = addPrevoteRequests
	kCfg.AddPrecommitRequests = addPrecommitRequests
	kCfg.AddFuturePrevoteRequests = addFuturePrevoteRequests
	kCfg.AddFuturePrecommitRequests = addFuturePrecommitRequests

	k, err := tmi.NewKernel(ctx, log.With("m_sys", "kernel"), kCfg)
	if err != nil {
		// Assuming the error format doesn't need additional detail.
		return nil, err
	}

	m := &Mirror{
		log: log,

		k: k,

		vs: cfg.ValidatorStore,
		rs: cfg.RoundStore,

		initialHeight: cfg.InitialHeight,

		hashScheme: cfg.HashScheme,
		sigScheme:  cfg.SignatureScheme,
		cmspScheme: cfg.CommonMessageSignatureProofScheme,

		snapshotRequests:   snapshotRequests,
		viewLookupRequests: viewLookupRequests,
		phCheckRequests:    phCheckRequests,

		addPHRequests:              addPHRequests,
		addPrevoteRequests:         addPrevoteRequests,
		addPrecommitRequests:       addPrecommitRequests,
		addFuturePrevoteRequests:   addFuturePrevoteRequests,
		addFuturePrecommitRequests: addFuturePrecommitRequests,
	}

	return m, nil
}

// Wait blocks until the mirror's background goroutines have all completed.
// To begin shutdown, cancel the context passed to [NewMirror].
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

	// Early checks that we can do without consulting the kernel.
	if ph.ProposerPubKey == nil {
		return tmconsensus.HandleProposedHeaderMissingProposerPubKey
	}

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

	// The PrevCommitProof should be in a finalized form,
	// so we need to use the CommonMessageSignatureProofScheme to validate it.
	// But in order to do so, we need to convert the PrevCommitProof to the finalized form.
	pcp := ph.Header.PrevCommitProof
	mainHash := string(ph.Header.PrevBlockHash)
	finProof := gcrypto.FinalizedCommonMessageSignatureProof{
		Keys:       checkResp.PrevValidatorSet.PubKeys,
		PubKeyHash: pcp.PubKeyHash,

		MainSignatures: pcp.Proofs[mainHash],
	}

	finProof.MainMessage, err = tmconsensus.PrecommitSignBytes(tmconsensus.VoteTarget{
		Height:    ph.Header.Height - 1,
		Round:     pcp.Round,
		BlockHash: mainHash,
	}, m.sigScheme)
	if err != nil {
		return tmconsensus.HandleProposedHeaderInternalError
	}

	hashesBySignContent := make(map[string]string, len(pcp.Proofs))
	hashesBySignContent[string(finProof.MainMessage)] = mainHash

	if len(pcp.Proofs) > 1 {
		finProof.Rest = make(map[string][]gcrypto.SparseSignature, len(pcp.Proofs)-1)
		// There were votes for other blocks, so set up the Rest field on finProof.
		for blockHash, sigs := range pcp.Proofs {
			if blockHash == mainHash {
				// Already handled.
				continue
			}

			msg, err := tmconsensus.PrecommitSignBytes(tmconsensus.VoteTarget{
				Height:    ph.Header.Height - 1,
				Round:     pcp.Round,
				BlockHash: blockHash,
			}, m.sigScheme)
			if err != nil {
				return tmconsensus.HandleProposedHeaderInternalError
			}

			finProof.Rest[string(msg)] = sigs
			hashesBySignContent[string(msg)] = blockHash
		}
	}

	if ph.Header.Height > m.initialHeight {
		// Only confirm the previous commit proof if we are beyond the genesis height,
		// as the initial height does not have previous commit proofs.
		signBitsByHash, allSigsUnique := m.cmspScheme.ValidateFinalizedProof(
			finProof, hashesBySignContent,
		)

		if !allSigsUnique {
			return tmconsensus.HandleProposedHeaderBadPrevCommitProofDoubleSigned
		}

		if signBitsByHash == nil {
			// Pretty sure, but not 100% sure, this is the right error to return here.
			return tmconsensus.HandleProposedHeaderBadPrevCommitProofSignature
		}

		var prevBlockVotePower, availableVotePower uint64
		prevVals := checkResp.PrevValidatorSet.Validators
		sigBits := signBitsByHash[string(ph.Header.PrevBlockHash)]
		for i, v := range prevVals {
			// If we already had the total vote power,
			// we could break out of this loop as soon as we cross majority power.
			availableVotePower += v.Power
			if sigBits.Test(uint(i)) {
				prevBlockVotePower += v.Power
			}
		}

		if prevBlockVotePower < tmconsensus.ByzantineMajority(availableVotePower) {
			return tmconsensus.HandleProposedHeaderBadPrevCommitVoteCount
		}
	}

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

	if vlResp.Status == tmi.ViewFuture {
		// Special handling for this case.
		return m.handleFuturePrevoteProofs(ctx, p, vlReq)
	}

	if vlResp.Status != tmi.ViewFound {
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
	sigsToAdd := m.getSignaturesToAdd(curProofs, p.Proofs, vlReq.VRV.ValidatorSet.PubKeys)

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
				p.Height, p.Round,
				blockHash,
				curPrevoteState.ValidatorSet.PubKeys,
				string(curPrevoteState.ValidatorSet.PubKeyHash),
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

// handleFuturePrevoteProofs is a special case within HandlePrevoteProofs
// for when we receive prevote proofs for a future round
// (i.e. later than voting round plus one, or a later height).
//
// This is very similar to the flow in HandlePrevoteProofs,
// but there are special accommodations
// ƒor this being a vote beyond the current voting view.
func (m *Mirror) handleFuturePrevoteProofs(
	ctx context.Context,
	p tmconsensus.PrevoteSparseProof,
	vlReq tmi.ViewLookupRequest,
) tmconsensus.HandleVoteProofsResult {
	defer trace.StartRegion(ctx, "handleFuturePrevoteProofs").End()
	// NOTE: keep changes to this method synchronized with handleFuturePrecommitProofs.

	// Sometimes the kernel is able to assign the validator set,
	// including public keys.
	pubKeys := vlReq.VRV.ValidatorSet.PubKeys

	if len(pubKeys) == 0 {
		// The mirror didn't have the public keys loaded in memory,
		// so read them from storage.
		var err error
		pubKeys, err = m.vs.LoadPubKeys(ctx, p.PubKeyHash)
		if err != nil {
			// The only "acceptable" error for loading public keys is not finding them.
			var noHashErr tmstore.NoPubKeyHashError
			if errors.As(err, &noHashErr) {
				// Call it too far in the future if we can't identify the public keys.
				// However, if supported, it would be better to make a remote call
				// to look up the public keys.
				return tmconsensus.HandleVoteProofsFutureUnverified
			}

			// If it was any other error, fail now.
			m.log.Warn(
				"Error while looking up future public keys",
				"h", p.Height,
				"r", p.Round,
				"err", err,
			)
			return tmconsensus.HandleVoteProofsInternalError
		}
	}

	// In the normal flow with non-future views,
	// the kernel maintains the canonical set of votes in memory;
	// but since this is a future view,
	// we will load from the store directly.
	// TODO: now that we have a case for loading only prevotes,
	// it may be better to add a more direct method on RoundStore,
	// to save the effort of loading the other values.
	_, curPrevotesSparse, _, err := m.rs.LoadRoundState(ctx, vlReq.H, vlReq.R)
	if err != nil {
		// Like with loading the public keys, there is one acceptable error,
		// and anything else is a failure.
		var noRoundErr tmconsensus.RoundUnknownError
		if !errors.As(err, &noRoundErr) {
			m.log.Warn(
				"Error while looking up future prevotes",
				"h", p.Height,
				"r", p.Round,
				"err", err,
			)
			return tmconsensus.HandleVoteProofsInternalError
		}

		// Then, it was a RoundUnknownError.
		// We need to set base values in the sparse signature collection.
		curPrevotesSparse.PubKeyHash = []byte(p.PubKeyHash)
		curPrevotesSparse.BlockSignatures = make(
			map[string][]gcrypto.SparseSignature, len(p.Proofs),
		)
	}

	// Convert the prevotes we just loaded from storage,
	// into a set of full proofs, so that we can merge in the new sparse proofs.
	fullMap, err := curPrevotesSparse.ToFullPrevoteProofMap(
		p.Height, p.Round,
		pubKeys,
		m.sigScheme, m.cmspScheme,
	)
	if err != nil {
		m.log.Warn(
			"Error building full prevote map for future prevotes",
			"h", p.Height,
			"r", p.Round,
			"err", err,
		)
		return tmconsensus.HandleVoteProofsInternalError
	}

	res := gcrypto.SignatureProofMergeResult{
		AllValidSignatures: true,
	}
	for hash, sparseSigs := range p.Proofs {
		sparseProof := gcrypto.SparseSignatureProof{
			PubKeyHash: p.PubKeyHash,
			Signatures: sparseSigs,
		}

		if fullMap[hash] == nil {
			// Then the full map just owns incoming proof.
			vt := tmconsensus.VoteTarget{
				Height:    p.Height,
				Round:     p.Round,
				BlockHash: hash,
			}
			msg, err := tmconsensus.PrevoteSignBytes(vt, m.sigScheme)
			if err != nil {
				m.log.Warn(
					"Failed to get prevote sign bytes",
					"h", p.Height,
					"r", p.Round,
					"err", err,
				)
				return tmconsensus.HandleVoteProofsInternalError
			}

			fullMap[hash], err = m.cmspScheme.New(
				msg, pubKeys, p.PubKeyHash,
			)
			if err != nil {
				m.log.Warn(
					"Failed to make empty signature proof for prevotes",
					"h", p.Height,
					"r", p.Round,
					"err", err,
				)
				return tmconsensus.HandleVoteProofsInternalError
			}
		}

		res = res.Combine(fullMap[hash].MergeSparse(sparseProof))
		if !res.AllValidSignatures {
			// If we see any bad signatures,
			// don't bother processing any of the good signatures.
			return tmconsensus.HandleVoteProofsBadSignature
		}
	}

	if !res.IncreasedSignatures {
		return tmconsensus.HandleVoteProofsNoNewSignatures
	}

	// At this point, we have an updated set of full proofs,
	// and we know we've added at least one new signature.
	// So we can forward this to the kernel.
	ch := make(chan tmi.AddVoteResult, 1)
	fReq := tmi.AddFuturePrevoteRequest{
		H: vlReq.H,
		R: vlReq.R,

		PubKeyHash: []byte(p.PubKeyHash),
		PubKeys:    pubKeys,

		Prevotes: fullMap,

		Resp: ch,
	}

	result, ok := gchan.ReqResp(
		ctx, m.log,
		m.addFuturePrevoteRequests, fReq,
		ch,
		"handleFuturePrevoteProofs",
	)
	if !ok {
		return tmconsensus.HandleVoteProofsInternalError
	}

	switch result {
	case tmi.AddVoteAccepted:
		// We are done.
		return tmconsensus.HandleVoteProofsFutureVerified
	case tmi.AddVoteRedundant:
		return tmconsensus.HandleVoteProofsNoNewSignatures
	case tmi.AddVoteInternalError:
		return tmconsensus.HandleVoteProofsInternalError
	default:
		panic(fmt.Errorf(
			"BUG: received unexpected AddVoteResult %d", result,
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

	if vlResp.Status == tmi.ViewFuture {
		// Special handling for this case.
		return m.handleFuturePrecommitProofs(ctx, p, vlReq)
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
	sigsToAdd := m.getSignaturesToAdd(curProofs, p.Proofs, vlReq.VRV.ValidatorSet.PubKeys)

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
				p.Height, p.Round,
				blockHash,
				curPrecommitState.ValidatorSet.PubKeys,
				string(curPrecommitState.ValidatorSet.PubKeyHash),
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

func (m *Mirror) handleFuturePrecommitProofs(
	ctx context.Context,
	p tmconsensus.PrecommitSparseProof,
	vlReq tmi.ViewLookupRequest,
) tmconsensus.HandleVoteProofsResult {
	defer trace.StartRegion(ctx, "handleFuturePrecommitProofs").End()
	// NOTE: keep changes to this method synchronized with handleFuturePrecommitProofs.

	// Sometimes the kernel is able to assign the validator set,
	// including public keys.
	pubKeys := vlReq.VRV.ValidatorSet.PubKeys

	if len(pubKeys) == 0 {
		// The mirror didn't have the public keys loaded in memory,
		// so read them from storage.
		var err error
		pubKeys, err = m.vs.LoadPubKeys(ctx, p.PubKeyHash)
		if err != nil {
			// The only "acceptable" error for loading public keys is not finding them.
			var noHashErr tmstore.NoPubKeyHashError
			if errors.As(err, &noHashErr) {
				// Call it too far in the future if we can't identify the public keys.
				// However, if supported, it would be better to make a remote call
				// to look up the public keys.
				return tmconsensus.HandleVoteProofsFutureUnverified
			}

			// If it was any other error, fail now.
			m.log.Warn(
				"Error while looking up future public keys",
				"h", p.Height,
				"r", p.Round,
				"err", err,
			)
			return tmconsensus.HandleVoteProofsInternalError
		}
	}

	// In the normal flow with non-future views,
	// the kernel maintains the canonical set of votes in memory;
	// but since this is a future view,
	// we will load from the store directly.
	// TODO: now that we have a case for loading only precommits,
	// it may be better to add a more direct method on RoundStore,
	// to save the effort of loading the other values.
	_, _, curPrecommitsSparse, err := m.rs.LoadRoundState(ctx, vlReq.H, vlReq.R)
	if err != nil {
		// Like with loading the public keys, there is one acceptable error,
		// and anything else is a failure.
		var noRoundErr tmconsensus.RoundUnknownError
		if !errors.As(err, &noRoundErr) {
			m.log.Warn(
				"Error while looking up future precommits",
				"h", p.Height,
				"r", p.Round,
				"err", err,
			)
			return tmconsensus.HandleVoteProofsInternalError
		}

		// Then, it was a RoundUnknownError.
		// We need to set base values in the sparse signature collection.
		curPrecommitsSparse.PubKeyHash = []byte(p.PubKeyHash)
		curPrecommitsSparse.BlockSignatures = make(
			map[string][]gcrypto.SparseSignature, len(p.Proofs),
		)
	}

	// Convert the precommits we just loaded from storage,
	// into a set of full proofs, so that we can merge in the new sparse proofs.
	fullMap, err := curPrecommitsSparse.ToFullPrecommitProofMap(
		p.Height, p.Round,
		pubKeys,
		m.sigScheme, m.cmspScheme,
	)
	if err != nil {
		m.log.Warn(
			"Error building full precommit map for future precommits",
			"h", p.Height,
			"r", p.Round,
			"err", err,
		)
		return tmconsensus.HandleVoteProofsInternalError
	}

	res := gcrypto.SignatureProofMergeResult{
		AllValidSignatures: true,
	}
	for hash, sparseSigs := range p.Proofs {
		sparseProof := gcrypto.SparseSignatureProof{
			PubKeyHash: p.PubKeyHash,
			Signatures: sparseSigs,
		}

		if fullMap[hash] == nil {
			// Then the full map just owns incoming proof.
			vt := tmconsensus.VoteTarget{
				Height:    p.Height,
				Round:     p.Round,
				BlockHash: hash,
			}
			msg, err := tmconsensus.PrecommitSignBytes(vt, m.sigScheme)
			if err != nil {
				m.log.Warn(
					"Failed to get precommit sign bytes",
					"h", p.Height,
					"r", p.Round,
					"err", err,
				)
				return tmconsensus.HandleVoteProofsInternalError
			}

			fullMap[hash], err = m.cmspScheme.New(
				msg, pubKeys, p.PubKeyHash,
			)
			if err != nil {
				m.log.Warn(
					"Failed to make empty signature proof for precommits",
					"h", p.Height,
					"r", p.Round,
					"err", err,
				)
				return tmconsensus.HandleVoteProofsInternalError
			}
		}

		res = res.Combine(fullMap[hash].MergeSparse(sparseProof))
		if !res.AllValidSignatures {
			// If we see any bad signatures,
			// don't bother processing any of the good signatures.
			return tmconsensus.HandleVoteProofsBadSignature
		}
	}

	if !res.IncreasedSignatures {
		return tmconsensus.HandleVoteProofsNoNewSignatures
	}

	// At this point, we have an updated set of full proofs,
	// and we know we've added at least one new signature.
	// So we can forward this to the kernel.
	ch := make(chan tmi.AddVoteResult, 1)
	fReq := tmi.AddFuturePrecommitRequest{
		H: vlReq.H,
		R: vlReq.R,

		PubKeyHash: []byte(p.PubKeyHash),
		PubKeys:    pubKeys,

		Precommits: fullMap,

		Resp: ch,
	}

	result, ok := gchan.ReqResp(
		ctx, m.log,
		m.addFuturePrecommitRequests, fReq,
		ch,
		"handleFuturePrecommitProofs",
	)
	if !ok {
		return tmconsensus.HandleVoteProofsInternalError
	}

	switch result {
	case tmi.AddVoteAccepted:
		// We are done.
		return tmconsensus.HandleVoteProofsFutureVerified
	case tmi.AddVoteRedundant:
		return tmconsensus.HandleVoteProofsNoNewSignatures
	case tmi.AddVoteInternalError:
		return tmconsensus.HandleVoteProofsInternalError
	default:
		panic(fmt.Errorf(
			"BUG: received unexpected AddVoteResult %d", result,
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
	pubKeys []gcrypto.PubKey,
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
	pubKeys []gcrypto.PubKey,
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
	emptyProof, err := m.cmspScheme.New(signContent, pubKeys, pubKeyHash)
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
	pubKeys []gcrypto.PubKey,
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
	emptyProof, err := m.cmspScheme.New(signContent, pubKeys, pubKeyHash)
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
