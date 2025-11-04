package tmintegration

import (
	"context"

	"github.com/gordian-engine/gordian/gcrypto"
	"github.com/gordian-engine/gordian/tm/tmconsensus"
	"github.com/gordian-engine/gordian/tm/tmconsensus/tmconsensustest"
	"github.com/gordian-engine/gordian/tm/tmstore"
	"github.com/gordian-engine/gordian/tm/tmstore/tmmemstore"
)

// InmemStoreFactory is meant to be embedded in another [tmintegration.Factory]
// to provide in-memory implementations of stores.
type InmemStoreFactory struct{}

func (f InmemStoreFactory) NewActionStore(ctx context.Context, idx int) (tmstore.ActionStore, error) {
	return tmmemstore.NewActionStore(), nil
}

func (f InmemStoreFactory) NewFinalizationStore(ctx context.Context, idx int) (tmstore.FinalizationStore, error) {
	return tmmemstore.NewFinalizationStore(), nil
}

func (f InmemStoreFactory) NewCommittedHeaderStore(ctx context.Context, idx int) (tmstore.CommittedHeaderStore, error) {
	return tmmemstore.NewCommittedHeaderStore(), nil
}

func (f InmemStoreFactory) NewMirrorStore(ctx context.Context, idx int) (tmstore.MirrorStore, error) {
	return tmmemstore.NewMirrorStore(), nil
}

func (f InmemStoreFactory) NewRoundStore(ctx context.Context, idx int) (tmstore.RoundStore, error) {
	return tmmemstore.NewRoundStore(), nil
}

func (f InmemStoreFactory) NewStateMachineStore(ctx context.Context, idx int) (tmstore.StateMachineStore, error) {
	return tmmemstore.NewStateMachineStore(), nil
}

func (f InmemStoreFactory) NewValidatorStore(ctx context.Context, idx int, hs tmconsensus.HashScheme) (tmstore.ValidatorStore, error) {
	return tmmemstore.NewValidatorStore(hs), nil
}

type InmemSchemeFactory struct{}

func (f InmemSchemeFactory) HashScheme(ctx context.Context, idx int) (tmconsensus.HashScheme, error) {
	return tmconsensustest.SimpleHashScheme{}, nil
}

func (f InmemSchemeFactory) SignatureScheme(ctx context.Context, idx int) (tmconsensus.SignatureScheme, error) {
	return tmconsensustest.SimpleSignatureScheme{}, nil
}

func (f InmemSchemeFactory) CommonMessageSignatureProofScheme(ctx context.Context, idx int) (gcrypto.CommonMessageSignatureProofScheme, error) {
	return gcrypto.SimpleCommonMessageSignatureProofScheme{}, nil
}

// InmemStoreNetwork implements a subset of the [Network] interface,
// for tests that do not need to explicitly exercise stores.
type InmemStoreNetwork struct{}

func (f InmemStoreNetwork) NewActionStore(ctx context.Context, idx int) tmstore.ActionStore {
	return tmmemstore.NewActionStore()
}

func (f InmemStoreNetwork) NewFinalizationStore(ctx context.Context, idx int) tmstore.FinalizationStore {
	return tmmemstore.NewFinalizationStore()
}

func (f InmemStoreNetwork) NewCommittedHeaderStore(ctx context.Context, idx int) tmstore.CommittedHeaderStore {
	return tmmemstore.NewCommittedHeaderStore()
}

func (f InmemStoreNetwork) NewMirrorStore(ctx context.Context, idx int) tmstore.MirrorStore {
	return tmmemstore.NewMirrorStore()
}

func (f InmemStoreNetwork) NewRoundStore(ctx context.Context, idx int) tmstore.RoundStore {
	return tmmemstore.NewRoundStore()
}

func (f InmemStoreNetwork) NewStateMachineStore(ctx context.Context, idx int) tmstore.StateMachineStore {
	return tmmemstore.NewStateMachineStore()
}

func (f InmemStoreNetwork) NewValidatorStore(ctx context.Context, idx int, hs tmconsensus.HashScheme) tmstore.ValidatorStore {
	return tmmemstore.NewValidatorStore(hs)
}
