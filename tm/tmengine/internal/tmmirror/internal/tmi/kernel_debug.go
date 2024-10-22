//go:build debug

package tmi

import (
	"fmt"

	"github.com/gordian-engine/gordian/gassert"
	"github.com/gordian-engine/gordian/tm/tmengine/tmelink"
)

// invariantReplayedHeaderResponse asserts that err is
// one of the few acceptable error types to send back on the [ReplayedHeaderResponse].
func invariantReplayedHeaderResponse(env gassert.Env, err error) {
	if !env.Enabled("tm.engine.mirror.kernel.replayed_header_response") {
		return
	}

	if err == nil {
		return
	}

	switch err.(type) {
	case tmelink.ReplayedHeaderValidationError,
		tmelink.ReplayedHeaderOutOfSyncError,
		tmelink.ReplayedHeaderInternalError:
		return
	}

	env.HandleAssertionFailure(fmt.Errorf(
		"illegal error type %T returned in response to replayed header: %w", err, err,
	))
}
