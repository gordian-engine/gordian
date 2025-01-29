package tmmemstore_test

import (
	"testing"

	"github.com/gordian-engine/gordian/tm/tmconsensus/tmconsensustest"
	"github.com/gordian-engine/gordian/tm/tmstore"
	"github.com/gordian-engine/gordian/tm/tmstore/tmmemstore"
	"github.com/gordian-engine/gordian/tm/tmstore/tmstoretest"
)

func TestMemFinalizationStore(t *testing.T) {
	t.Parallel()

	tmstoretest.TestFinalizationStoreCompliance(t, func(func(func())) (tmstore.FinalizationStore, error) {
		return tmmemstore.NewFinalizationStore(), nil
	}, tmconsensustest.NewEd25519Fixture)
}
