package gwatchdog

import (
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"
)

type MonitorConfig struct {
	// The name of the subsystem being monitored, for reporting purposes.
	Name string

	// The watchdog will poll the subsystem every Interval + [-Jitter, +Jitter) duration.
	// The jitter range is uniformly distributed.
	Interval, Jitter time.Duration

	// If the subsystem does not both accept the signal
	// and close its Alive response channel within ResponseTimeout,
	// the watchdog sends a termination signal to the entire system.
	ResponseTimeout time.Duration
}

func (c MonitorConfig) validate() error {
	var err error
	if c.Name == "" {
		err = errors.Join(err, errors.New("MonitorConfig.Name must not be empty"))
	}

	if c.Interval <= 0 {
		err = errors.Join(err, errors.New("MonitorConfig.Interval must be positive"))
	}

	if c.Jitter <= 0 {
		err = errors.Join(err, errors.New("MonitorConfig.Jitter must be positive"))
	}

	if c.Jitter > c.Interval {
		err = errors.Join(err, errors.New("MonitorConfig.Jitter must be less than MonitorConfig.Interval"))
	}

	if c.Jitter <= 0 {
		err = errors.Join(err, errors.New("MonitorConfig.Jitter must be positive"))
	}

	if c.ResponseTimeout <= 0 {
		err = errors.Join(err, errors.New("MonitorConfig.ResponseTimeout must be positive"))
	}

	return err
}

// monitor runs in its own goroutine to poll a subsystem on an interval
// specified by cfg.
func monitor(
	ctx context.Context,
	log *slog.Logger,
	cfg MonitorConfig,
	wg *sync.WaitGroup,
	sigCh chan<- Signal,
	cancel context.CancelCauseFunc,
) {
	defer wg.Done()

	// Every monitor instance gets its own RNG, seeded by the global RNG.
	// The memory overhead of a PCG is two uint64s,
	// and the Rand value just wraps the PCG in a rand.Source interface,
	// so the trivial amount of memory used to avoid a mutex seems worth it.
	rng := rand.New(rand.NewPCG(rand.Uint64(), rand.Uint64()))

	for {
		j := rng.Int64N(int64(2*cfg.Jitter)) - int64(cfg.Jitter)

		timer := time.NewTimer(cfg.Interval + time.Duration(j))

		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
			if !checkSubsys(ctx, log, cfg.Name, cfg.ResponseTimeout, sigCh, cancel) {
				return
			}
		}
	}
}

func checkSubsys(
	ctx context.Context,
	log *slog.Logger,
	name string,
	responseTimeout time.Duration,
	sigCh chan<- Signal,
	cancel context.CancelCauseFunc,
) (ok bool) {
	alive := make(chan struct{})
	sig := Signal{
		Alive: alive,
	}
	timer := time.NewTimer(responseTimeout)
	defer timer.Stop()

	// First the signal needs to be received within the timeout.
	select {
	case <-ctx.Done():
		return false
	case sigCh <- sig:
		// Okay, keep going.
	case <-timer.C:
		cancel(FailureToRespondError{SubsystemName: name})

		// Does the return value really matter here?
		return true
	}

	// Expect to receive the signal before the timeout.
	select {
	case <-ctx.Done():
		// Context finished, so quit.
		return false
	case <-alive:
		// Okay.
		return true
	case <-timer.C:
		// If the timer elapsed, we will do one final fast check,
		// as it is remotely possible they responded before the timer elapsed
		// but the runtime chose the timer path from the available cases at random.
		select {
		case <-alive:
			// Good.
			return true
		default:
			// Still didn't have the signal, so we failed.
			cancel(FailureToRespondError{SubsystemName: name})

			// Does the return value really matter here?
			return true
		}
	}
}
