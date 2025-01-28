package tmintegration_test

import (
	"testing"

	"github.com/gordian-engine/gordian/tm/tmintegration"
)

// Libp2pInmemFactory uses a basic libp2p factory
// along with in-mem stores and the default scheme factories.
type Libp2pInmemFactory struct {
	tmintegration.Libp2pFactory

	tmintegration.InmemStoreFactory
	tmintegration.InmemSchemeFactory
}

func TestLibp2pInmem(t *testing.T) {
	tmintegration.RunIntegrationTest(t, func(e *tmintegration.Env) tmintegration.Factory {
		lf := tmintegration.NewLibp2pFactory(e)
		return Libp2pInmemFactory{
			Libp2pFactory: lf,
		}
	})
}
