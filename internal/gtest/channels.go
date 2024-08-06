package gtest

import (
	"time"
)

// TestingFatalHelper is a subset of [testing.TB] to satisfy the requirements of
// [ReceiveOrTimeout] and [SendOrTimeout],
// and to allow those helpers to themselves be easily tested.
type TestingFatalHelper interface {
	Helper()

	Fatalf(format string, args ...any)
}

// ReceiveSoon attempts to receive a value from ch.
// If the receive is blocked for a reasonable default timeout, tb.Fatal is called.
func ReceiveSoon[T any](tb TestingFatalHelper, ch <-chan T) T {
	tb.Helper()
	return ReceiveOrTimeout(tb, ch, ScaleMs(100))
}

// ReceiveOrTimeout attempts to receive a value from ch.
// If the value cannot be received within the given timeout, tb.Fatal is called.
// Use [ScaleMs] to produce the ScaledDuration value;
// this offers flexibility for slower machines without modifying tests.
//
// Most tests should use [ReceiveSoon]; ReceiveOrTimeout should be reserved for exceptional cases.
func ReceiveOrTimeout[T any](tb TestingFatalHelper, ch <-chan T, timeout ScaledDuration) T {
	tb.Helper()

	if ch == nil {
		tb.Fatalf("immediate failure to avoid blocking receive from nil channel %T %v", ch, ch)
		panic("unreachable")
	}

	timer := time.NewTimer(time.Duration(timeout))
	defer timer.Stop()

	select {
	case <-timer.C:
		tb.Fatalf(
			"timed out while blocked receiving from channel %T %v; if this is flaky on only one machine, set the environment variable GORDIAN_TEST_TIME_FACTOR to a value greater than the current value of %d",
			ch, ch, TimeFactor,
		)
		// t.Fatalf would typically stop the testing goroutine,
		// but since we are mocking tb in tests,
		// we panic here, also to avoid a return value.
		panic("unreachable")
	case x := <-ch:
		return x
	}
}

// SendSoon attempts to send x to ch.
// If the send is blocked for a reasonable default timeout, tb.Fatal is called.
func SendSoon[T any](tb TestingFatalHelper, ch chan<- T, x T) {
	tb.Helper()
	SendOrTimeout(tb, ch, x, ScaleMs(100))
}

// SendOrTimeout attempts to send x to ch.
// If the send is blocked for the entire timeout, tb.Fatal is called.
// Use [ScaleMs] to produce the ScaledDuration value;
// this offers flexibility for slower machines without modifying tests.
//
// Most tests should use [SendSoon]; SendOrTimeout should be reserved for exceptional cases.
func SendOrTimeout[T any](tb TestingFatalHelper, ch chan<- T, x T, timeout ScaledDuration) {
	tb.Helper()

	if ch == nil {
		tb.Fatalf("immediate failure to avoid blocking send to nil channel %T %v", ch, ch)
		panic("unreachable")
	}

	timer := time.NewTimer(time.Duration(timeout))
	defer timer.Stop()

	select {
	case <-timer.C:
		tb.Fatalf(
			"timed out while blocked sending to channel %T %v; if this is flaky on only one machine, set the environment variable GORDIAN_TEST_TIME_FACTOR to a value greater than the current value of %d",
			ch, ch, TimeFactor,
		)
		panic("unreachable")
	case ch <- x:
		// Okay.
	}
}

// NotSending checks if a value is ready to be read from ch.
// If a value is available, tb.Fatal is called, and the received value is logged.
func NotSending[T any](tb TestingFatalHelper, ch <-chan T) {
	tb.Helper()

	if ch == nil {
		tb.Fatalf("immediate failure to check that a nil channel is not sending (%T %v)", ch, ch)
		panic("unreachable")
	}

	select {
	case x := <-ch:
		tb.Fatalf("no value should have been sent on channel %T %v; got %v", ch, ch, x)
	default:
		// Okay.
	}
}

// IsSending checks that a value is immediately ready to be ready from ch.
// If the value cannot be immediately received, tb.Fatal is called.
func IsSending[T any](tb TestingFatalHelper, ch <-chan T) T {
	tb.Helper()

	if ch == nil {
		tb.Fatalf("a nil channel will never send a value (%T %v)", ch, ch)
		panic("unreachable")
	}

	select {
	case x := <-ch:
		return x
	default:
		tb.Fatalf("expected a value to be immediately sent on channel %T %v, but no value could be received", ch, ch)
		panic("unreachable")
	}
}

// NotSendingSoon asserts that a read from ch is blocked for a reasonable, short duration.
//
// If there are any other synchronization events available, [NotSending] should be preferred,
// because this will block the test for a short duration.
func NotSendingSoon[T any](tb TestingFatalHelper, ch <-chan T) {
	tb.Helper()

	if ch == nil {
		tb.Fatalf("immediate failure to check that a nil channel is not sending (%T %v)", ch, ch)
		panic("unreachable")
	}

	timer := time.NewTimer(time.Duration(ScaleMs(75)))
	defer timer.Stop()

	select {
	case <-timer.C:
		// Okay.
	case x := <-ch:
		tb.Fatalf(
			"received value %v on channel %T %v, when it was expected not to send any values",
			x, ch, ch,
		)
		panic("unreachable")
	}
}
