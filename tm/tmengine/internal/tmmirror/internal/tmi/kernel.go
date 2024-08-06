package tmi

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"runtime/trace"
	"slices"
	"time"

	"github.com/rollchains/gordian/gcrypto"
	"github.com/rollchains/gordian/gwatchdog"
	"github.com/rollchains/gordian/internal/glog"
	"github.com/rollchains/gordian/tm/tmconsensus"
	"github.com/rollchains/gordian/tm/tmengine/internal/tmeil"
	"github.com/rollchains/gordian/tm/tmengine/internal/tmemetrics"
	"github.com/rollchains/gordian/tm/tmengine/tmelink"
	"github.com/rollchains/gordian/tm/tmstore"
)

type Kernel struct {
	log *slog.Logger

	store  tmstore.MirrorStore
	bStore tmstore.BlockStore
	rStore tmstore.RoundStore
	vStore tmstore.ValidatorStore

	hashScheme tmconsensus.HashScheme
	sigScheme  tmconsensus.SignatureScheme
	cmspScheme gcrypto.CommonMessageSignatureProofScheme

	// Required for certain edge cases.
	// Usually 1.
	initialHeight uint64
	initialVals   []tmconsensus.Validator

	pbf tmelink.ProposedBlockFetcher
	mc  *tmemetrics.Collector

	gossipOutCh chan<- tmelink.NetworkViewUpdate

	stateMachineRoundEntranceIn <-chan tmeil.StateMachineRoundEntrance

	snapshotRequests   <-chan SnapshotRequest
	viewLookupRequests <-chan ViewLookupRequest
	pbCheckRequests    <-chan PBCheckRequest

	addPBRequests        <-chan tmconsensus.ProposedBlock
	addPrevoteRequests   <-chan AddPrevoteRequest
	addPrecommitRequests <-chan AddPrecommitRequest

	done chan struct{}
}

type KernelConfig struct {
	Store          tmstore.MirrorStore
	BlockStore     tmstore.BlockStore
	RoundStore     tmstore.RoundStore
	ValidatorStore tmstore.ValidatorStore

	HashScheme                        tmconsensus.HashScheme
	SignatureScheme                   tmconsensus.SignatureScheme
	CommonMessageSignatureProofScheme gcrypto.CommonMessageSignatureProofScheme

	InitialHeight     uint64
	InitialValidators []tmconsensus.Validator

	ProposedBlockFetcher tmelink.ProposedBlockFetcher

	GossipStrategyOut chan<- tmelink.NetworkViewUpdate

	StateMachineRoundEntranceIn <-chan tmeil.StateMachineRoundEntrance

	// View sent to the state machine.
	// It should usually map to the voting view,
	// but it will occasionally "blip" to the committing view
	// when the Mirror considers a round committed
	// while the state machine is in a Commit Wait phase.
	StateMachineRoundViewOut chan<- tmeil.StateMachineRoundView

	NHRRequests        <-chan chan NetworkHeightRound
	SnapshotRequests   <-chan SnapshotRequest
	ViewLookupRequests <-chan ViewLookupRequest
	PBCheckRequests    <-chan PBCheckRequest

	AddPBRequests        <-chan tmconsensus.ProposedBlock
	AddPrevoteRequests   <-chan AddPrevoteRequest
	AddPrecommitRequests <-chan AddPrecommitRequest

	MetricsCollector *tmemetrics.Collector

	Watchdog *gwatchdog.Watchdog
}

func NewKernel(ctx context.Context, log *slog.Logger, cfg KernelConfig) (*Kernel, error) {
	nhr, err := NetworkHeightRoundFromStore(cfg.Store.NetworkHeightRound(ctx))
	if err != nil && err != tmstore.ErrStoreUninitialized {
		return nil, fmt.Errorf(
			"cannot initialize mirror kernel: failed to retrieve stored network height/round: %w",
			err,
		)
	}
	if err == tmstore.ErrStoreUninitialized {
		nhr = NetworkHeightRound{
			VotingHeight: cfg.InitialHeight,
			// Committing height stays at zero,
			// because we don't have a committing view until the first block reaches commit.
		}
		if err := cfg.Store.SetNetworkHeightRound(nhr.ForStore(ctx)); err != nil {
			return nil, fmt.Errorf(
				"cannot initialize mirror kernel: failed to set initial height/round on store: %w",
				err,
			)
		}
	}

	k := &Kernel{
		log: log,

		store:  cfg.Store,
		bStore: cfg.BlockStore,
		rStore: cfg.RoundStore,
		vStore: cfg.ValidatorStore,

		hashScheme: cfg.HashScheme,
		sigScheme:  cfg.SignatureScheme,
		cmspScheme: cfg.CommonMessageSignatureProofScheme,

		initialHeight: cfg.InitialHeight,
		initialVals:   slices.Clone(cfg.InitialValidators),

		pbf: cfg.ProposedBlockFetcher,
		mc:  cfg.MetricsCollector,

		// Channels provided through the config,
		// i.e. channels coordinated by the Engine or Mirror.
		gossipOutCh: cfg.GossipStrategyOut,

		stateMachineRoundEntranceIn: cfg.StateMachineRoundEntranceIn,

		snapshotRequests:   cfg.SnapshotRequests,
		viewLookupRequests: cfg.ViewLookupRequests,
		pbCheckRequests:    cfg.PBCheckRequests,

		addPBRequests:        cfg.AddPBRequests,
		addPrevoteRequests:   cfg.AddPrevoteRequests,
		addPrecommitRequests: cfg.AddPrecommitRequests,

		done: make(chan struct{}),
	}

	// Seed the initial state with view heights and rounds,
	// so the loadInitial* calls have sufficient information.
	initState := kState{
		Committing: tmconsensus.VersionedRoundView{
			RoundView: tmconsensus.RoundView{
				Height: nhr.CommittingHeight,
				Round:  nhr.CommittingRound,
			},
		},
		Voting: tmconsensus.VersionedRoundView{
			RoundView: tmconsensus.RoundView{
				Height: nhr.VotingHeight,
				Round:  nhr.VotingRound,
			},
		},
		// Not necessary to prepopulate NextRound,
		// as that will happen in k.loadInitialVotingView.

		InFlightFetchPBs: make(map[string]context.CancelFunc),

		StateMachineViewManager: newStateMachineViewManager(cfg.StateMachineRoundViewOut),

		GossipViewManager: newGossipViewManager(cfg.GossipStrategyOut),
	}

	// Have to load the committing view first,
	// because the voting view depends on the block being committed.
	if nhr.CommittingHeight >= cfg.InitialHeight {
		if err := k.loadInitialCommittingView(ctx, &initState); err != nil {
			// Error assumed to be already formatted correctly.
			return nil, err
		}
	}

	if err := k.loadInitialVotingView(ctx, &initState); err != nil {
		// Error assumed to be already formatted correctly.
		return nil, err
	}

	if err := k.updateObservers(ctx, &initState); err != nil {
		return nil, err
	}

	go k.mainLoop(ctx, &initState, cfg.Watchdog)

	return k, nil
}

func (k *Kernel) Wait() {
	<-k.done
}

