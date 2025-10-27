package tmlibp2pintegration_test

import (
	"testing"

	"github.com/gordian-engine/gordian/tm/tmintegration"
	"github.com/gordian-engine/gordian/tm/tmp2p/tmlibp2p/tmlibp2pintegration"
)

// Libp2pInmemFactory uses a basic libp2p factory
// along with in-mem stores and the default scheme factories.
type Libp2pInmemFactory struct {
	tmlibp2pintegration.Libp2pFactory

	tmintegration.ConsensusFixtureFactory

	tmintegration.InmemStoreFactory
	tmintegration.InmemSchemeFactory
}

func TestLibp2pInmem(t *testing.T) {
	tmintegration.RunIntegrationTest(t, func(e *tmintegration.Env) tmintegration.Factory {
		lf := tmlibp2pintegration.NewLibp2pFactory(e)
		return Libp2pInmemFactory{
			Libp2pFactory: lf,

			ConsensusFixtureFactory: tmintegration.Ed25519ConsensusFixtureFactory{},
		}
	})
}
