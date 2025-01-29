package tmmemstore_test

import (
	"testing"

	"github.com/gordian-engine/gordian/tm/tmconsensus/tmconsensustest"
	"github.com/gordian-engine/gordian/tm/tmstore"
	"github.com/gordian-engine/gordian/tm/tmstore/tmmemstore"
	"github.com/gordian-engine/gordian/tm/tmstore/tmstoretest"
)

func TestMemValidatorStore(t *testing.T) {
	t.Parallel()

	tmstoretest.TestValidatorStoreCompliance(t, func(func(func())) (tmstore.ValidatorStore, error) {
		return tmmemstore.NewValidatorStore(tmconsensustest.SimpleHashScheme{}), nil
	}, tmconsensustest.NewEd25519Fixture)
}