func (k *Kernel) mainLoop(ctx context.Context, s *kState, wd *gwatchdog.Watchdog) {
	ctx, task := trace.NewTask(ctx, "Mirror.kernel.mainLoop")
	defer task.End()

	defer close(k.done)

	defer func() {
		if !gwatchdog.IsTermination(ctx) {
			return
		}

		nvrVal := slog.StringValue("<nil>")
		if s.GossipViewManager.NilVotedRound != nil {
			nvrVal = s.GossipViewManager.NilVotedRound.LogValue()
		}

		k.log.Info(
			"WATCHDOG TERMINATING; DUMPING STATE",
			"kState", slog.GroupValue(
				slog.Any("CommittingVRV", s.Committing),
				slog.Any("VotingVRV", s.Voting),
				slog.Any("NextRoundVRV", s.NextRound),
				slog.Any("NilVotedRound", nvrVal),
			),
		)
	}()

	wSig := wd.Monitor(ctx, gwatchdog.MonitorConfig{
		Name:     "Mirror kernel",
		Interval: 10 * time.Second, Jitter: time.Second,
		ResponseTimeout: time.Second,
	})

	for {
		smOut := s.StateMachineViewManager.Output(s)

		gsOut := s.GossipViewManager.Output()

		select {
		case <-ctx.Done():
			k.log.Info(
				"Mirror kernel stopping",
				"cause", context.Cause(ctx),
				"committing_height", s.CommittingBlock.Height,
				"committing_hash", glog.Hex(s.CommittingBlock.Hash),
				"voting_height", s.Voting.Height,
				"voting_round", s.Voting.Round,
				"voting_vote_summary", s.Voting.VoteSummary,
				"state_machine_height", s.StateMachineViewManager.H(),
				"state_machine_round", s.StateMachineViewManager.R(),
			)
			return

		case req := <-k.snapshotRequests:
			k.sendSnapshotResponse(ctx, s, req)

		case req := <-k.viewLookupRequests:
			k.sendViewLookupResponse(ctx, s, req)

		case req := <-k.pbCheckRequests:
			k.sendPBCheckResponse(ctx, s, req)

		case pb := <-k.addPBRequests:
			k.addPB(ctx, s, pb)

		case req := <-k.addPrevoteRequests:
			k.addPrevote(ctx, s, req)

		case req := <-k.addPrecommitRequests:
			k.addPrecommit(ctx, s, req)

		case gsOut.Ch <- gsOut.Val:
			gsOut.MarkSent()

		case smOut.Ch <- smOut.Val:
			smOut.MarkSent()

		case pb := <-k.pbf.FetchedProposedBlocks:
			k.addPB(ctx, s, pb)

		case re := <-k.stateMachineRoundEntranceIn:
			k.handleStateMachineRoundEntrance(ctx, s, re)

		case act := <-s.StateMachineViewManager.Actions():
			k.handleStateMachineAction(ctx, s, act)

		case sig := <-wSig:
			close(sig.Alive)
		}
	}
}

// addPB adds a proposed block to the current round state.
// This is called both from a direct add proposed block request (from the Mirror layer)
// and from an out-of-band fetched proposed block's arrival.
func (k *Kernel) addPB(ctx context.Context, s *kState, pb tmconsensus.ProposedBlock) {
	defer trace.StartRegion(ctx, "addPB").End()

	// Before any other work, cancel an outstanding fetch for this PB.
	if cancel, ok := s.InFlightFetchPBs[string(pb.Block.Hash)]; ok {
		cancel()
		delete(s.InFlightFetchPBs, string(pb.Block.Hash))
	}

	vrv, viewID, _ := s.FindView(pb.Block.Height, pb.Round, "(*Kernel).addPB")
	if vrv == nil {
		k.log.Info(
			"Dropping proposed block that did not match a view (may have been received immediately before a view shift)",
			"pb_height", pb.Block.Height, "pb_round", pb.Round,
			"voting_height", s.Voting.Height, "voting_round", s.Voting.Round,
		)
		return
	}

	// If we concurrently handled multiple requests for the same proposed block,
	// the goroutines calling into HandleProposedBlock would have seen the same original view
	// and would both request the same block to be added.
	// Since those add blocks are serialized into the kernel,
	// we now need to make sure this isn't a duplicate.
	for _, have := range vrv.ProposedBlocks {
		// HandleProposedBlock should have done all the validation,
		// and we assume it is impossible for two distinct blocks
		// to have an identical signature.
		if bytes.Equal(have.Signature, pb.Signature) {
			// Not logging the duplicate block drop, as it is not very informative.
			return
		}
	}

	// On the right height/round, no duplicate detected,
	// so we can add the proposed block.
	vrv.ProposedBlocks = append(vrv.ProposedBlocks, pb)

	// Persist the change before updating local state.
	if err := k.rStore.SaveProposedBlock(ctx, pb); err != nil {
		glog.HRE(k.log, pb.Block.Height, pb.Round, err).Warn(
			"Failed to save proposed block to round store; this may cause issues upon restart",
		)
		// Continue anyway despite failure.
	}

	s.MarkViewUpdated(viewID)

	if viewID != ViewIDVoting && viewID != ViewIDNextRound {
		// The rest of the method assumes we merged the proposed block into the current height.
		return
	}

	// Also, now that we saved this proposed block,
	// we need to check if it had commit info for our previous height.
	// This applies whether we added the proposed block into voting or next round,
	// which is guaranteed by the prior guard clause.
	backfillVRV := &s.Committing

	// TODO: this merging code should probably move to a function in gcrypto.
	commitProofs := pb.Block.PrevCommitProof.Proofs
	mergedAny := false
	for blockHash, laterSigs := range commitProofs {
		target := backfillVRV.PrecommitProofs[blockHash]
		if target == nil {
			panic("TODO: backfill unknown block precommit")
		}

		laterSparseCommit := gcrypto.SparseSignatureProof{
			PubKeyHash: pb.Block.PrevCommitProof.PubKeyHash,
			Signatures: laterSigs,
		}

		mergeRes := target.MergeSparse(laterSparseCommit)
		mergedAny = mergedAny || mergeRes.IncreasedSignatures
	}

	if mergedAny {
		// We've updated the previous precommits, so the round store needs updated.
		if err := k.rStore.OverwritePrecommitProofs(
			ctx,
			pb.Block.Height-1, pb.Block.PrevCommitProof.Round, // TODO: Don't assume this matches the committing view.
			backfillVRV.PrecommitProofs,
		); err != nil {
			glog.HRE(k.log, pb.Block.Height, pb.Round, err).Warn(
				"Failed to save backfilled commit info to round store; this may cause issues upon restart",
			)
		}

		// Also update the committing view.
		s.MarkCommittingViewUpdated()
	}

	// Finally, since we know at this point we've added a new proposed block,
	// we need to double check whether we need to do a view shift.
	// This is only applicable to a proposed block on the voting view.
	// If we had >1/3 votes in NextRound,
	// we would have already shifted the NextRound into Voting
	// upon receipt of those votes.
	//
	// We can probably use a more sophisticated heuristic to avoid work in checking the view shift,
	// but for now, we will check for a view shift if this proposed block
	// has any precommits, indicating we've received this block later than expected.
	if viewID == ViewIDVoting {
		if _, ok := s.Voting.PrecommitProofs[string(pb.Block.Hash)]; ok {
			k.checkVotingPrecommitViewShift(ctx, s)
		}
	}
}

