package gwatchdog

import (
	"context"
	"errors"
)

// IsTermination reports whether the context was cancelled by the watchdog.
func IsTermination(ctx context.Context) bool {
	e := context.Cause(ctx)
	if e == nil {
		return false
	}

	var ftr FailureToRespondError
	if errors.As(e, &ftr) {
		return true
	}

	var ft ForcedTerminationError
	return errors.As(e, &ft)
}

// FailureToRespondError indicates a particular subsystem failed to respond
// to its watchdog monitor within the configured response expectation duration.
type FailureToRespondError struct {
	SubsystemName string
}

func (e FailureToRespondError) Error() string {
	return e.SubsystemName + " failed to respond to watchdog monitoring within expected duration"
}

// ForcedTerminationError indicates that [*Watchdog.Terminate] was called.
type ForcedTerminationError struct {
	Reason string
}

func (e ForcedTerminationError) Error() string {
	return "Watchdog forced termination: " + e.Reason
}
