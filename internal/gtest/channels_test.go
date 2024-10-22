package gtest_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/gordian-engine/gordian/internal/gtest"
	"github.com/stretchr/testify/require"
)

type fatalHelper struct {
	HelperCalled bool
	FatalMessage string
}

func (h *fatalHelper) Helper() {
	h.HelperCalled = true
}

func (h *fatalHelper) Fatalf(format string, args ...any) {
	h.FatalMessage = fmt.Sprintf(format, args...)
}

func TestReceiveOrTimeout(t *testing.T) {
	t.Run("receive within time", func(t *testing.T) {
		t.Parallel()

		ch := make(chan int)

		go func() {
			time.Sleep(5 * time.Millisecond)
			ch <- 1
		}()

		fh := new(fatalHelper)

		n := gtest.ReceiveOrTimeout(fh, ch, gtest.ScaleMs(1000))

		require.Equal(t, n, 1)

		require.True(t, fh.HelperCalled)
		require.Empty(t, fh.FatalMessage)
	})

	t.Run("timeout", func(t *testing.T) {
		t.Parallel()

		ch := make(chan string)

		fh := new(fatalHelper)

		const ms = 5
		before := time.Now()
		require.Panics(t, func() {
			_ = gtest.ReceiveOrTimeout(fh, ch, gtest.ScaleMs(ms))
		})
		after := time.Now()

		require.GreaterOrEqual(t, after.Sub(before), ms*time.Millisecond)

		require.True(t, fh.HelperCalled)
		require.NotEmpty(t, fh.FatalMessage)
	})

	t.Run("fatal on nil channel", func(t *testing.T) {
		t.Parallel()

		var ch chan float64

		fh := new(fatalHelper)

		require.Panics(t, func() {
			// Excessively long timeout that shouldn't be run in test.
			// The helper should fatal/panic immediately on nil channel.
			_ = gtest.ReceiveOrTimeout(fh, ch, gtest.ScaleMs(1_000_000_000))
		})

		require.True(t, fh.HelperCalled)
		require.NotEmpty(t, fh.FatalMessage)
	})
}

func TestSendOrTimeout(t *testing.T) {
	t.Run("send within time", func(t *testing.T) {
		t.Parallel()

		// Unbuffered.
		ch := make(chan int)
		ready := make(chan struct{})

		var got int
		go func() {
			time.Sleep(5 * time.Millisecond)
			got = <-ch
			close(ready)
		}()

		fh := new(fatalHelper)

		require.NotPanics(t, func() {
			gtest.SendOrTimeout(fh, ch, 1, gtest.ScaleMs(1000))
		})

		<-ready

		require.Equal(t, got, 1)

		require.True(t, fh.HelperCalled)
		require.Empty(t, fh.FatalMessage)
	})

	t.Run("timeout", func(t *testing.T) {
		t.Parallel()

		ch := make(chan string)

		fh := new(fatalHelper)

		const ms = 5
		before := time.Now()
		require.Panics(t, func() {
			gtest.SendOrTimeout(fh, ch, "", gtest.ScaleMs(ms))
		})
		after := time.Now()

		require.GreaterOrEqual(t, after.Sub(before), ms*time.Millisecond)

		require.True(t, fh.HelperCalled)
		require.NotEmpty(t, fh.FatalMessage)
	})

	t.Run("fatal on nil channel", func(t *testing.T) {
		t.Parallel()

		var ch chan float64

		fh := new(fatalHelper)

		require.Panics(t, func() {
			// Excessively long timeout that shouldn't be run in test.
			// The helper should fatal/panic immediately on nil channel.
			gtest.SendOrTimeout(fh, ch, 0, gtest.ScaleMs(1_000_000_000))
		})

		require.True(t, fh.HelperCalled)
		require.NotEmpty(t, fh.FatalMessage)
	})
}
