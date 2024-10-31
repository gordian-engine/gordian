// Package tmelink contains types used by the internals of the
// [github.com/gordian-engine/gordian/tm/tmengine.Engine]
// that need to be exposed outside of the tmengine package.
// In other words, the types in this package are used to "link"
// individual components of the engine.
//
// It exists as a separate package to avoid a circular dependency
// between tmengine and its internal packages.
package tmelink
