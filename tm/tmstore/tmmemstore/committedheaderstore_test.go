package tmmemstore_test

import (
	"testing"

	"github.com/gordian-engine/gordian/tm/tmconsensus/tmconsensustest"
	"github.com/gordian-engine/gordian/tm/tmstore"
	"github.com/gordian-engine/gordian/tm/tmstore/tmmemstore"
	"github.com/gordian-engine/gordian/tm/tmstore/tmstoretest"
)

func TestMemCommittedHeaderStore(t *testing.T) {
	t.Parallel()

	tmstoretest.TestCommittedHeaderStoreCompliance(t, func(func(func())) (tmstore.CommittedHeaderStore, error) {
		return tmmemstore.NewCommittedHeaderStore(), nil
	}, tmconsensustest.NewEd25519Fixture)
}
