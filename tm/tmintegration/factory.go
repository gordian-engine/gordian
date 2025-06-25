package tmintegration

import (
	"context"
	"log/slog"
	"testing"

	"github.com/gordian-engine/gordian/gcrypto"
	"github.com/gordian-engine/gordian/tm/tmconsensus"
	"github.com/gordian-engine/gordian/tm/tmconsensus/tmconsensustest"
	"github.com/gordian-engine/gordian/tm/tmgossip"
	"github.com/gordian-engine/gordian/tm/tmp2p"
	"github.com/gordian-engine/gordian/tm/tmp2p/tmp2ptest"
	"github.com/gordian-engine/gordian/tm/tmstore"
)

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

type Factory interface {
	// NewNetwork will be called only once per test.
	// The implementer may assume that the context will be canceled
	// at or before the test's completion.
	NewNetwork(*testing.T, context.Context, *gcrypto.Registry) (tmp2ptest.Network, error)

	NewActionStore(context.Context, int) (tmstore.ActionStore, error)
	NewCommittedHeaderStore(context.Context, int) (tmstore.CommittedHeaderStore, error)
	NewFinalizationStore(context.Context, int) (tmstore.FinalizationStore, error)
	NewMirrorStore(context.Context, int) (tmstore.MirrorStore, error)
	NewRoundStore(context.Context, int) (tmstore.RoundStore, error)
	NewStateMachineStore(context.Context, int) (tmstore.StateMachineStore, error)
	NewValidatorStore(context.Context, int, tmconsensus.HashScheme) (tmstore.ValidatorStore, error)

	// TODO: fixture probably takes precedence over this?
	HashScheme(context.Context, int) (tmconsensus.HashScheme, error)
	SignatureScheme(context.Context, int) (tmconsensus.SignatureScheme, error)
	CommonMessageSignatureProofScheme(context.Context, int) (gcrypto.CommonMessageSignatureProofScheme, error)

	NewGossipStrategy(context.Context, int, tmp2p.Connection) (tmgossip.Strategy, error)

	NewConsensusFixture(nVals int) *tmconsensustest.Fixture
}