// addPrevote is the kernel method to add prevotes to the current state.
// The non-kernel HandlePrevoteProofs method takes a snapshot of the then-current kernel state,
// and eagerly updates that copy with the new prevotes from the network.
// Then it notifies the kernel of the new prevotes and the previous version.
// For any new prevotes where the previous version matches our currently understood previous version,
// the new version is applied immediately.
// If any versions are out of date, we notify the caller that there was a conflict,
// and they may try again.
func (k *Kernel) addPrevote(ctx context.Context, s *kState, req AddPrevoteRequest) {
	defer trace.StartRegion(ctx, "addPrevote").End()

	// NOTE: keep changes to this method synchronized with addPrecommit.

	vrv, vID, vStatus := s.FindView(req.H, req.R, "(*Kernel).addPrevote")
	if vStatus != ViewFound {
		switch vStatus {
		case ViewBeforeCommitting, ViewOrphaned:
			if req.Response != nil {
				req.Response <- AddVoteOutOfDate
			}
			return
		case ViewWrongCommit:
			k.log.Warn("TODO: add new addVoteResult for viewWrongCommit")
			if req.Response != nil {
				req.Response <- AddVoteOutOfDate
			}
			return
		default:
			panic(fmt.Errorf(
				"TODO: handle unexpected view status (%s) when looking up view to add prevote",
				vStatus,
			))
		}
	}
	if vID != ViewIDCommitting && vID != ViewIDVoting && vID != ViewIDNextRound {
		panic(fmt.Errorf(
			"TODO: handle adding prevotes to %s view", vID,
		))
	}

	// Assume the votes will be accepted, then invalidate that if needed.
	allAccepted := true
	anyAdded := false
	for blockHash, u := range req.PrevoteUpdates {
		if u.PrevVersion == vrv.PrevoteBlockVersions[blockHash] {
			// Then we can apply this particular change.
			vrv.PrevoteProofs[blockHash] = u.Proof
			if vrv.PrevoteBlockVersions == nil {
				vrv.PrevoteBlockVersions = make(map[string]uint32)
			}
			vrv.PrevoteBlockVersions[blockHash]++
			anyAdded = true
		} else {
			allAccepted = false
		}
	}

	// Bookkeeping.
	if anyAdded {
		vrv.VoteSummary.SetPrevotePowers(vrv.Validators, vrv.PrevoteProofs)
		s.MarkViewUpdated(vID)

		if err := k.rStore.OverwritePrevoteProofs(
			ctx,
			req.H, req.R,
			vrv.PrevoteProofs,
		); err != nil {
			glog.HRE(k.log, req.H, req.R, err).Warn(
				"Failed to save prevotes to round store; this may cause issues upon restart",
			)
		}
	}

	var res AddVoteResult
	if allAccepted {
		res = AddVoteAccepted
	} else {
		res = AddVoteConflict
	}

	// We can perform a blocking send to the response,
	// since it is guaranteed to be 1-buffered, if it is not nil.
	if req.Response != nil {
		req.Response <- res
	}

	// See if we need to make a request for a proposed block.
	k.checkMissingPBs(ctx, s, vrv.PrevoteProofs)

	// END OF addPrecommit SYNCHRONIZATION.

	// And if this was an accepted prevote for NextRound,
	// we might need to shift the view.
	if res == AddVoteAccepted && vID == ViewIDNextRound {
		// TODO: this needs to also check NextHeight.
		if err := k.checkPrevoteViewShift(ctx, s, vID); err != nil {
			k.log.Warn("Error while checking view shift for prevotes into next round; kernel may be in bad state", "err", err)
		}
	}
}

// addPrecommit is the kernel method to add precommits to the current state.
// The non-kernel HandlePrecommitProofs method takes a snapshot of the then-current kernel state,
// and eagerly updates that copy with the new precommits from the network.
// Then it notifies the kernel of the new precommits and the previous version.
// For any new precommits where the previous version matches our currently understood previous version,
// the new version is applied immediately.
// If any versions are out of date, we notify the caller that there was a conflict,
// and they may try again.
func (k *Kernel) addPrecommit(ctx context.Context, s *kState, req AddPrecommitRequest) {
	defer trace.StartRegion(ctx, "addPrecommit").End()

	// NOTE: keep changes to this method synchronized with addPrevote.

	vrv, vID, vStatus := s.FindView(req.H, req.R, "(*Kernel).addPrecommit")
	if vStatus != ViewFound {
		switch vStatus {
		case ViewBeforeCommitting, ViewOrphaned:
			if req.Response != nil {
				req.Response <- AddVoteOutOfDate
			}
			return
		case ViewWrongCommit:
			k.log.Warn("TODO: add new addVoteResult for viewWrongCommit")
			if req.Response != nil {
				req.Response <- AddVoteOutOfDate
			}
			return
		default:
			panic(fmt.Errorf(
				"TODO: handle unexpected view status (%s) when looking up view to add precommit",
				vStatus,
			))
		}
	}
	if vID != ViewIDCommitting && vID != ViewIDVoting && vID != ViewIDNextRound {
		panic(fmt.Errorf(
			"TODO: handle adding precommits to %s view", vID,
		))
	}

	// Assume the votes will be accepted, then invalidate that if needed.
	allAccepted := true
	anyAdded := false
	for blockHash, u := range req.PrecommitUpdates {
		if u.PrevVersion == vrv.PrecommitBlockVersions[blockHash] {
			// Then we can apply this particular change.
			vrv.PrecommitProofs[blockHash] = u.Proof
			if vrv.PrecommitBlockVersions == nil {
				vrv.PrecommitBlockVersions = make(map[string]uint32)
			}
			vrv.PrecommitBlockVersions[blockHash]++
			anyAdded = true
		} else {
			allAccepted = false
		}
	}

	// Bookkeeping.
	if anyAdded {
		vrv.VoteSummary.SetPrecommitPowers(vrv.Validators, vrv.PrecommitProofs)
		s.MarkViewUpdated(vID)

		if err := k.rStore.OverwritePrecommitProofs(
			ctx,
			req.H, req.R,
			vrv.PrecommitProofs,
		); err != nil {
			glog.HRE(k.log, req.H, req.R, err).Warn(
				"Failed to save precommits to round store; this may cause issues upon restart",
			)
		}
	}

	var res AddVoteResult
	if allAccepted {
		res = AddVoteAccepted
	} else {
		res = AddVoteConflict
	}

	// We can perform a blocking send to the response,
	// since it is guaranteed to be 1-buffered.
	if req.Response != nil {
		req.Response <- res
	}

	// See if we need to make a request for a proposed block.
	k.checkMissingPBs(ctx, s, vrv.PrecommitProofs)

	// END OF addPrevote SYNCHRONIZATION.

	if res != AddVoteAccepted {
		return
	}

	switch vID {
	case ViewIDVoting:
		if err := k.checkVotingPrecommitViewShift(ctx, s); err != nil {
			k.log.Warn("Error while checking view shift for precommit in voting round; kernel may be in bad state", "err", err)
		}
	case ViewIDNextRound:
		if err := k.checkNextRoundPrecommitViewShift(ctx, s); err != nil {
			k.log.Warn("Error while checking view shift for precommit in next round; kernel may be in bad state", "err", err)
		}
	case ViewIDCommitting:
		// No view shift possible here.
	default:
		panic(fmt.Errorf("BUG: unhandled view ID %s in addPrecommit", vID))
	}
}

