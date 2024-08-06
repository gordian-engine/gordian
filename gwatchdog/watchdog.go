package gwatchdog

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/rollchains/gordian/internal/gchan"
)

type Watchdog struct {
	log *slog.Logger

	cancel          context.CancelCauseFunc
	monitorRequests chan monitorRequest

	// We cannot know up front how many monitors the watchdog will have,
	// so a WaitGroup makes it easy to track them all.
	wg sync.WaitGroup
}

// NewWatchdog returns a new Watchdog and a context associated with the watchdog
// and derived from the passed-in context.
//
// The returned context is canceled if a subsystem who subscribes through [*Watchdog.Monitor]
// fails to respond to a signal within its configured response timeout,
// or more rarely, upon a call to [*Watchdog.Terminate].
func NewWatchdog(ctx context.Context, log *slog.Logger) (*Watchdog, context.Context) {
	wCtx, cancel := context.WithCancelCause(ctx)
	w := &Watchdog{
		log:             log,
		cancel:          cancel,
		monitorRequests: make(chan monitorRequest), // Unbuffered since requests are synchronous.
	}
	w.wg.Add(1)
	go w.kernel(ctx, wCtx, cancel)
	return w, wCtx
}

// NewNopWatchdog returns a new Watchdog that disregards calls to [*Watchdog.Monitor],
// but still respects calls to Terminate.
//
// NewNopWatchdog should only be called in test.
func NewNopWatchdog(ctx context.Context, log *slog.Logger) (*Watchdog, context.Context) {
	wCtx, cancel := context.WithCancelCause(ctx)
	w := &Watchdog{
		log:    log,
		cancel: cancel,
		// The monitorRequests channel is nil here,
		// which means that any calls to w.Monitor will return a nil signal channel.
	}
	w.wg.Add(1)
	go w.kernel(ctx, wCtx, cancel)
	return w, wCtx
}

// Wait blocks until w's background goroutines complete.
// The goroutines are tied to the lifecycle of the context passed to [NewWatchdog],
// so simply calling Terminate or failing to process a monitor signal
// are not sufficient to unblock a call to Wait.
func (w *Watchdog) Wait() {
	w.wg.Wait()
}

// Terminate forces the watchdog context to be cancelled
// with a cause of [ForcedTerminationError].
func (w *Watchdog) Terminate(reason string) {
	w.cancel(ForcedTerminationError{Reason: reason})
}

func (w *Watchdog) kernel(rootCtx, wCtx context.Context, cancel context.CancelCauseFunc) {
	defer w.wg.Done()

	for {
		select {
		case <-rootCtx.Done():
			w.log.Info("Stopping due to root context cancellation", "cause", context.Cause(rootCtx))
			return
		case req := <-w.monitorRequests:
			sigCh := make(chan Signal) // Unbuffered because it must be synchronous.
			w.wg.Add(1)

			go monitor(
				// The monitor runs off the watchdog context,
				// because it should also shut down on an abort signal.
				wCtx,
				w.log.With("target", req.Cfg.Name),
				req.Cfg,
				&w.wg, sigCh, cancel,
			)

			req.Resp <- sigCh
		}
	}
}

// monitorRequest is sent from a separate goroutine calling [*Watchdog.Monitor]
// to the watchdog's kernel goroutine.
type monitorRequest struct {
	Cfg MonitorConfig

	// Response to the caller, who needs a receive-only channel of signals.
	Resp chan (<-chan Signal)
}

// Monitor configures a monitor for an individual subsystem.
// The subsystem requesting a monitor must receive from the returned channel
// in the subystem's main loop and close the [Signal.Alive] channel
// to indicate timely receipt of the signal.
//
// The name argument is used for reporting purposes,
// to indicate which subsystem is being monitored.
//
// Under normal operation, a value will arrive on the returned channel every
// interval + [-jitter/2, +jitter/2) duration; the jitter duration is uniformly distributed.
// However, it will also be possible in the future for an operator to request a status check.
//
// If the context is cancelled before the new monitor starts running,
// the returned channel is nil.
func (w *Watchdog) Monitor(ctx context.Context, cfg MonitorConfig) <-chan Signal {
	// Validate the config regardless of whether the watchdog is performing monitoring.
	if err := cfg.validate(); err != nil {
		panic(fmt.Errorf("(*Watchdog).Monitor: MonitorConfig is invalid: %w", err))
	}

	if w.monitorRequests == nil {
		// w is configured as a nop watchdog,
		// so we are not accepting monitor requests.
		return nil
	}

	req := monitorRequest{
		Cfg:  cfg,
		Resp: make(chan (<-chan Signal), 1),
	}

	ch, _ := gchan.ReqResp(
		ctx, w.log,
		w.monitorRequests, req,
		req.Resp,
		"requesting new monitor",
	)
	return ch
}

// Signal is the value returned by [*Watchdog.Monitor].
// The subsystem requesting the monitor must respond to the signal as soon as possible
// in order to prevent the watchdog from terminating the entire system.
type Signal struct {
	// Every signal will have a non-nil, non-closed Alive channel.
	Alive chan<- struct{}

	// In the future there will be at least one more channel here
	// for introspection of the monitored subsystem.
}
