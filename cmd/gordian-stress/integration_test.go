package main

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"sync"
	"testing"

	"github.com/rollchains/gordian/internal/gtest"
	"github.com/stretchr/testify/require"
)

func TestIntegration_startVals(t *testing.T) {
	t.Parallel()

	// Set up wait group first since deferred cancel will happen before deferred wg.Wait.
	var wg sync.WaitGroup
	defer wg.Wait()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	log := gtest.NewLogger(t)

	_, socketPath := runSeedCmd(ctx, log.With("cmd", "seed"), &wg)

	mustRunRegisterValidatorCmd(ctx, t, log.With("cmd", "rv"), socketPath, "val1", 1)
	mustRunRegisterValidatorCmd(ctx, t, log.With("cmd", "rv"), socketPath, "val2", 1)

	val1Cmd := runValidatorCmd(ctx, log.With("val", 1), &wg, socketPath, "val1")
	val2Cmd := runValidatorCmd(ctx, log.With("val", 2), &wg, socketPath, "val2")

	mustRunStartCmd(ctx, t, log.With("cmd", "start"), socketPath)

	// Validators are running...
	gtest.NotSending(t, val1Cmd.ErrCh)
	gtest.NotSending(t, val2Cmd.ErrCh)

	// Stop everything via context cancellation.
	cancel()

	_ = gtest.ReceiveSoon(t, val1Cmd.ErrCh)
	_ = gtest.ReceiveSoon(t, val2Cmd.ErrCh)
}

func TestIntegration_halt(t *testing.T) {
	t.Parallel()

	// Set up wait group first since deferred cancel will happen before deferred wg.Wait.
	var wg sync.WaitGroup
	defer wg.Wait()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	log := gtest.NewLogger(t)

	seedCmd, socketPath := runSeedCmd(ctx, log.With("cmd", "seed"), &wg)

	mustRunRegisterValidatorCmd(ctx, t, log.With("cmd", "rv"), socketPath, "val1", 1)
	mustRunRegisterValidatorCmd(ctx, t, log.With("cmd", "rv"), socketPath, "val2", 1)

	val1Cmd := runValidatorCmd(ctx, log.With("val", 1), &wg, socketPath, "val1")
	val2Cmd := runValidatorCmd(ctx, log.With("val", 2), &wg, socketPath, "val2")

	mustRunStartCmd(ctx, t, log.With("cmd", "start"), socketPath)

	// TODO: until we have a way to synchronize on the validators actually running,
	// we can't assert a clean shutdown.
	// Currently the shutdown happens so quickly after the start call
	// that the context is cancelled before the chain is fully initialized.
	mustRunHaltCmd(ctx, t, log.With("cmd", "halt"), socketPath)

	// Halt results in a clean shutdown for everything.
	require.Nil(t, gtest.ReceiveSoon(t, seedCmd.ErrCh))

	// It would be nice to assert there are nil,
	// but see the above TODO comment.
	_ = gtest.ReceiveSoon(t, val1Cmd.ErrCh)
	_ = gtest.ReceiveSoon(t, val2Cmd.ErrCh)
}

func runCmdSync(
	ctx context.Context,
	log *slog.Logger,
	args ...string,
) (outBuf, errBuf *bytes.Buffer, err error) {
	outBuf = new(bytes.Buffer)
	errBuf = new(bytes.Buffer)

	cmd := NewRootCmd(log)
	cmd.SetArgs(args)
	cmd.SetOutput(outBuf)
	cmd.SetErr(errBuf)

	err = cmd.ExecuteContext(ctx)
	return outBuf, errBuf, err
}

func runCmd(
	ctx context.Context,
	log *slog.Logger,
	wg *sync.WaitGroup,
	args ...string,
) *cmdFixture {
	cfx := &cmdFixture{
		ErrCh: make(chan error, 1),
	}

	cmd := NewRootCmd(log)
	cmd.SetArgs(args)
	cmd.SetOutput(&cfx.outBuf)
	cmd.SetErr(&cfx.errBuf)

	wg.Add(1)
	go func() {
		defer wg.Done()

		cfx.ErrCh <- cmd.ExecuteContext(ctx)
	}()

	return cfx
}

type cmdFixture struct {
	ErrCh          chan error
	outBuf, errBuf bytes.Buffer
}

func runSeedCmd(
	ctx context.Context,
	log *slog.Logger,
	wg *sync.WaitGroup,
) (cfx *cmdFixture, socketPath string) {
	f, err := os.CreateTemp("", "*.sock")
	if err != nil {
		panic(err)
	}

	socketPath = f.Name()

	if err := f.Close(); err != nil {
		panic(err)
	}
	if err := os.Remove(f.Name()); err != nil {
		panic(err)
	}

	cfx = runCmd(ctx, log, wg, "seed", socketPath)

	// It is possible the command's goroutine hasn't opened the socket yet,
	// so poll until it is available.
	for range 10 {
		if _, err := os.Stat(socketPath); err != nil {
			// Delay then check again.
			gtest.Sleep(gtest.ScaleMs(5))
			continue
		}

		// Stat-ed the file without error, so assume the socket is ready.
		return cfx, socketPath
	}

	// If we polled and never returned early, then the socket never opened.
	panic(fmt.Errorf("socket path %q was never ready", socketPath))
}

func mustRunRegisterValidatorCmd(
	ctx context.Context,
	t *testing.T,
	log *slog.Logger,
	socketPath, valPassphrase string,
	valPow uint64,
) {
	t.Helper()

	pow := strconv.FormatUint(valPow, 10)
	outBuf, errBuf, err := runCmdSync(ctx, log, "rv", socketPath, valPassphrase, pow)
	if err != nil {
		t.Logf("got error while running register-validator command: %v", err)

		t.Logf("command stdout:\n%s\n", outBuf)
		t.Logf("command stderr:\n%s\n", errBuf)
		t.FailNow()
	}
}

func runValidatorCmd(
	ctx context.Context,
	log *slog.Logger,
	wg *sync.WaitGroup,
	socketPath, valPassphrase string,
) *cmdFixture {
	return runCmd(ctx, log, wg, "validator", socketPath, valPassphrase)
}

func mustRunStartCmd(
	ctx context.Context,
	t *testing.T,
	log *slog.Logger,
	socketPath string,
) {
	t.Helper()

	outBuf, errBuf, err := runCmdSync(ctx, log, "start", socketPath)
	if err != nil {
		t.Logf("got error while running start command: %v", err)

		t.Logf("command stdout:\n%s\n", outBuf)
		t.Logf("command stderr:\n%s\n", errBuf)
		t.FailNow()
	}
}

func mustRunHaltCmd(
	ctx context.Context,
	t *testing.T,
	log *slog.Logger,
	socketPath string,
) {
	t.Helper()

	outBuf, errBuf, err := runCmdSync(ctx, log, "halt", socketPath)
	if err != nil {
		t.Logf("got error while running halt command: %v", err)

		t.Logf("command stdout:\n%s\n", outBuf)
		t.Logf("command stderr:\n%s\n", errBuf)
		t.FailNow()
	}
}
