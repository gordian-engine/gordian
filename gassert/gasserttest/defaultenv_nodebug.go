//go:build !debug

package gasserttest

import "github.com/gordian-engine/gordian/gassert"

// DefaultEnv returns the no-op Env, in non-debug builds.
func DefaultEnv() gassert.Env {
	return gassert.Env{}
}

// NopEnv returns the no-op Env.
// This should generally not be used, but maybe it is helpful in already expensive tests,
// when debug builds are enabled.
func NopEnv() gassert.Env {
	return gassert.Env{}
}
