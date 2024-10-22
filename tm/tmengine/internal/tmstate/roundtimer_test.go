package tmstate_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/gordian-engine/gordian/internal/gtest"
	"github.com/gordian-engine/gordian/tm/tmengine"
	"github.com/gordian-engine/gordian/tm/tmengine/internal/tmstate"
	"github.com/stretchr/testify/require"
)

func TestStandardRoundTimer(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping due to short mode")
	}

	ms25 := gtest.ScaleMs(25)
	s25 := tmengine.LinearTimeoutStrategy{
		ProposalBase:       time.Duration(ms25),
		PrevoteDelayBase:   time.Duration(ms25),
		PrecommitDelayBase: time.Duration(ms25),
		CommitWaitBase:     time.Duration(ms25),
	}

	sShort := tmengine.LinearTimeoutStrategy{
		ProposalBase:       time.Millisecond,
		PrevoteDelayBase:   time.Millisecond,
		PrecommitDelayBase: time.Millisecond,
		CommitWaitBase:     time.Millisecond,
	}

	t.Run("ProposalTimer", func(t *testing.T) {
		t.Run("channel closed upon elapse", func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			rt := tmstate.NewStandardRoundTimer(ctx, sShort)
			defer rt.Wait()
			defer cancel()

			ch, tCancel := rt.ProposalTimer(ctx, 1, 0)
			defer tCancel()

			_ = gtest.ReceiveOrTimeout(t, ch, gtest.ScaleMs(50))
		})

		t.Run("channel not closed upon cancel", func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			rt := tmstate.NewStandardRoundTimer(ctx, s25)
			defer rt.Wait()
			defer cancel()

			ch, tCancel := rt.ProposalTimer(ctx, 1, 0)
			tCancel() // Immediate cancel.

			// Sleep longer than what would elapse.
			gtest.Sleep(gtest.ScaleMs(25 + 5))

			gtest.NotSending(t, ch)
		})
	})

	t.Run("PrevoteDelayTimer", func(t *testing.T) {
		t.Run("channel closed upon elapse", func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			rt := tmstate.NewStandardRoundTimer(ctx, sShort)
			defer rt.Wait()
			defer cancel()

			ch, tCancel := rt.PrevoteDelayTimer(ctx, 1, 0)
			defer tCancel()

			_ = gtest.ReceiveOrTimeout(t, ch, gtest.ScaleMs(50))
		})

		t.Run("channel not closed upon cancel", func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			rt := tmstate.NewStandardRoundTimer(ctx, s25)
			defer rt.Wait()
			defer cancel()

			ch, tCancel := rt.PrevoteDelayTimer(ctx, 1, 0)
			tCancel() // Immediate cancel.

			// Sleep longer than what would elapse.
			gtest.Sleep(gtest.ScaleMs(25 + 5))

			gtest.NotSending(t, ch)
		})
	})

	t.Run("PrecommitDelayTimer", func(t *testing.T) {
		t.Run("channel closed upon elapse", func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			rt := tmstate.NewStandardRoundTimer(ctx, sShort)
			defer rt.Wait()
			defer cancel()

			ch, tCancel := rt.PrecommitDelayTimer(ctx, 1, 0)
			defer tCancel()

			_ = gtest.ReceiveOrTimeout(t, ch, gtest.ScaleMs(50))
		})

		t.Run("channel not closed upon cancel", func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			rt := tmstate.NewStandardRoundTimer(ctx, s25)
			defer rt.Wait()
			defer cancel()

			ch, tCancel := rt.PrecommitDelayTimer(ctx, 1, 0)
			tCancel() // Immediate cancel.

			// Sleep longer than what would elapse.
			gtest.Sleep(gtest.ScaleMs(25 + 5))

			gtest.NotSending(t, ch)
		})
	})

	t.Run("CommitWaitTimer", func(t *testing.T) {
		t.Run("channel closed upon elapse", func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			rt := tmstate.NewStandardRoundTimer(ctx, sShort)
			defer rt.Wait()
			defer cancel()

			ch, tCancel := rt.CommitWaitTimer(ctx, 1, 0)
			defer tCancel()

			_ = gtest.ReceiveOrTimeout(t, ch, gtest.ScaleMs(50))
		})

		t.Run("channel not closed upon cancel", func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			rt := tmstate.NewStandardRoundTimer(ctx, s25)
			defer rt.Wait()
			defer cancel()

			ch, tCancel := rt.CommitWaitTimer(ctx, 1, 0)
			tCancel() // Immediate cancel.

			// Sleep longer than what would elapse.
			gtest.Sleep(gtest.ScaleMs(25 + 5))

			gtest.NotSending(t, ch)
		})
	})

	t.Run("return values when context is cancelled", func(t *testing.T) {
		t.Parallel()

		mainTestDone := make(chan struct{})
		defer close(mainTestDone)

		// In case any of these fail, build in a timeout
		// so that the test operator does not need to debug a hanging test.
		go func() {
			timer := time.NewTimer(3 * time.Second) // Does not need scaled.
			defer timer.Stop()
			select {
			case <-mainTestDone:
				// Okay.
			case <-timer.C:
				panic(fmt.Errorf("test %q not completed within hardcoded 3s timeout", t.Name()))
			}
		}()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		rt := tmstate.NewStandardRoundTimer(ctx, sShort)
		defer rt.Wait()
		cancel() // Not deferred -- immediately cancelled here.

		ch, tCancel := rt.ProposalTimer(ctx, 1, 0)
		require.NotNil(t, tCancel)
		require.NotPanics(t, tCancel)
		require.Nil(t, ch)

		ch, tCancel = rt.PrevoteDelayTimer(ctx, 1, 0)
		require.NotNil(t, tCancel)
		require.NotPanics(t, tCancel)
		require.Nil(t, ch)

		ch, tCancel = rt.PrecommitDelayTimer(ctx, 1, 0)
		require.NotNil(t, tCancel)
		require.NotPanics(t, tCancel)
		require.Nil(t, ch)

		ch, tCancel = rt.CommitWaitTimer(ctx, 1, 0)
		require.NotNil(t, tCancel)
		require.NotPanics(t, tCancel)
		require.Nil(t, ch)
	})

	t.Run("multiple calls", func(t *testing.T) {
		t.Parallel()

		mainTestDone := make(chan struct{})
		defer close(mainTestDone)

		// In case any of these fail, build in a timeout
		// so that the test operator does not need to debug a hanging test.
		go func() {
			timer := time.NewTimer(3 * time.Second) // Does not need scaled.
			defer timer.Stop()
			select {
			case <-mainTestDone:
				// Okay.
			case <-timer.C:
				panic(fmt.Errorf("test %q not completed within hardcoded 3s timeout", t.Name()))
			}
		}()

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		rt := tmstate.NewStandardRoundTimer(ctx, sShort)
		defer rt.Wait()
		defer cancel()

		ch, tCancel := rt.ProposalTimer(ctx, 1, 0)
		defer tCancel()
		_ = gtest.ReceiveSoon(t, ch)

		ch, tCancel = rt.PrevoteDelayTimer(ctx, 1, 0)
		defer tCancel()
		_ = gtest.ReceiveSoon(t, ch)

		ch, tCancel = rt.PrecommitDelayTimer(ctx, 1, 0)
		defer tCancel()
		_ = gtest.ReceiveSoon(t, ch)

		ch, tCancel = rt.CommitWaitTimer(ctx, 1, 0)
		defer tCancel()
		_ = gtest.ReceiveSoon(t, ch)
	})
}
