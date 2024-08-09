package tmengine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/rollchains/gordian/gcrypto"
	"github.com/rollchains/gordian/gwatchdog"
	"github.com/rollchains/gordian/internal/gchan"
	"github.com/rollchains/gordian/internal/glog"
	"github.com/rollchains/gordian/tm/tmconsensus"
	"github.com/rollchains/gordian/tm/tmdriver"
	"github.com/rollchains/gordian/tm/tmengine/internal/tmeil"
	"github.com/rollchains/gordian/tm/tmengine/internal/tmemetrics"
	"github.com/rollchains/gordian/tm/tmengine/internal/tmmirror"
	"github.com/rollchains/gordian/tm/tmengine/internal/tmstate"
	"github.com/rollchains/gordian/tm/tmengine/tmelink"
	"github.com/rollchains/gordian/tm/tmgossip"
	"github.com/rollchains/gordian/tm/tmstore"
)

// Engine is the entrypoint to a working consensus engine.
type Engine struct {
	log *slog.Logger

	genesis *tmconsensus.ExternalGenesis

	gs tmgossip.Strategy

	hashScheme tmconsensus.HashScheme
	sigScheme  tmconsensus.SignatureScheme
	cmspScheme gcrypto.CommonMessageSignatureProofScheme

	// Hold the mirror config separate from the mirror,
	// so a user can run a standalone mirror by borrowing an Engine instance.
	mCfg tmmirror.MirrorConfig
	m    *tmmirror.Mirror

	sm *tmstate.StateMachine

	initChainCh chan<- tmdriver.InitChainRequest
	metricsCh   chan<- Metrics

	watchdog *gwatchdog.Watchdog
}

func New(ctx context.Context, log *slog.Logger, opts ...Opt) (*Engine, error) {
	// These channels must be unbuffered so that all communication is synchronized.
	smViewCh := make(chan tmeil.StateMachineRoundView)
	gsCh := make(chan tmelink.NetworkViewUpdate)

	e := &Engine{
		log: log,

		mCfg: tmmirror.MirrorConfig{
			GossipStrategyOut:        gsCh,
			StateMachineRoundViewOut: smViewCh,
		},
	}

	smCfg := tmstate.StateMachineConfig{
		RoundViewInCh: smViewCh,
	}

	var err error
	for _, opt := range opts {
		err = errors.Join(opt(e, &smCfg))
	}
	if err != nil {
		return nil, err
	}

	if err := e.validateSettings(smCfg); err != nil {
		return nil, err
	}

	if e.metricsCh != nil {
		mc := tmemetrics.NewCollector(ctx, 4, e.metricsCh)
		smCfg.MetricsCollector = mc
		e.mCfg.MetricsCollector = mc
	}

	// The assigned genesis may be a zero value if the chain was already initialized,
	// but the state machine should be able to handle that.
	smCfg.Genesis, err = e.maybeInitializeChain(ctx, smCfg.FinalizationStore)
	if err != nil {
		return nil, err
	}

	// We will never use the init chain channel again,
	// so clear it out to make it GC-able.
	e.initChainCh = nil

	e.mCfg.InitialHeight = e.genesis.InitialHeight

	// Prefer to set the mirror config's validators
	// to match the state machine's validators that resulted from the InitChain call.
	// But if that is nil (probably because we didn't need to InitChain),
	// then fall back to the configured genesis validators.
	e.mCfg.InitialValidators = smCfg.Genesis.Validators
	if e.mCfg.InitialValidators == nil {
		e.mCfg.InitialValidators = e.genesis.GenesisValidators
	}

	// Set up a cancelable context in case any of the subsystems fail to create.
	// We cancel the context in any error path to stop the subsystems,
	// although we don't wait for them at that point.
	// That does mean, at this point, error paths must close e.done
	// and return e, so that backgrounded goroutines can also be Waited if desired.
	ctx, cancel := context.WithCancel(ctx)
	_ = cancel // Suppress unused cancel warning.

	stateMachineRoundEntrances := make(chan tmeil.StateMachineRoundEntrance)
	e.mCfg.StateMachineRoundEntranceIn = stateMachineRoundEntrances
	smCfg.RoundEntranceOutCh = stateMachineRoundEntrances

	e.m, err = tmmirror.NewMirror(ctx, log.With("e_sys", "mirror"), e.mCfg)
	if err != nil {
		cancel()
		return e, fmt.Errorf("failed to instantiate mirror: %w", err)
	}

	e.sm, err = tmstate.NewStateMachine(ctx, log.With("e_sys", "statemachine"), smCfg)
	if err != nil {
		cancel()
		return e, fmt.Errorf("failed to instantiate state machine: %w", err)
	}

	e.gs.Start(gsCh)

	return e, nil
}

