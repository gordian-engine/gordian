package tmintegration

import (
	"context"
	"log/slog"
	"testing"

	"github.com/gordian-engine/gordian/tm/tmconsensus"
	"github.com/gordian-engine/gordian/tm/tmconsensus/tmconsensustest"
	"github.com/gordian-engine/gordian/tm/tmgossip"
	"github.com/gordian-engine/gordian/tm/tmp2p"
	"github.com/gordian-engine/gordian/tm/tmp2p/tmp2ptest"
	"github.com/gordian-engine/gordian/tm/tmstore"
)

// FactoryFunc is the function type to create a Network and a StoreFactory.
type FactoryFunc func(
	t *testing.T,
	ctx context.Context,
	stores []BlockDataStore,
) (Network, StoreFactory)

// Network represents a collection of connected gossip strategies.
type Network interface {
	Fixture() *tmconsensustest.Fixture

	GetGossipStrategy(ctx context.Context, idx int) tmgossip.Strategy

	SetConsensusHandler(ctx context.Context, idx int, h tmconsensus.ConsensusHandler)

	// Block while the network stabilizes.
	// This will be a no-op for some networks.
	Stabilize(context.Context)

	// Block until all background work is finished.
	Wait()
}

// StoreFactory defines how to get stores for an integration test.
type StoreFactory interface {
	NewActionStore(context.Context, int) tmstore.ActionStore
	NewCommittedHeaderStore(context.Context, int) tmstore.CommittedHeaderStore
	NewFinalizationStore(context.Context, int) tmstore.FinalizationStore
	NewMirrorStore(context.Context, int) tmstore.MirrorStore
	NewRoundStore(context.Context, int) tmstore.RoundStore
	NewStateMachineStore(context.Context, int) tmstore.StateMachineStore
	NewValidatorStore(context.Context, int, tmconsensus.HashScheme) tmstore.ValidatorStore
}

// Env contains some of the primitives of the current test environment,
// to inform the creation of a [Factory].
type Env struct {
	// The RootLogger can be used when the Factory
	// needs a logger in a created value.
	RootLogger *slog.Logger

	// Inline interface to avoid directly depending on testing package.
	tb interface {
		Cleanup(func())

		TempDir() string
	}
}

// TempDir returns the path to a new temporary directory,
// in case the factory needs a place to write data to disk.
func (e *Env) TempDir() string {
	return e.tb.TempDir()
}

// Cleanup calls fn when the test is complete,
// regardless of whether the test passed or failed.
func (e *Env) Cleanup(fn func()) {
	e.tb.Cleanup(fn)
}

type NewFactoryFunc func(e *Env) Factory

// Factory is the interface provided when running integration tests
// (via [RunIntegrationTest]).
//
// Within each integration sub-test:
//   - The factory func is called once, creating a new Factory instance
//   - On that factory instance, NewNetwork is called once
//   - For each validator that the test creates, [tmp2ptest.Network.Connect] is called once,
//     and all of the other factory methods (the store creators and NewGossipStrategy)
//     are called once with the corresponding index of the validator.
type Factory interface {
	// NewNetwork will be called only once per test.
	// The implementer may assume that the context will be canceled
	// at or before the test's completion.
	//
	// The method returns a tmconsensustest.Fixture
	// used internally by the integration tests.
	NewNetwork(t *testing.T, ctx context.Context, nVals int) (
		tmp2ptest.Network, *tmconsensustest.Fixture, error,
	)

	NewActionStore(context.Context, int) (tmstore.ActionStore, error)
	NewCommittedHeaderStore(context.Context, int) (tmstore.CommittedHeaderStore, error)
	NewFinalizationStore(context.Context, int) (tmstore.FinalizationStore, error)
	NewMirrorStore(context.Context, int) (tmstore.MirrorStore, error)
	NewRoundStore(context.Context, int) (tmstore.RoundStore, error)
	NewStateMachineStore(context.Context, int) (tmstore.StateMachineStore, error)
	NewValidatorStore(context.Context, int, tmconsensus.HashScheme) (tmstore.ValidatorStore, error)

	NewGossipStrategy(context.Context, int, tmp2p.Connection) (tmgossip.Strategy, error)
}