// checkVotingPrecommitViewShift checks if precommit consensus
// has been reached on the voting round, and if so,
// updates the voting round accordingly.
func (k *Kernel) checkVotingPrecommitViewShift(ctx context.Context, s *kState) error {
	vrv := &s.Voting
	vs := vrv.VoteSummary
	oldHeight, oldRound := vrv.Height, vrv.Round

	maj := tmconsensus.ByzantineMajority(vs.AvailablePower)
	committingHash := vs.MostVotedPrecommitHash
	highestPow := vs.PrecommitBlockPower[committingHash]
	if highestPow < maj {
		// No block reached majority power.
		// But, we do need to check if we have 100% of votes present,
		// in which case we can advance the round anyway.
		// TODO: there are probably other subtle cases where we can advance the round.
		// For example, if we have 50% votes for one block and 45% votes for another,
		// then we know it doesn't matter where the remaining 5% land --
		// it will not influence a block to be committed.
		if vs.TotalPrecommitPower == vs.AvailablePower {
			if err := k.advanceVotingRound(ctx, s); err != nil { // Certain due to 100% precommits present.
				return err
			}

			k.log.Info(
				"Shifted voting round due to 100% of votes received without consensus",
				"height", oldHeight,
				"old_round", oldRound, "new_round", oldRound+1,
			)
		}

		// Finished here regardless of whether we reached 100% votes.
		return nil
	}

	// At this point, we know the most voted precommit hash has exceeded the majority requirement.
	if committingHash == "" {
		// Voted nil, so only update the voting round.
		if err := k.advanceVotingRound(ctx, s); err != nil { // Certain because full nil precommit.
			return err
		}

		newHeight, newRound := s.Voting.Height, s.Voting.Round

		k.log.Info(
			"Shifted voting round due to nil precommit",
			"old_height", oldHeight, "old_round", oldRound,
			"new_height", newHeight, "new_round", newRound,
		)
		return nil
	}

	// It was a precommit for a non-nil block.
	hasPB := false
	for _, pb := range vrv.ProposedBlocks {
		if string(pb.Block.Hash) == committingHash {
			hasPB = true
			break
		}
	}

	if !hasPB {
		_, ok := s.InFlightFetchPBs[committingHash]
		k.log.Warn(
			"Ready to commit block, but block is not yet available; stuck in this voting round until the block is fetched",
			"height", vrv.Height, "round", vrv.Round,
			"block_hash", glog.Hex(committingHash),
			"fetch_in_progress", ok,
		)
		return nil
	}

	var votedBlock tmconsensus.Block
	for _, pb := range s.Voting.ProposedBlocks {
		if string(pb.Block.Hash) == committingHash {
			votedBlock = pb.Block
			break
		}
	}
	if votedBlock.Hash == nil {
		// Still the zero value, which is an unsolved problem for now.
		panic(fmt.Errorf(
			"BUG: missed update; needed to fetch missing proposed block with hash %x",
			committingHash,
		))
	}

	// We are about to shift the voting view to committing,
	// but first we need to persist the currently committing block.
	if s.CommittingBlock.Height >= k.initialHeight {
		cb := tmconsensus.CommittedBlock{
			Block: s.CommittingBlock,
			Proof: votedBlock.PrevCommitProof,
		}
		if err := k.bStore.SaveBlock(ctx, cb); err != nil {
			return fmt.Errorf("failed to save newly committed block: %w", err)
		}
	}

	nextVals := slices.Clone(votedBlock.NextValidators)
	nhd := nextHeightDetails{
		Validators: nextVals,
	}
	var err error

	nhd.Round0NilPrevote, nhd.Round0NilPrecommit, err =
		k.getInitialNilProofs(votedBlock.Height+1, 0, nextVals)
	if err != nil {
		return fmt.Errorf("failed to load nil proofs on new voting round: %w", err)
	}
	nhd.Round1NilPrevote, nhd.Round1NilPrecommit, err =
		k.getInitialNilProofs(votedBlock.Height+1, 1, nextVals)
	if err != nil {
		return fmt.Errorf("failed to load nil proofs on new next round: %w", err)
	}

	// TODO: we can avoid calculating these hashes sometimes,
	// if we are smart about inspecting the previous view.
	pubKeys := tmconsensus.ValidatorsToPubKeys(nextVals)
	bPubKeyHash, err := k.hashScheme.PubKeys(pubKeys)
	if err != nil {
		return fmt.Errorf("failed to build public key hash for new voting height: %w", err)
	}
	nhd.ValidatorPubKeyHash = string(bPubKeyHash)

	pows := tmconsensus.ValidatorsToVotePowers(nextVals)
	bPowHash, err := k.hashScheme.VotePowers(pows)
	if err != nil {
		return fmt.Errorf("failed to build vote power hash for new voting height: %w", err)
	}
	nhd.ValidatorVotePowerHash = string(bPowHash)

	nhd.VotedBlock = votedBlock

	s.ShiftVotingToCommitting(nhd)
	if err := k.updateObservers(ctx, s); err != nil {
		return err
	}

	k.log.Info(
		"Committed block",
		"height", s.CommittingBlock.Height-1, "hash", glog.Hex(s.CommittingBlock.PrevBlockHash),
		"next_committing_height", s.CommittingBlock.Height, "next_committing_hash", glog.Hex(s.CommittingBlock.Hash),
	)

	return nil
}

// checkNextRoundPrecommitViewShift checks if precommit consensus
// has surpassed the minority threshold on a single block in the next round.
// If it has, voting advances to the next round.
func (k *Kernel) checkNextRoundPrecommitViewShift(ctx context.Context, s *kState) error {
	vrv := &s.NextRound

	vs := vrv.VoteSummary
	min := tmconsensus.ByzantineMinority(vs.AvailablePower)
	if vs.TotalPrecommitPower < min {
		// Nothing to do.
		return nil
	}

	oldHeight, oldRound := s.Voting.Height, s.Voting.Round

	// Otherwise at least a minority of the network is precommitting on the target round,
	// so we need to jump voting to that round.
	// This is a jump, not advance, because we actually don't have
	// sufficient information to treat the current round as a nil commit.
	if err := k.jumpVotingRound(ctx, s); err != nil {
		return err
	}

	newHeight, newRound := s.Voting.Height, s.Voting.Round

	k.log.Info(
		"Shifting voting round due to minority precommit",
		"old_height", oldHeight, "old_round", oldRound,
		"new_height", newHeight, "new_round", newRound,
	)

	maj := tmconsensus.ByzantineMajority(vs.AvailablePower)
	maxPow := vs.PrecommitBlockPower[vs.MostVotedPrecommitHash]
	if maxPow >= maj {
		// Need a test in place before handling the ready to commit case.
		panic("TODO: handle a majority precommit for NextRound")
	}

	if maxPow >= min {
		// Make a PB fetch request if we don't have the proposed block
		// that just crossed the threshold.
		k.checkMissingPBs(ctx, s, s.Voting.PrecommitProofs)
	}

	return nil
}

// checkPrevoteViewShift inspects the Next Round to see if the total prevotes
// have crossed the minority threshold.
// If they have, voting advances to that round.
func (k *Kernel) checkPrevoteViewShift(ctx context.Context, s *kState, vID ViewID) error {
	var vrv *tmconsensus.VersionedRoundView
	switch vID {
	case ViewIDNextRound:
		vrv = &s.NextRound
	default:
		panic(fmt.Errorf("BUG: unhandled view ID %s in checkPrecommitViewShift", vID))
	}

	vs := vrv.VoteSummary
	min := tmconsensus.ByzantineMinority(vs.AvailablePower)
	if vs.TotalPrevotePower < min {
		// Nothing to do.
		return nil
	}

	oldHeight, oldRound := s.Voting.Height, s.Voting.Round

	// Otherwise a minority of the network is prevoting on the target round,
	// so we need to jump voting to that round.
	// This is a jump, not advance, because we actually don't have
	// sufficient information to treat the current round as a nil commit.
	if err := k.jumpVotingRound(ctx, s); err != nil {
		return err
	}

	newHeight, newRound := s.Voting.Height, s.Voting.Round

	k.log.Info(
		"Shifted voting round due to minority prevote",
		"old_height", oldHeight, "old_round", oldRound,
		"new_height", newHeight, "new_round", newRound,
	)

	// If the vote was for a single non-nil block, we may need to fetch proposed blocks.
	if vs.PrevoteBlockPower[vs.MostVotedPrevoteHash] >= min {
		k.checkMissingPBs(ctx, s, s.Voting.PrevoteProofs)
	}

	return nil
}