func (e *Engine) Wait() {
	// For the subsystems, these will typically be non-nil,
	// but they may be nil if there was a failure during NewEngine.

	if e.m != nil {
		e.m.Wait()
	}
	if e.sm != nil {
		e.sm.Wait()
	}
	if e.gs != nil {
		e.gs.Wait()
	}
	if e.mCfg.MetricsCollector != nil {
		e.mCfg.MetricsCollector.Wait()
	}
}

func (e *Engine) validateSettings(smc tmstate.StateMachineConfig) error {
	var err error

	if e.genesis == nil {
		err = errors.Join(err, errors.New("no genesis set (use tmengine.WithGenesis)"))
	}

	if e.hashScheme == nil {
		err = errors.Join(err, errors.New("no hash scheme set (use tmengine.WithHashScheme)"))
	}
	if e.sigScheme == nil {
		err = errors.Join(err, errors.New("no signature scheme set (use tmengine.WithSignatureScheme)"))
	}
	if e.cmspScheme == nil {
		err = errors.Join(err, errors.New("no common message signature proof scheme set (use tmengine.WithCommonMessageSignatureProofScheme)"))
	}

	if e.gs == nil {
		err = errors.Join(err, errors.New("no gossip strategy set (use tmengine.WithGossipStrategy[3a])"))
	}

	if smc.ActionStore == nil && smc.Signer != nil {
		err = errors.Join(err, errors.New("no action store set (use tmengine.WithActionStore)"))
	}

	if smc.FinalizationStore == nil {
		err = errors.Join(err, errors.New("no finalization store set (use tmengine.WithFinalizationStore)"))
	}

	if e.mCfg.Store == nil {
		err = errors.Join(err, errors.New("no mirror store set (use tmengine.WithMirrorStore)"))
	}

	if e.mCfg.RoundStore == nil {
		err = errors.Join(err, errors.New("no round store set (use tmengine.WithRoundStore)"))
	}

	if e.mCfg.ValidatorStore == nil {
		err = errors.Join(err, errors.New("no validator store set (use tmengine.WithValidatorStore)"))
	}

	if e.watchdog == nil {
		err = errors.Join(err, errors.New("no watchdog set (use tmengine.WithWatchdog)"))
	}

	if smc.ConsensusStrategy == nil {
		err = errors.Join(err, errors.New("no consensus strategy set (use tmengine.WithConsensusStrategy)"))
	}

	if smc.FinalizeBlockRequestCh == nil {
		err = errors.Join(err, errors.New("no block finalization channel set (use tmengine.WithBlockFinalizationChannel)"))
	}

	// TODO: we are currently not validating the presence of the AppDataArrival channel.
	// Add a WithoutAppDataArrival() option so that the rare case
	// of not needing to separately retrieve app data is explicitly opt-in.
	// Fail validation if both or neither of the option pair is provided.

	// This is one special case.
	// Tests instantiate a tmstate.MockRoundTimer to avoid reliance on the wall clock.
	// But, external callers are expected to only provide a TimeoutStrategy.
	// So even though we are inspecting the lower-level RoundTimer value,
	// we return an error about the API external callers are expected to use.
	if smc.RoundTimer == nil {
		err = errors.Join(err, errors.New("no timeout strategy set (use tmengine.WithTimeoutStrategy)"))
	}

	return err
}

