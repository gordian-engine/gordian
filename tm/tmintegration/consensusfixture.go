package tmintegration

import "github.com/gordian-engine/gordian/tm/tmconsensus/tmconsensustest"

type ConsensusFixtureFactory interface {
	NewConsensusFixture(nVals int) *tmconsensustest.Fixture
}

type Ed25519ConsensusFixtureFactory struct{}

func (f Ed25519ConsensusFixtureFactory) NewConsensusFixture(nVals int) *tmconsensustest.Fixture {
	return tmconsensustest.NewEd25519Fixture(nVals)
}
