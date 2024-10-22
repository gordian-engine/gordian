package tmengine

import (
	"context"
	"errors"
	"log/slog"

	"github.com/gordian-engine/gordian/tm/tmconsensus"
	"github.com/gordian-engine/gordian/tm/tmengine/internal/tmmirror"
)

// The Mirror follows the state of the active validators on the network,
// replicating the blocks and votes they produce.
//
// The Mirror is normally an internal component of a full [Engine]
// including a state machine connected to a user-defined application.
// However, in some cases, it may be desirable to run a Mirror by itself
// for the sake of tracking the state of the rest of the network.
type Mirror interface {
	tmconsensus.FineGrainedConsensusHandler

	// Wait blocks until the Mirror is finished.
	// Stop the mirror by canceling the context passed to [NewMirror].
	Wait()
}

func NewMirror(ctx context.Context, log *slog.Logger, opts ...Opt) (Mirror, error) {
	// We borrow the engine options to configure the mirror,
	// so we need an Engine value to collect the options.
	// Note that we never start the Engine we instantiate.
	var e Engine

	var err error
	for _, opt := range opts {
		err = errors.Join(opt(&e, nil))
	}
	if err != nil {
		return nil, err
	}

	cfg := e.mCfg
	cfg.InitialHeight = e.genesis.InitialHeight
	cfg.InitialValidatorSet = e.genesis.GenesisValidatorSet

	if err := validateMirrorSettings(cfg); err != nil {
		return nil, err
	}

	m, err := tmmirror.NewMirror(ctx, log, cfg)
	if err != nil {
		return nil, err
	}

	return m, nil
}

func validateMirrorSettings(cfg tmmirror.MirrorConfig) error {
	var err error

	if cfg.Store == nil {
		err = errors.Join(err, errors.New("no mirror store set (use tmengine.WithMirrorStore)"))
	}

	if cfg.CommittedHeaderStore == nil {
		err = errors.Join(err, errors.New("no committed header store set (use tmengine.WithCommittedHeaderStore)"))
	}

	if cfg.RoundStore == nil {
		err = errors.Join(err, errors.New("no round store set (use tmengine.WithRoundStore)"))
	}
	if cfg.ValidatorStore == nil {
		err = errors.Join(err, errors.New("no validator store set (use tmengine.WithValidatorStore)"))
	}

	// TODO: validate InitialHeight and InitialValidators?

	if cfg.HashScheme == nil {
		err = errors.Join(err, errors.New("no hash scheme set (use tmengine.WithHashScheme)"))
	}
	if cfg.SignatureScheme == nil {
		err = errors.Join(err, errors.New("no signature scheme set (use tmengine.WithSignatureScheme)"))
	}
	if cfg.CommonMessageSignatureProofScheme == nil {
		err = errors.Join(err, errors.New("no common message signature proof scheme set (use tmengine.WithCommonMessageSignatureProofScheme)"))
	}

	return err
}
