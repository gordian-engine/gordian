package tmmemstore_test

import (
	"testing"

	"github.com/gordian-engine/gordian/tm/tmconsensus/tmconsensustest"
	"github.com/gordian-engine/gordian/tm/tmstore"
	"github.com/gordian-engine/gordian/tm/tmstore/tmmemstore"
	"github.com/gordian-engine/gordian/tm/tmstore/tmstoretest"
)

func TestMemRoundStore(t *testing.T) {
	t.Parallel()

	tmstoretest.TestRoundStoreCompliance(t, func(func(func())) (tmstore.RoundStore, error) {
		return tmmemstore.NewRoundStore(), nil
	}, tmconsensustest.NewEd25519Fixture)
}