// checkMissingPBs creates a fetch proposed block request,
// if there is more than minority voting power present for a singular block
// and if we do not have that proposed block yet
// and if we do not have an outstanding request for that block.
// This is only applicable to the Voting view.
func (k *Kernel) checkMissingPBs(ctx context.Context, s *kState, proofs map[string]gcrypto.CommonMessageSignatureProof) {
	havePBHashes := make(map[string]struct{}, len(s.Voting.ProposedBlocks))
	for _, pb := range s.Voting.ProposedBlocks {
		havePBHashes[string(pb.Block.Hash)] = struct{}{}
	}

	// Any block hash -- except nil --
	// that we have a proof for, but we don't have a proposed block for.
	missingPBs := make([]string, 0, len(proofs)-1)
	for blockHash := range proofs {
		if blockHash == "" {
			continue
		}

		if _, ok := havePBHashes[blockHash]; !ok {
			missingPBs = append(missingPBs, blockHash)
		}
	}

	if len(missingPBs) == 0 {
		// Nothing left to do.
		return
	}

	// Check if we have outstanding fetch requests
	// before bothering with the vote distribution.

	skippedAny := false
	for i, missingPB := range missingPBs {
		if _, ok := s.InFlightFetchPBs[missingPB]; !ok {
			continue
		}

		// We do have an in-flight request for this proposed block.
		// Clear the value so we know not to try fetching it.
		missingPBs[i] = ""
		skippedAny = true
	}

	if skippedAny {
		// Bulk delete any cleared elements, which should be slightly more efficient than deleting individually.
		missingPBs = slices.DeleteFunc(missingPBs, func(hash string) bool {
			return hash == ""
		})

		if len(missingPBs) == 0 {
			// If we cleared the whole slice, then there is no need for further work.
			return
		}
	}

	// There is at least one missing proposed block.
	// Don't try to fetch the block until we have crossed the byzantine minority threshold.
	// This way, if every Byzantine validator were to vote for an individual, or even the same,
	// nonexistent proposed block

	// TODO: figure out how to use the VoteSummary with the proofs argument properly.
	dist := newVoteDistribution(proofs, s.Voting.Validators)

	min := tmconsensus.ByzantineMinority(dist.AvailableVotePower)

	for _, missingHash := range missingPBs {
		if dist.BlockVotePower[missingHash] < min {
			continue
		}

		// This hash has met or exceeded the minimum threshold,
		// so we need to make a fetch request.

		fetchCtx, cancel := context.WithCancel(ctx)
		_ = cancel // Suppresses the vet warning about cancel not being used on all return paths.

		select {
		case <-ctx.Done():
			// The caller should log whatever it needs for a context cancellation.
			return
		case k.pbf.FetchRequests <- tmelink.ProposedBlockFetchRequest{
			Ctx:       fetchCtx,
			Height:    s.Voting.Height,
			BlockHash: missingHash,
		}:
			// Okay.
			s.InFlightFetchPBs[missingHash] = cancel
		default:
			// The FetchRequests channel ought to be sufficiently buffered to avoid this.
			// But even if we do hit this log line once,
			// the fetch attempt will repeat for every subsequent vote received thereafter.
			k.log.Debug(
				"Blocked sending fetch request; kernel may deadlock if this block reaches consensus",
				"height", s.Voting.Height, "round", s.Voting.Round,
				"missing_hash", glog.Hex(missingHash),
			)
		}
	}
}

// advanceVotingRound is called when the kernel needs to increase the voting round by one,
// and when we have sufficient information for the voting round to treat it as a nil commit.
func (k *Kernel) advanceVotingRound(ctx context.Context, s *kState) error {
	h := s.NextRound.Height
	r := s.NextRound.Round + 1
	nilPrevote, nilPrecommit, err := k.getInitialNilProofs(h, r, s.NextRound.Validators)
	if err != nil {
		return fmt.Errorf(
			"failed to get initial nil proofs for h=%d/r=%d when advancing voting round",
			h, r,
		)
	}

	s.AdvanceVotingRound(nilPrevote, nilPrecommit)
	if err := k.updateObservers(ctx, s); err != nil {
		return err
	}
	return nil
}

// jumpVotingRound is called when the kernel needs to increase the voting round by one,
// but this is due to timing without receiving a majority nil vote on the round.
// Compared to [*Kernel.advanceVotingRound], this sends more information to the state machine
// indicating the kernel's intent to skip the round.
func (k *Kernel) jumpVotingRound(ctx context.Context, s *kState) error {
	h := s.NextRound.Height
	r := s.NextRound.Round + 1
	nilPrevote, nilPrecommit, err := k.getInitialNilProofs(h, r, s.NextRound.Validators)
	if err != nil {
		return fmt.Errorf(
			"failed to get initial nil proofs for h=%d/r=%d when jumping voting round",
			h, r,
		)
	}

	s.JumpVotingRound(nilPrevote, nilPrecommit)
	if err := k.updateObservers(ctx, s); err != nil {
		return err
	}

	return nil
}

func (k *Kernel) getInitialNilProofs(h uint64, r uint32, vals []tmconsensus.Validator) (
	prevote, precommit gcrypto.CommonMessageSignatureProof,
	err error,
) {
	nilVT := tmconsensus.VoteTarget{
		Height: h,
		Round:  r,
	}
	nilPrevoteContent, err := tmconsensus.PrevoteSignBytes(nilVT, k.sigScheme)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get initial nil prevote sign bytes: %w", err)
	}

	pubKeys := tmconsensus.ValidatorsToPubKeys(vals)
	bPubKeyHash, err := k.hashScheme.PubKeys(pubKeys)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to build public key hash: %w", err)
	}
	pubKeyHash := string(bPubKeyHash)

	prevoteNilProof, err := k.cmspScheme.New(nilPrevoteContent, pubKeys, pubKeyHash)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get initial nil prevote proof: %w", err)
	}

	nilPrecommitContent, err := tmconsensus.PrecommitSignBytes(nilVT, k.sigScheme)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get initial nil precommit sign bytes: %w", err)
	}

	precommitNilProof, err := k.cmspScheme.New(nilPrecommitContent, pubKeys, pubKeyHash)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get initial nil precommit proof: %w", err)
	}

	return prevoteNilProof, precommitNilProof, nil
}

// sendSnapshotResponse sends a response to a snapshot request.
func (k *Kernel) sendSnapshotResponse(ctx context.Context, s *kState, req SnapshotRequest) {
	defer trace.StartRegion(ctx, "sendSnapshotResponse").End()
	defer close(req.Ready)

	if req.Snapshot.Voting != nil {
		k.copySnapshotView(s.Voting, req.Snapshot.Voting, req.Fields)
	}
	if req.Snapshot.Committing != nil {
		k.copySnapshotView(s.Committing, req.Snapshot.Committing, req.Fields)
	}
}

