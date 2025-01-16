package tmengine

import (
	"context"
	"errors"
	"fmt"

	"github.com/gordian-engine/gordian/gassert"
	"github.com/gordian-engine/gordian/gcrypto"
	"github.com/gordian-engine/gordian/gwatchdog"
	"github.com/gordian-engine/gordian/tm/tmconsensus"
	"github.com/gordian-engine/gordian/tm/tmdriver"
	"github.com/gordian-engine/gordian/tm/tmengine/internal/tmstate"
	"github.com/gordian-engine/gordian/tm/tmengine/tmelink"
	"github.com/gordian-engine/gordian/tm/tmgossip"
	"github.com/gordian-engine/gordian/tm/tmstore"
)

// Opt is an option for the Engine.
// The underlying function signature for Opt is subject to change at any time.
// Only Opt values returned by With* functions may be considered stable values.
type Opt func(*Engine, *tmstate.StateMachineConfig) error

// WithConsensusStrategy sets the engine's consensus strategy.
// This option is required.
func WithConsensusStrategy(cs tmconsensus.ConsensusStrategy) Opt {
	return func(_ *Engine, smc *tmstate.StateMachineConfig) error {
		smc.ConsensusStrategy = cs
		return nil
	}
}

// WithGossipStrategy sets the engine's gossip strategy.
// This option is required.
func WithGossipStrategy(gs tmgossip.Strategy) Opt {
	return func(e *Engine, _ *tmstate.StateMachineConfig) error {
		e.gs = gs
		return nil
	}
}

// WithActionStore sets the engine's action store.
// This option is required if using a non-nil signer.
func WithActionStore(s tmstore.ActionStore) Opt {
	return func(_ *Engine, smc *tmstate.StateMachineConfig) error {
		smc.ActionStore = s
		return nil
	}
}

// WithCommittedHeaderStore sets the engine's committed header store.
// This option is required.
func WithCommittedHeaderStore(s tmstore.CommittedHeaderStore) Opt {
	return func(e *Engine, _ *tmstate.StateMachineConfig) error {
		e.mCfg.CommittedHeaderStore = s
		return nil
	}
}

// WithFinalizationStore sets the engine's finalization store.
// This option is required.
func WithFinalizationStore(s tmstore.FinalizationStore) Opt {
	return func(_ *Engine, smc *tmstate.StateMachineConfig) error {
		smc.FinalizationStore = s
		return nil
	}
}

// WithMirrorStore sets the engine's mirror store.
// This option is required.
func WithMirrorStore(s tmstore.MirrorStore) Opt {
	return func(e *Engine, _ *tmstate.StateMachineConfig) error {
		e.mCfg.Store = s
		return nil
	}
}

// WithRoundStore sets the engine's round store.
// This option is required.
func WithRoundStore(s tmstore.RoundStore) Opt {
	return func(e *Engine, smc *tmstate.StateMachineConfig) error {
		if e != nil {
			e.mCfg.RoundStore = s
		}
		return nil
	}
}

func WithStateMachineStore(s tmstore.StateMachineStore) Opt {
	return func(_ *Engine, smc *tmstate.StateMachineConfig) error {
		smc.StateMachineStore = s
		return nil
	}
}

// WithValidatorStore sets the engine's validator store.
// This option is required.
func WithValidatorStore(s tmstore.ValidatorStore) Opt {
	return func(e *Engine, smc *tmstate.StateMachineConfig) error {
		e.mCfg.ValidatorStore = s
		return nil
	}
}

// WithSignatureScheme sets the engine's signature scheme.
// This option is required.
func WithSignatureScheme(s tmconsensus.SignatureScheme) Opt {
	return func(e *Engine, smc *tmstate.StateMachineConfig) error {
		e.sigScheme = s
		e.mCfg.SignatureScheme = s
		smc.SignatureScheme = s
		return nil
	}
}

// WithHashScheme sets the engine's hash scheme.
// This option is required.
func WithHashScheme(h tmconsensus.HashScheme) Opt {
	return func(e *Engine, smc *tmstate.StateMachineConfig) error {
		e.hashScheme = h
		e.mCfg.HashScheme = h
		smc.HashScheme = h
		return nil
	}
}

// WithCommonMessageSignatureProofScheme sets the engine's common message signature proof scheme.
// This option is required.
func WithCommonMessageSignatureProofScheme(s gcrypto.CommonMessageSignatureProofScheme) Opt {
	return func(e *Engine, smc *tmstate.StateMachineConfig) error {
		e.cmspScheme = s
		e.mCfg.CommonMessageSignatureProofScheme = s
		smc.CommonMessageSignatureProofScheme = s
		return nil
	}
}

