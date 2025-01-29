package tmstoretest

import "github.com/gordian-engine/gordian/tm/tmconsensus/tmconsensustest"

// FixtureFactory is used in every store compliance test,
// to produce validators and signatures.
//
// [tmconsensustest.NewEd25519Fixture] should be used by default
// in the core Gordian code,
// but having this as part of compliance test signatures
// makes it possible to assert that various store types
// are compatible with other key schemes.
type FixtureFactory func(nVals int) *tmconsensustest.Fixture