// copySnapshotView copies an individual view from kernel state to a snapshot request.
func (k *Kernel) copySnapshotView(src tmconsensus.VersionedRoundView, dst *tmconsensus.VersionedRoundView, fields RVFieldFlags) {
	dst.Height = src.Height
	dst.Round = src.Round
	dst.Version = src.Version

	// Reuse any existing allocated space in the input slices.
	if (fields & RVValidators) > 0 {
		dst.Validators = append(dst.Validators[:0], src.Validators...)
		dst.ValidatorPubKeyHash = src.ValidatorPubKeyHash
		dst.ValidatorVotePowerHash = src.ValidatorVotePowerHash
	} else {
		dst.Validators = dst.Validators[:0]
		dst.ValidatorPubKeyHash = ""
		dst.ValidatorVotePowerHash = ""
	}

	if (fields & RVProposedBlocks) > 0 {
		dst.ProposedBlocks = append(dst.ProposedBlocks[:0], src.ProposedBlocks...)
	} else {
		dst.ProposedBlocks = dst.ProposedBlocks[:0]
	}

	// Clear the prevote maps regardless of whether we are populating them.
	clear(dst.PrevoteProofs)
	clear(dst.PrevoteBlockVersions)
	if (fields & RVPrevotes) > 0 {
		if dst.PrevoteProofs == nil {
			dst.PrevoteProofs = make(map[string]gcrypto.CommonMessageSignatureProof, len(src.PrevoteProofs))
		}
		for blockHash, proof := range src.PrevoteProofs {
			dst.PrevoteProofs[blockHash] = proof.Clone()
		}

		if dst.PrevoteBlockVersions == nil && len(src.PrevoteBlockVersions) > 0 {
			dst.PrevoteBlockVersions = make(map[string]uint32, len(src.PrevoteBlockVersions))
		}
		for blockHash, version := range src.PrevoteBlockVersions {
			dst.PrevoteBlockVersions[blockHash] = version
		}
	}

	// Same for precommits.
	clear(dst.PrecommitProofs)
	clear(dst.PrecommitBlockVersions)
	if (fields & RVPrecommits) > 0 {
		if dst.PrecommitProofs == nil {
			dst.PrecommitProofs = make(map[string]gcrypto.CommonMessageSignatureProof, len(src.PrecommitProofs))
		}
		for blockHash, proof := range src.PrecommitProofs {
			dst.PrecommitProofs[blockHash] = proof.Clone()
		}

		if dst.PrecommitBlockVersions == nil && len(src.PrecommitBlockVersions) > 0 {
			dst.PrecommitBlockVersions = make(map[string]uint32, len(src.PrecommitBlockVersions))
		}
		for blockHash, version := range src.PrecommitBlockVersions {
			dst.PrecommitBlockVersions[blockHash] = version
		}
	}

	dst.VoteSummary.Reset()
	if (fields & RVVoteSummary) > 0 {
		dst.VoteSummary.AvailablePower = src.VoteSummary.AvailablePower
		dst.VoteSummary.TotalPrevotePower = src.VoteSummary.TotalPrevotePower
		dst.VoteSummary.TotalPrecommitPower = src.VoteSummary.TotalPrecommitPower
		if dst.VoteSummary.PrevoteBlockPower == nil {
			dst.VoteSummary.PrevoteBlockPower = maps.Clone(src.VoteSummary.PrevoteBlockPower)
		} else {
			for k, v := range src.VoteSummary.PrevoteBlockPower {
				dst.VoteSummary.PrevoteBlockPower[k] = v
			}
		}

		if dst.VoteSummary.PrecommitBlockPower == nil {
			dst.VoteSummary.PrecommitBlockPower = maps.Clone(src.VoteSummary.PrecommitBlockPower)
		} else {
			for k, v := range src.VoteSummary.PrecommitBlockPower {
				dst.VoteSummary.PrecommitBlockPower[k] = v
			}
		}

		dst.VoteSummary.MostVotedPrevoteHash = src.VoteSummary.MostVotedPrevoteHash
		dst.VoteSummary.MostVotedPrecommitHash = src.VoteSummary.MostVotedPrecommitHash
	}
}

// sendViewLookupResponse sends a ViewLookupResponse to the given ViewLookupRequest.
func (k *Kernel) sendViewLookupResponse(ctx context.Context, s *kState, req ViewLookupRequest) {
	defer trace.StartRegion(ctx, "sendViewLookupResponse").End()

	if req.Reason == "" {
		panic(errors.New("BUG: ViewLookupRequest.Reason must not be empty"))
	}

	var resp ViewLookupResponse

	srcVRV, vID, vStatus := s.FindView(req.H, req.R, req.Reason)
	if srcVRV != nil {
		k.copySnapshotView(*srcVRV, req.VRV, req.Fields)
	}
	resp.ID = vID
	resp.Status = vStatus

	// The response channel is guaranteed to be buffered,
	// so this send does not need to be wrapped in a select.
	req.Resp <- resp
}

func (k *Kernel) sendPBCheckResponse(ctx context.Context, s *kState, req PBCheckRequest) {
	defer trace.StartRegion(ctx, "sendPBCheckResponse").End()

	var resp PBCheckResponse

	pbHeight := req.PB.Block.Height
	pbRound := req.PB.Round
	votingHeight := s.Voting.Height
	votingRound := s.Voting.Round
	committingHeight := s.Committing.Height
	committingRound := s.Committing.Round

	// Sorted earliest to latest heights,
	// then interior round checks also sorted earliest to latest.
	if pbHeight < committingHeight {
		resp.Status = PBCheckRoundTooOld
	} else if pbHeight == committingHeight {
		if pbRound < committingRound {
			resp.Status = PBCheckRoundTooOld
		} else if pbRound == committingRound {
			k.setPBCheckStatus(req, &resp, s.Committing)
		} else {
			panic(fmt.Errorf(
				"TODO: handle proposed block with round (%d) beyond committing round (%d)",
				pbRound, committingRound,
			))
		}
	} else if pbHeight == votingHeight {
		if pbRound < votingRound {
			resp.Status = PBCheckRoundTooOld
		} else if pbRound == votingRound {
			k.setPBCheckStatus(req, &resp, s.Voting)
		} else if pbRound == votingRound+1 {
			k.setPBCheckStatus(req, &resp, s.NextRound)
		} else {
			panic(fmt.Errorf(
				"TODO: handle proposed block with round (%d) beyond voting round (%d)",
				pbRound, votingRound,
			))
		}
	} else if pbHeight == votingHeight+1 {
		// Special case of the proposed block being for the next height.
		resp.Status = PBCheckNextHeight

		rv := s.Voting.RoundView.Clone()
		resp.VotingRoundView = &rv
	} else {
		resp.Status = PBCheckRoundTooFarInFuture
	}

	if resp.Status == PBCheckInvalid {
		// Wasn't set.
		panic(fmt.Errorf(
			"BUG: cannot determine PBStatus; pb h=%d/r=%d, voting h=%d/r=%d, committing h=%d/r=%d",
			pbHeight, pbRound, votingHeight, votingRound, committingHeight, committingRound,
		))
	}

	// Guaranteed to be 1-buffered, no need to select.
	req.Resp <- resp
}

