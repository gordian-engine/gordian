//go:build debug

package gasserttest

import "github.com/gordian-engine/gordian/gassert"

// DefaultEnv returns an assertion environment that enables all assertion checks.
func DefaultEnv() gassert.Env {
	env, err := gassert.EnvironmentFromString("*")
	if err != nil {
		panic(err)
	}
	env.UseCaching()
	return env
}

// NopEnv returns an assertion environment that disables all assertion checks.
// This should generally not be used, but maybe it is helpful in already expensive tests.
func NopEnv() gassert.Env {
	env, err := gassert.EnvironmentFromString("")
	if err != nil {
		panic(err)
	}
	env.UseCaching()
	return env
}
