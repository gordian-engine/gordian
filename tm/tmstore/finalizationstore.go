package tmstore

import (
	"context"

	"github.com/gordian-engine/gordian/tm/tmconsensus"
)

type FinalizationStore interface {
	SaveFinalization(
		ctx context.Context,
		height uint64, round uint32,
		blockHash string,
		valSet tmconsensus.ValidatorSet,
		appStateHash string,
	) error

	LoadFinalizationByHeight(ctx context.Context, height uint64) (
		round uint32,
		blockHash string,
		valSet tmconsensus.ValidatorSet,
		appStateHash string,
		err error,
	)
}
