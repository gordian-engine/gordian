package tmmemstore_test

import (
	"context"
	"testing"

	"github.com/gordian-engine/gordian/tm/tmstore"
	"github.com/gordian-engine/gordian/tm/tmstore/tmmemstore"
	"github.com/gordian-engine/gordian/tm/tmstore/tmstoretest"
)

func TestStateMachineStore(t *testing.T) {
	t.Parallel()

	tmstoretest.TestStateMachineStoreCompliance(t, func(ctx context.Context, _ func(func())) (tmstore.StateMachineStore, error) {
		return tmmemstore.NewStateMachineStore(), nil
	})
}