// maybeInitializeChain checks if we need to call into the app for InitChain, and calls it if required.
//
// The Genesis value returned is only populated if InitChain was called.
// It needs to be set in the state machine config.
func (e *Engine) maybeInitializeChain(
	ctx context.Context, fStore tmstore.FinalizationStore,
) (tmconsensus.Genesis, error) {
	// First we check if there is a stored network height-round.
	// While we don't set that directly in the Engine,
	// the value being present indicates the system has already been initialized.
	// Moreover, it is possible that the finalization prior to the initial height
	// has been pruned, so we can't rely solely on the finalization.
	_, _, _, _, err := e.mCfg.Store.NetworkHeightRound(ctx)
	if err == nil {
		// Successful load, so we don't need to initialize.
		return tmconsensus.Genesis{}, nil
	}
	if err != tmstore.ErrStoreUninitialized {
		return tmconsensus.Genesis{}, fmt.Errorf(
			"unexpected error checking network height-round before for chain initialization: %w", err,
		)
	}

	initFinHeight := e.genesis.InitialHeight - 1
	// The mirror store was uninitialized, so we have never reached mirror initialization.
	// Next we have to confirm that there is no finalization prior to the initial height.
	// It is possible, though unlikely, that we ran InitChain once before but failed to reach the Mirror.
	_, _, _, _, err = fStore.LoadFinalizationByHeight(ctx, initFinHeight)
	if err == nil {
		// We have the finalization, so we don't need to initialize the chain.
		return tmconsensus.Genesis{}, nil
	}
	if !errors.Is(err, tmconsensus.HeightUnknownError{Want: initFinHeight}) {
		return tmconsensus.Genesis{}, fmt.Errorf(
			"unexpected error checking pre-genesis finalization for chain initialization: %w", err,
		)
	}

	// Now, we have to initialize the chain.
	// If the init chain channel was not set in the options, we fail now.
	if e.initChainCh == nil {
		return tmconsensus.Genesis{}, errors.New(
			"chain is uninitialized and no init chain channel provided (use tmengine.WithInitChainChannel)",
		)
	}

	// We have a valid init chain channel, so make the request.
	e.log.Info("Making init chain request to application")
	respCh := make(chan tmdriver.InitChainResponse) // Unbuffered since we block on the read.
	req := tmdriver.InitChainRequest{
		Genesis: *e.genesis,
		Resp:    respCh,
	}
	resp, ok := gchan.ReqResp(
		ctx, e.log,
		e.initChainCh, req,
		respCh,
		"initializing chain",
	)
	if !ok {
		return tmconsensus.Genesis{}, fmt.Errorf(
			"context cancelled while initializing chain: %w", context.Cause(ctx),
		)
	}

	// Confirm whether the validators were overridden.
	vals := resp.Validators
	if len(vals) == 0 {
		// Nil validators in init chain response means use whatever was in genesis.
		vals = e.genesis.GenesisValidators
	}

	// Get the block hash from the genesis with possibly updated validators.
	updatedGenesis := tmconsensus.Genesis{
		ChainID:             e.genesis.ChainID,
		InitialHeight:       e.genesis.InitialHeight,
		CurrentAppStateHash: resp.AppStateHash,
		Validators:          vals,
	}
	b, err := updatedGenesis.Block(e.hashScheme)
	if err != nil {
		return tmconsensus.Genesis{}, fmt.Errorf("failure building genesis block: %w", err)
	}

	// Now we have the finalization; we have to store it.
	if err := fStore.SaveFinalization(
		ctx,
		initFinHeight, 0,
		string(b.Hash),
		vals,
		string(resp.AppStateHash),
	); err != nil {
		return tmconsensus.Genesis{}, fmt.Errorf("failure saving genesis finalization: %w", err)
	}

	e.log.Info(
		"Chain initialized",
		"initial_height", e.genesis.InitialHeight,
		"initial_app_state_hash", glog.Hex(resp.AppStateHash),
	)

	return updatedGenesis, nil
}

func (e *Engine) HandleProposedBlock(ctx context.Context, pb tmconsensus.ProposedBlock) tmconsensus.HandleProposedBlockResult {
	return e.m.HandleProposedBlock(ctx, pb)
}

func (e *Engine) HandlePrevoteProofs(ctx context.Context, p tmconsensus.PrevoteSparseProof) tmconsensus.HandleVoteProofsResult {
	return e.m.HandlePrevoteProofs(ctx, p)
}

func (e *Engine) HandlePrecommitProofs(ctx context.Context, p tmconsensus.PrecommitSparseProof) tmconsensus.HandleVoteProofsResult {
	return e.m.HandlePrecommitProofs(ctx, p)
}
