package gwatchdog_test

import (
	"context"
	"testing"
	"time"

	"github.com/rollchains/gordian/gwatchdog"
	"github.com/rollchains/gordian/internal/gtest"
	"github.com/stretchr/testify/require"
)

func TestWatchdog_Terminate_normal(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w, wCtx := gwatchdog.NewWatchdog(ctx, gtest.NewLogger(t))
	defer w.Wait()
	defer cancel()

	// The returned context is of course not cancelled immediately.
	require.NoError(t, wCtx.Err())
	require.False(t, gwatchdog.IsTermination(wCtx))

	// Calling Terminate directly cancels the context.
	w.Terminate("testing purposes")
	require.Error(t, wCtx.Err())
	t.Logf("Context cause: %T %v", context.Cause(wCtx), context.Cause(wCtx))
	require.True(t, gwatchdog.IsTermination(wCtx))
	require.Equal(t, gwatchdog.ForcedTerminationError{
		Reason: "testing purposes",
	}, context.Cause(wCtx))

	// Calling a second time does not change the error.
	w.Terminate("again")
	require.Equal(t, gwatchdog.ForcedTerminationError{
		Reason: "testing purposes",
	}, context.Cause(wCtx))
}

func TestWatchdog_Terminate_afterParentCancel(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w, wCtx := gwatchdog.NewWatchdog(ctx, gtest.NewLogger(t))
	defer w.Wait()
	defer cancel()

	// If the parent is canceled first, and then terminate is called...
	cancel()
	w.Terminate("late")

	// The watchdog context is cancelled but does not match IsTermination.
	require.Error(t, wCtx.Err())
	require.False(t, gwatchdog.IsTermination(wCtx))
}

func TestWatchdog_monitor_notAcceptingSignalCausesTermination(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w, wCtx := gwatchdog.NewWatchdog(ctx, gtest.NewLogger(t))
	defer w.Wait()
	defer cancel()

	name := t.Name()
	cfg := gwatchdog.MonitorConfig{
		Name:     name,
		Interval: 100 * time.Microsecond, Jitter: 10 * time.Microsecond,

		// The response time is practically instant.
		ResponseTimeout: 50 * time.Microsecond,
	}
	_ = w.Monitor(ctx, cfg)

	// Sleep for a little more than the entire send and response timeout.
	time.Sleep(cfg.Interval + cfg.Jitter + cfg.ResponseTimeout + 2*time.Millisecond)

	require.Error(t, wCtx.Err())
	require.True(t, gwatchdog.IsTermination(wCtx))
}

func TestWatchdog_monitor_acceptingSignalWithoutRespondingCausesTermination(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w, wCtx := gwatchdog.NewWatchdog(ctx, gtest.NewLogger(t))
	defer w.Wait()
	defer cancel()

	name := t.Name()
	cfg := gwatchdog.MonitorConfig{
		Name:     name,
		Interval: 100 * time.Microsecond, Jitter: 10 * time.Microsecond,

		// The response time is short enough to reasonably sleep past.
		ResponseTimeout: time.Duration(gtest.ScaleMs(150)),
	}
	sigCh := w.Monitor(ctx, cfg)

	// Accept the signal successfully.
	_ = gtest.ReceiveSoon(t, sigCh)

	// But sleep for longer than the response timeout, without responding.
	gtest.Sleep(gtest.ScaleMs(160))

	require.Error(t, wCtx.Err())
	require.True(t, gwatchdog.IsTermination(wCtx))
}

func TestWatchdog_monitor_respondingOnTimeDoesNotCauseTermination(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w, wCtx := gwatchdog.NewWatchdog(ctx, gtest.NewLogger(t))
	defer w.Wait()
	defer cancel()

	name := t.Name()
	cfg := gwatchdog.MonitorConfig{
		Name:     name,
		Interval: 100 * time.Microsecond, Jitter: 10 * time.Microsecond,

		// The response time is short enough to reasonably sleep past.
		ResponseTimeout: time.Duration(gtest.ScaleMs(150)),
	}
	sigCh := w.Monitor(ctx, cfg)

	// Accept the signal successfully.
	sig := gtest.ReceiveSoon(t, sigCh)

	// Respond immediately.
	close(sig.Alive)

	// There is no error.
	require.NoError(t, wCtx.Err())

	// Upon sending the signal, the short interval timer starts again, so we should receive a new signal soon.
	sig = gtest.ReceiveSoon(t, sigCh)

	// We have a long time to respond.
	// But we only have to confirm that there is still no error.
	require.NoError(t, wCtx.Err())
}

func TestNopWatchdog_monitor(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w, wCtx := gwatchdog.NewNopWatchdog(ctx, gtest.NewLogger(t))
	defer w.Wait()
	defer cancel()

	name := t.Name()
	cfg := gwatchdog.MonitorConfig{
		// The config is still validated, so we have to provide valid values.
		Name:     name,
		Interval: 100 * time.Microsecond, Jitter: 10 * time.Microsecond,
		ResponseTimeout: time.Millisecond,
	}

	// Monitor returns a nil channel,
	// so it will never be chosen in a select statement.
	sigCh := w.Monitor(wCtx, cfg)
	require.Nil(t, sigCh)
}

func TestNopWatchdog_Terminate(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w, wCtx := gwatchdog.NewNopWatchdog(ctx, gtest.NewLogger(t))
	defer w.Wait()
	defer cancel()

	require.NoError(t, wCtx.Err())

	w.Terminate("testing")
	require.Error(t, wCtx.Err())
	require.True(t, gwatchdog.IsTermination(wCtx))
}
