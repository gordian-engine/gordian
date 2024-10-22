package gcmemstore_test

import (
	"testing"

	"github.com/gordian-engine/gordian/gcosmos/gcstore"
	"github.com/gordian-engine/gordian/gcosmos/gcstore/gcmemstore"
	"github.com/gordian-engine/gordian/gcosmos/gcstore/gcstoretest"
)

func TestBlockDataStoreCompliance(t *testing.T) {
	t.Parallel()

	gcstoretest.TestBlockDataStoreCompliance(t, func() gcstore.BlockDataStore {
		return gcmemstore.NewBlockDataStore()
	})
}