func (k *Kernel) setPBCheckStatus(
	req PBCheckRequest,
	resp *PBCheckResponse,
	vrv tmconsensus.VersionedRoundView,
) {
	alreadyHaveSignature := slices.ContainsFunc(vrv.ProposedBlocks, func(havePB tmconsensus.ProposedBlock) bool {
		return bytes.Equal(havePB.Signature, req.PB.Signature)
	})

	if alreadyHaveSignature {
		resp.Status = PBCheckAlreadyHaveSignature
	} else {
		// The block might be acceptable, but we need to confirm that there is a matching public key first.
		// We are currently assuming that it is cheaper for the kernel to block on seeking through the validators
		// than it is to copy over the entire validator block and hand it off to the mirror's calling goroutine.
		var proposerPubKey gcrypto.PubKey
		for _, val := range vrv.Validators {
			if req.PB.ProposerPubKey.Equal(val.PubKey) {
				proposerPubKey = val.PubKey
				break
			}
		}

		if proposerPubKey == nil {
			resp.Status = PBCheckSignerUnrecognized
		} else {
			resp.Status = PBCheckAcceptable
			resp.ProposerPubKey = proposerPubKey
		}
	}
}

func (k *Kernel) handleStateMachineRoundEntrance(ctx context.Context, s *kState, re tmeil.StateMachineRoundEntrance) {
	defer trace.StartRegion(ctx, "handleStateMachineRoundEntrance").End()

	// We have received an updated height and round, and new action channels.
	s.StateMachineViewManager.Reset(re)

	// And now we need to respond with the matching view.
	vrv, _, status := s.FindView(re.H, re.R, "(*Kernel).handleStateMachineRoundEntrance")
	if vrv == nil {
		// There is one acceptable condition here -- it was before the committing round.
		if status == ViewBeforeCommitting {
			// Then we have to load it from the block store.
			cb, err := k.bStore.LoadBlock(ctx, re.H)
			if err != nil {
				panic(fmt.Errorf(
					"failed to load block at height %d from block store for state machine: %w",
					re.H, err,
				))
			}

			// Send on 1-buffered channel does not require a select.
			re.Response <- tmeil.RoundEntranceResponse{
				CB: cb,
			}
			return
		}

		panic(fmt.Errorf(
			"TODO: handle view not found (status=%s) when responding to state machine round update for height/round %d/%d",
			status, re.H, re.R,
		))
	}

	r := tmeil.RoundEntranceResponse{
		VRV: vrv.Clone(),
	}

	// Response channel is 1-buffered so it is safe to send this without a select.
	re.Response <- r
	s.StateMachineViewManager.MarkFirstSentVersion(r.VRV.Version)
}

func (k *Kernel) handleStateMachineAction(ctx context.Context, s *kState, act tmeil.StateMachineRoundAction) {
	defer trace.StartRegion(ctx, "handleStateMachineAction").End()

	hasPB := len(act.PB.Block.Hash) > 0
	hasPrevote := len(act.Prevote.Sig) > 0
	hasPrecommit := len(act.Precommit.Sig) > 0

	if !(hasPB || hasPrevote || hasPrecommit) {
		panic(errors.New("BUG: no state machine action present"))
	}

	if hasPB {
		if hasPrevote || hasPrecommit {
			panic(fmt.Errorf(
				"BUG: multiple state machine actions present when exactly one required: pb=true prevote=%t precommit=%t",
				hasPrevote, hasPrecommit,
			))
		}

		// addPB works directly on the proposed block without any feedback to the caller,
		// so we are fine to call that directly here.
		k.addPB(ctx, s, act.PB)
		return
	}

	// For votes, we have to duplicate some of the logic that happens in the mirror.
	// Specifically, we have to get the current state so we can produce an accurate VoteUpdate.

	h, r := s.StateMachineViewManager.H(), s.StateMachineViewManager.R()
	vrv, vID, _ := s.FindView(h, r, "(*Kernel).handleStateMachineAction")
	if vrv == nil || (vID != ViewIDVoting && vID != ViewIDCommitting) {
		k.log.Info(
			"Dropping state machine vote due to not matching voting or committing view",
			"req_h", h,
			"req_r", r,
			"voting_h", s.Voting.Height,
			"voting_r", s.Voting.Round,
			"committing_h", s.Committing.Height,
			"committing_r", s.Committing.Round,
			"view_id", vID,
		)
		return
	}

	if hasPrevote {
		if hasPrecommit {
			panic(errors.New(
				"BUG: multiple state machine actions present when exactly one required: pb=false prevote=true precommit=true",
			))
		}

		hash := act.Prevote.TargetHash
		var updatedVote gcrypto.CommonMessageSignatureProof
		existingVote := vrv.PrevoteProofs[hash]
		if existingVote == nil {
			// First vote we have for this hash.
			var err error
			updatedVote, err = k.cmspScheme.New(
				act.Prevote.SignContent,
				tmconsensus.ValidatorsToPubKeys(s.Voting.Validators),
				s.Voting.ValidatorPubKeyHash,
			)
			if err != nil {
				k.log.Error(
					"Failed to build empty prevote proof for prevote from state machine",
					"prevote_h", h,
					"prevote_r", r,
					"err", err,
				)
				return
			}
		} else {
			// There is an existing vote and we have to merge into it.
			// But we will clone it first in case something goes wrong.
			updatedVote = existingVote.Clone()
		}
		if err := updatedVote.AddSignature(act.Prevote.Sig, s.StateMachineViewManager.PubKey()); err != nil {
			k.log.Error(
				"Failed to add prevote signature from state machine",
				"prevote_h", h,
				"prevote_r", r,
				"err", err,
			)
			return
		}

		req := AddPrevoteRequest{
			H: h,
			R: r,

			PrevoteUpdates: map[string]VoteUpdate{
				act.Prevote.TargetHash: {
					Proof:       updatedVote,
					PrevVersion: vrv.PrevoteBlockVersions[hash],
				},
			},

			// No response field because we are going to ignore it.
			// The handler skips sending to a nil channel.
		}
		k.addPrevote(ctx, s, req)
		return
	}

	// At this point, from the early returns, only hasPrecommit must be true.
	hash := act.Precommit.TargetHash
	var updatedVote gcrypto.CommonMessageSignatureProof
	existingVote := vrv.PrecommitProofs[hash]
	if existingVote == nil {
		var err error
		updatedVote, err = k.cmspScheme.New(
			act.Precommit.SignContent,
			tmconsensus.ValidatorsToPubKeys(s.Voting.Validators),
			s.Voting.ValidatorPubKeyHash,
		)
		if err != nil {
			k.log.Error(
				"Failed to build empty precommit proof for precommit from state machine",
				"precommit_h", h,
				"precommit_r", r,
				"err", err,
			)
			return
		}
	} else {
		updatedVote = existingVote.Clone()
	}
	if err := updatedVote.AddSignature(act.Precommit.Sig, s.StateMachineViewManager.PubKey()); err != nil {
		k.log.Error(
			"Failed to add precommit signature from state machine",
			"precommit_h", h,
			"precommit_r", r,
			"err", err,
		)
		return
	}

	req := AddPrecommitRequest{
		H: h,
		R: r,

		PrecommitUpdates: map[string]VoteUpdate{
			act.Precommit.TargetHash: {
				Proof:       updatedVote,
				PrevVersion: vrv.PrecommitBlockVersions[hash],
			},
		},

		// No response field because we are going to ignore it.
		// The handler skips sending to a nil channel.
	}
	k.addPrecommit(ctx, s, req)
}

