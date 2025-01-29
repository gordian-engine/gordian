package tmmemstore_test

import (
	"testing"

	"github.com/gordian-engine/gordian/tm/tmconsensus/tmconsensustest"
	"github.com/gordian-engine/gordian/tm/tmstore"
	"github.com/gordian-engine/gordian/tm/tmstore/tmmemstore"
	"github.com/gordian-engine/gordian/tm/tmstore/tmstoretest"
)

func TestActionStore(t *testing.T) {
	t.Parallel()

	tmstoretest.TestActionStoreCompliance(t, func(func(func())) (tmstore.ActionStore, error) {
		return tmmemstore.NewActionStore(), nil
	}, tmconsensustest.NewEd25519Fixture)
}
