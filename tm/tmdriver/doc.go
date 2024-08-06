// Package tmdriver contains types for the driver to interact with the consensus engine.
// The driver could be considered as the "application" to the consensus engine,
// but we use the term driver here because this is a low-level interface to the engine
// and it clearly disambiguates from the userspace application that is likely interacting
// with another layer such as the Cosmos SDK.
//
// While other packages are focused on other primitives that the driver must be aware of,
// this package focuses specifically and directly on the engine-driver interactions.
package tmdriver