// loadInitialView loads the committing or voting RoundView
// at the given height and round from the RoundStore, inside NewKernel.
func (k *Kernel) loadInitialView(
	ctx context.Context,
	h uint64, r uint32,
	vals []tmconsensus.Validator,
) (tmconsensus.RoundView, error) {
	var rv tmconsensus.RoundView
	pbs, prevotes, precommits, err := k.rStore.LoadRoundState(ctx, h, r)
	if err != nil && !errors.Is(err, tmconsensus.RoundUnknownError{WantHeight: h, WantRound: r}) {
		return rv, err
	}

	rv = tmconsensus.RoundView{
		Height: h,
		Round:  r,

		Validators: vals,

		ProposedBlocks: pbs,
	}

	// Is there ever a case where we don't have the validator hashes in the store?
	// This should be safe anyway, and it only happens once at startup.
	valPubKeys := tmconsensus.ValidatorsToPubKeys(rv.Validators)
	rv.ValidatorPubKeyHash, err = k.vStore.SavePubKeys(ctx, valPubKeys)
	if err != nil && !errors.As(err, new(tmstore.PubKeysAlreadyExistError)) {
		return tmconsensus.RoundView{}, fmt.Errorf(
			"cannot initialize view: failed to save or check initial view validator pubkey hash: %w",
			err,
		)
	}

	rv.ValidatorVotePowerHash, err = k.vStore.SaveVotePowers(
		ctx,
		tmconsensus.ValidatorsToVotePowers(rv.Validators),
	)
	if err != nil && !errors.As(err, new(tmstore.VotePowersAlreadyExistError)) {
		return tmconsensus.RoundView{}, fmt.Errorf(
			"cannot initialize view: failed to save or check initial view vote power hash: %w",
			err,
		)
	}

	// Now that we have the validator public keys,
	// we can ensure that the view has the nil proof set.
	nilVT := tmconsensus.VoteTarget{Height: h, Round: r}
	if prevotes == nil {
		prevotes = make(map[string]gcrypto.CommonMessageSignatureProof)
	}
	if prevotes[""] == nil {
		content, err := tmconsensus.PrevoteSignBytes(nilVT, k.sigScheme)
		if err != nil {
			return tmconsensus.RoundView{}, fmt.Errorf("failed to get initial nil prevote sign bytes: %w", err)
		}
		prevotes[""], err = k.cmspScheme.New(content, valPubKeys, rv.ValidatorPubKeyHash)
		if err != nil {
			return tmconsensus.RoundView{}, fmt.Errorf("failed to get initial nil prevote proof: %w", err)
		}
	}
	rv.PrevoteProofs = prevotes

	if precommits == nil {
		precommits = make(map[string]gcrypto.CommonMessageSignatureProof)
	}
	if precommits[""] == nil {
		content, err := tmconsensus.PrecommitSignBytes(nilVT, k.sigScheme)
		if err != nil {
			return tmconsensus.RoundView{}, fmt.Errorf("failed to get initial nil precommit sign bytes: %w", err)
		}
		precommits[""], err = k.cmspScheme.New(content, valPubKeys, rv.ValidatorPubKeyHash)
		if err != nil {
			return tmconsensus.RoundView{}, fmt.Errorf("failed to get initial nil precommit proof: %w", err)
		}
	}
	rv.PrecommitProofs = precommits

	rv.VoteSummary = tmconsensus.NewVoteSummary()
	rv.VoteSummary.SetAvailablePower(rv.Validators)

	return rv, nil
}

func (k *Kernel) loadInitialCommittingView(ctx context.Context, s *kState) error {
	var vals []tmconsensus.Validator

	h := s.Committing.Height
	r := s.Committing.Round

	if h == k.initialHeight || h == k.initialHeight+1 {
		vals = slices.Clone(k.initialVals)
	} else {
		panic("TODO: load committing validators beyond initial height")
	}

	rv, err := k.loadInitialView(ctx, h, r, vals)
	if err != nil {
		return err
	}
	s.Committing.RoundView = rv
	s.Committing.PrevoteVersion = 1
	s.Committing.PrecommitVersion = 1
	s.MarkCommittingViewUpdated()

	// Now we need to set s.CommittingBlock.
	// We know this block is in the committing view,
	// so it must have >2/3 voting power available.
	// That means we can simply look for the single block with the highest voting power.
	if len(rv.PrecommitProofs) == 0 {
		panic(fmt.Errorf(
			"BUG: loading commit view from disk without any precommits, height=%d/round=%d",
			h, r,
		))
	}

	var maxPower uint64
	var committingHash string

	dist := newVoteDistribution(rv.PrecommitProofs, rv.Validators)
	for blockHash, pow := range dist.BlockVotePower {
		if pow > maxPower {
			maxPower = pow
			committingHash = blockHash
		}
	}

	// Now find which proposed block matches the hash.
	for _, pb := range rv.ProposedBlocks {
		if string(pb.Block.Hash) == committingHash {
			s.CommittingBlock = pb.Block
			break
		}
	}

	if string(s.CommittingBlock.Hash) == "" {
		panic(fmt.Errorf(
			"BUG: failed to determine committing block at height=%d/round=%d, expected hash %x",
			h, r, committingHash,
		))
	}

	return nil
}

// loadInitialVotingView loads any already saved proposed blocks and votes
// for the voting view at the height and round already set on the voting view.
//
// It also prepopulates the NextRound view.
func (k *Kernel) loadInitialVotingView(ctx context.Context, s *kState) error {
	var vals []tmconsensus.Validator

	h := s.Voting.Height
	r := s.Voting.Round

	if h == k.initialHeight || h == k.initialHeight+1 {
		vals = slices.Clone(k.initialVals)
	} else {
		// During initialization, we have set the committing block on the kState value.
		// TODO: when the validator slice is no longer part of the Block,
		// we will have to do a store lookup here.
		vals = slices.Clone(s.CommittingBlock.Validators)
	}

	if len(vals) == 0 {
		panic(fmt.Errorf(
			"BUG: no validators available when loading initial Voting View at height=%d/round=%d",
			h, r,
		))
	}

	rv, err := k.loadInitialView(ctx, h, r, vals)
	if err != nil {
		return err
	}
	s.Voting.RoundView = rv
	s.Voting.PrevoteVersion = 1
	s.Voting.PrecommitVersion = 1
	s.MarkVotingViewUpdated()

	// The voting view may be cleared independently of the next round view,
	// so take another clone of the validators slice to be defensive.
	nrrv, err := k.loadInitialView(ctx, h, r+1, slices.Clone(vals))
	if err != nil {
		return err
	}
	s.NextRound.RoundView = nrrv
	s.NextRound.PrevoteVersion = 1
	s.NextRound.PrecommitVersion = 1
	s.MarkNextRoundViewUpdated()

	return nil
}

// updateObservers records the new voting and committing heights and rounds,
// to the Mirror store and to the metrics collector.
func (k *Kernel) updateObservers(ctx context.Context, s *kState) error {
	if err := k.store.SetNetworkHeightRound(
		ctx,
		s.Voting.Height, s.Voting.Round,
		s.Committing.Height, s.Committing.Round,
	); err != nil {
		return fmt.Errorf("failed to update mirror store with new heights and rounds: %w", err)
	}

	// This should only be nil in test.
	if k.mc == nil {
		return nil
	}

	k.mc.UpdateMirror(tmemetrics.MirrorMetrics{
		VH: s.Voting.Height, VR: s.Voting.Round,
		CH: s.Committing.Height, CR: s.Committing.Round,
	})

	return nil
}
