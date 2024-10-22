package tmmemstore_test

import (
	"testing"

	"github.com/gordian-engine/gordian/tm/tmstore"
	"github.com/gordian-engine/gordian/tm/tmstore/tmmemstore"
	"github.com/gordian-engine/gordian/tm/tmstore/tmstoretest"
)

func TestMemMirrorStore(t *testing.T) {
	t.Parallel()

	tmstoretest.TestMirrorStoreCompliance(t, func(func(func())) (tmstore.MirrorStore, error) {
		return tmmemstore.NewMirrorStore(), nil
	})
}
