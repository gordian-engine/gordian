package tmintegration_test

import (
	"testing"

	"github.com/gordian-engine/gordian/tm/tmintegration"
)

type DaisyChainInmemFactory struct {
	tmintegration.DaisyChainFactory

	tmintegration.ConsensusFixtureFactory

	tmintegration.InmemStoreFactory
	tmintegration.InmemSchemeFactory
}

func TestDaisyChainInmem(t *testing.T) {
	t.Parallel()

	tmintegration.RunIntegrationTest(t, func(e *tmintegration.Env) tmintegration.Factory {
		return DaisyChainInmemFactory{
			DaisyChainFactory: tmintegration.NewDaisyChainFactory(e),

			ConsensusFixtureFactory: tmintegration.Ed25519ConsensusFixtureFactory{},
		}
	})
}
