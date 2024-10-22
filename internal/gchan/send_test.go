package gchan_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/gordian-engine/gordian/internal/gchan"
	"github.com/gordian-engine/gordian/internal/gtest"
	"github.com/stretchr/testify/require"
)

func TestSendC_contextCanceled(t *testing.T) {
	t.Parallel()

	res := make(chan bool, 1)

	// Send to a nil channel blocks forever.
	var blockedOut chan int

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var buf bytes.Buffer
	log := slog.New(
		slog.NewJSONHandler(&buf, nil),
	)

	running := make(chan struct{})
	go func() {
		close(running)
		res <- gchan.SendC(ctx, log, blockedOut, 1, "running test")
	}()

	// Ensure the goroutine is running.
	_ = gtest.ReceiveSoon(t, running)

	// Now, nothing should be sent on res yet.
	select {
	case <-res:
		t.Fatal("Result sent before it should have been")
	case <-time.After(20 * time.Millisecond):
		// Okay.
	}

	// Canceling the context should cause the result to send ~immediately.
	cancel()
	require.False(t, gtest.ReceiveSoon(t, res))

	// And the correct message is logged at info level.
	var m map[string]string
	require.NoError(t, json.Unmarshal(buf.Bytes(), &m))

	require.Equal(t, "INFO", m["level"])
	require.Equal(t, "Context canceled while running test", m["msg"])
	require.Equal(t, context.Cause(ctx).Error(), m["cause"])
}

func TestSendC_valueSent(t *testing.T) {
	t.Parallel()

	res := make(chan bool, 1)

	out := make(chan int) // Unbuffered so we can control when the test proceeds.

	ctx := context.Background()

	var buf bytes.Buffer
	log := slog.New(
		slog.NewJSONHandler(&buf, nil),
	)

	running := make(chan struct{})
	go func() {
		close(running)
		res <- gchan.SendC(ctx, log, out, 1, "running test")
	}()

	// Ensure the goroutine is running.
	_ = gtest.ReceiveSoon(t, running)

	// Now, nothing should be sent on res yet.
	select {
	case <-res:
		t.Fatal("Result sent before it should have been")
	case <-time.After(20 * time.Millisecond):
		// Okay.
	}

	// Now read from the out channel to unblock the goroutine.
	outVal := gtest.ReceiveSoon(t, out)
	require.Equal(t, 1, outVal)

	// And the sent result was true since the context was not canceled.
	require.True(t, gtest.ReceiveSoon(t, res))

	// Finally, nothing was logged.
	require.Zero(t, buf.Len())
}

func TestRecvC_contextCanceled(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var buf bytes.Buffer
	log := slog.New(
		slog.NewJSONHandler(&buf, nil),
	)

	in := make(chan int, 1)
	resVal := make(chan int, 1)
	resReceived := make(chan bool, 1)

	running := make(chan struct{})
	go func() {
		close(running)
		val, sent := gchan.RecvC(ctx, log, in, "running test")
		resVal <- val
		resReceived <- sent
	}()

	// Ensure the goroutine is running.
	_ = gtest.ReceiveSoon(t, running)

	// No result sent immediately.
	select {
	case <-resVal:
		t.Fatal("Result sent before it should have been")
	case <-resReceived:
		t.Fatal("Result sent before it should have been")
	case <-time.After(20 * time.Millisecond):
		// Okay.
	}

	// Canceling the context should cause the result to send ~immediately.
	cancel()
	require.Zero(t, gtest.ReceiveSoon(t, resVal)) // Zero value sent.
	require.False(t, gtest.ReceiveSoon(t, resReceived))

	// And the correct message is logged at info level.
	var m map[string]string
	require.NoError(t, json.Unmarshal(buf.Bytes(), &m))

	require.Equal(t, "INFO", m["level"])
	require.Equal(t, "Context canceled while running test", m["msg"])
	require.Equal(t, context.Cause(ctx).Error(), m["cause"])
}

func TestRecvC_valueReceived(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	var buf bytes.Buffer
	log := slog.New(
		slog.NewJSONHandler(&buf, nil),
	)

	in := make(chan int, 1)
	resVal := make(chan int, 1)
	resReceived := make(chan bool, 1)

	running := make(chan struct{})
	go func() {
		close(running)
		val, sent := gchan.RecvC(ctx, log, in, "running test")
		resVal <- val
		resReceived <- sent
	}()

	// Ensure the goroutine is running.
	_ = gtest.ReceiveSoon(t, running)

	// No result sent immediately.
	select {
	case <-resVal:
		t.Fatal("Result sent before it should have been")
	case <-resReceived:
		t.Fatal("Result sent before it should have been")
	case <-time.After(20 * time.Millisecond):
		// Okay.
	}

	// Sending the value on the in channel causes the result to send ~immediately.
	in <- 1
	require.Equal(t, 1, gtest.ReceiveSoon(t, resVal))
	require.True(t, gtest.ReceiveSoon(t, resReceived))

	// And nothing was logged.
	require.Zero(t, buf.Len())
}
