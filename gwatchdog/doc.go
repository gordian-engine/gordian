// Package gwatchdog provides a Watchdog type that periodically communicates
// with subsystems that have opted in to the watchdog.
// Each subsystem that opts in provides an interval and jitter indicating
// how frequently the watchdog will poll the subsystem,
// and a timeout indicating the tolerable duration for the subystem's response.
// If the subsystem does not repsond within the tolerable duration,
// the watchdog invokes a termination by canceling the root context.
package gwatchdog