// WithSigner sets the engine's signer.
// If omitted or set to nil, the engine will never actively participate in consensus;
// it will only operate as an observer.
func WithSigner(s tmconsensus.Signer) Opt {
	return func(_ *Engine, smc *tmstate.StateMachineConfig) error {
		smc.Signer = s
		return nil
	}
}

// WithGenesis sets the engine's ExternalGenesis.
// This option is required.
func WithGenesis(g *tmconsensus.ExternalGenesis) Opt {
	return func(e *Engine, smc *tmstate.StateMachineConfig) error {
		e.genesis = g
		return nil
	}
}

// WithInitChainChannel sets the init chain channel for the engine to send on.
// This option is only required if the chain has not yet been initialized.
func WithInitChainChannel(ch chan<- tmdriver.InitChainRequest) Opt {
	return func(e *Engine, _ *tmstate.StateMachineConfig) error {
		e.initChainCh = ch
		return nil
	}
}

// WithBlockFinalizationChannel sets the channel that the engine sends on
// when a block is due to be finalized.
// The application must receive from this channel.
// This option is required.
func WithBlockFinalizationChannel(ch chan<- tmdriver.FinalizeBlockRequest) Opt {
	return func(_ *Engine, smc *tmstate.StateMachineConfig) error {
		smc.FinalizeBlockRequestCh = ch
		return nil
	}
}

// WithAppDataArrivalChannel sets the channel that the engine reads from
// in order to refresh the consensus strategy,
// in the event that application data is received
// later than a proposed block is received.
func WithBlockDataArrivalChannel(ch <-chan tmelink.BlockDataArrival) Opt {
	return func(_ *Engine, smc *tmstate.StateMachineConfig) error {
		smc.BlockDataArrivalCh = ch
		return nil
	}
}

// WithLagStateChannel sets the channel that the engine writes to
// when its lag state changes.
// This option is not required, but is strongly recommended.
func WithLagStateChannel(ch chan<- tmelink.LagState) Opt {
	return func(e *Engine, _ *tmstate.StateMachineConfig) error {
		if cap(ch) != 0 {
			// cap(nil) is also 0, notably.
			// We'll allow a nil ch for now, but one could argue against allowing that.
			return fmt.Errorf("WithLagStateChannel: capacity of channel must be zero (got %d)", cap(ch))
		}

		e.mCfg.LagStateOut = ch
		return nil
	}
}

// WithReplayedHeaderRequestChannel sets the channel that the engine
// reads replayed header requests from.
// This option is not required, but is strongly recommended.
func WithReplayedHeaderRequestChannel(ch <-chan tmelink.ReplayedHeaderRequest) Opt {
	return func(e *Engine, _ *tmstate.StateMachineConfig) error {
		e.mCfg.ReplayedHeadersIn = ch
		return nil
	}
}

type roundTimer = tmstate.RoundTimer

// WithInternalRoundTimer sets the round timer, an internal type to the engine's state machine.
// This is only intended for testing.
//
// Non-test usage should call [WithTimeoutStrategy] to use an exported type.
func WithInternalRoundTimer(rt roundTimer) Opt {
	return func(_ *Engine, smc *tmstate.StateMachineConfig) error {
		smc.RoundTimer = rt
		return nil
	}
}

// WithTimeoutStrategy sets the timeout strategy
// for calculating state machine timeouts during consensus.
// The context value controls the lifecycle of the timer.
func WithTimeoutStrategy(ctx context.Context, s TimeoutStrategy) Opt {
	return WithInternalRoundTimer(tmstate.NewStandardRoundTimer(ctx, s))
}

// WithWatchdog sets the engine's watchdog, propagating it through subsystems of the engine.
// This option is required.
// For tests, the caller may use [gwatchdog.NewNopWatchdog] to avoid creating unnecessary goroutines.
func WithWatchdog(wd *gwatchdog.Watchdog) Opt {
	return func(e *Engine, smc *tmstate.StateMachineConfig) error {
		e.watchdog = wd
		e.mCfg.Watchdog = wd
		smc.Watchdog = wd
		return nil
	}
}

// WithMetricsChannel sets the channel where the engine
// emits metrics for its subsystems.
func WithMetricsChannel(ch chan<- Metrics) Opt {
	return func(e *Engine, _ *tmstate.StateMachineConfig) error {
		if len(ch) != 0 {
			return errors.New("WithMetricsChannel: ch must be unbuffered")
		}
		e.metricsCh = ch
		return nil
	}
}

// WithAssertEnv sets the assert environment on the engine ands its subcomponents.
// It is safe to exclude this option in builds that do not have the "debug" build tag.
// However, in debug builds, omitting this option will cause a runtime panic.
func WithAssertEnv(assertEnv gassert.Env) Opt {
	return func(e *Engine, smc *tmstate.StateMachineConfig) error {
		e.mCfg.AssertEnv = assertEnv
		smc.AssertEnv = assertEnv
		return nil
	}
}
