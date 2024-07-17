package main_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"slices"
	"testing"

	"cosmossdk.io/core/transaction"
	simdcmd "cosmossdk.io/simapp/v2/simdv2/cmd"
	svrcmd "github.com/cosmos/cosmos-sdk/server/cmd"
	"github.com/rollchains/gordian/gcosmos/internal/gci"
	"github.com/rollchains/gordian/internal/gtest"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

type ChainConfig struct {
	ID string

	NVals int
}

type Chain struct {
	RootCmds []CmdEnv
}

func ConfigureChain(t *testing.T, ctx context.Context, cfg ChainConfig) Chain {
	t.Helper()

	if cfg.ID == "" {
		panic("test setup issue: ChainConfig.ID must not be empty")
	}

	if cfg.NVals <= 0 {
		panic("test setup issue: ChainConfig.NVals must be greater than zero")
	}

	rootCmds := make([]CmdEnv, cfg.NVals)
	keyAddresses := make([]string, cfg.NVals)

	for i := range cfg.NVals {
		// Each validator gets its own command environment
		// and therefore its own home directory.
		e := NewRootCmd(t)
		rootCmds[i] = e

		// Each validator needs its own initialized config and genesis.
		valName := fmt.Sprintf("val%d", i)
		e.Run("init", valName, "--chain-id", cfg.ID).NoError(t)

		// Each validator needs its own key.
		res := e.Run("keys", "add", valName, "--output=json")
		res.NoError(t)

		// Collect the addresses from the JSON output.
		var keyAddOutput struct {
			Address string
		}
		require.NoError(t, json.Unmarshal(res.Stdout.Bytes(), &keyAddOutput))
		keyAddresses[i] = keyAddOutput.Address
	}

	// Add each key as a genesis account on the first validator's environment.
	for _, a := range keyAddresses {
		rootCmds[0].Run(
			"genesis", "add-genesis-account",
			a, "10000000000stake",
		).NoError(t)
	}

	// Add a gentx for every validator, to a temporary gentx directory.
	gentxDir := t.TempDir()
	for i, e := range rootCmds {
		vs := fmt.Sprintf("val%d", i)
		if i > 0 {
			// The first validator has every genesis account already,
			// but each validator needs its own address as a genesis account
			// in order to do a gentx.
			e.Run(
				"genesis", "add-genesis-account",
				vs, "10000000000stake",
			).NoError(t)
		}
		e.Run(
			"genesis", "gentx",
			"--chain-id", cfg.ID,
			"--output-document", filepath.Join(gentxDir, vs+".gentx.json"),
			vs, "10000000000stake",
		).NoError(t)
	}

	// Collect gentxs on the first validator.
	rootCmds[0].Run(
		"genesis", "collect-gentxs", "--gentx-dir", gentxDir,
	).NoError(t)

	// The gRPC server defaults to listening on port 9090,
	// and the test will fail if the gRPC server cannot bind,
	// so just use an anonymous port.
	// We are not disabling the server since we expect to need it for later tests anyway.
	rootCmds[0].Run("config", "set", "app", "grpc.address", "localhost:0", "--skip-validate").NoError(t)


	return Chain{
		RootCmds: rootCmds,
	}
}

func NewRootCmd(
	t *testing.T,
) CmdEnv {
	t.Helper()

	return CmdEnv{
		log:     gtest.NewLogger(t),
		homeDir: t.TempDir(),
	}
}

type CmdEnv struct {
	log     *slog.Logger
	homeDir string
}

func (e CmdEnv) Run(args ...string) RunResult {
	return e.RunWithInput(nil, args...)
}

func (e CmdEnv) RunC(ctx context.Context, args ...string) RunResult {
	return e.RunWithInputC(ctx, nil, args...)
}

func (e CmdEnv) RunWithInput(in io.Reader, args ...string) RunResult {
	return e.RunWithInputC(context.Background(), in, args...)
}

func (e CmdEnv) RunWithInputC(ctx context.Context, in io.Reader, args ...string) RunResult {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var cmd *cobra.Command

	// Compile-time flag declared near top of this file.
	if runCometInsteadOfGordian {
		cmd = simdcmd.NewRootCmd[transaction.Tx]()
	} else {
		cmd = gci.NewSimdRootCmdWithGordian(ctx, e.log)
	}

	// Just add the home flag directly instead of
	// relying on the comet CLI integration in the SDK.
	// Might be brittle, but should also be a little simpler.
	cmd.PersistentFlags().StringP("home", "", e.homeDir, "default test dir home, do not change")

	ctx = svrcmd.CreateExecuteContext(ctx)

	args = append(
		slices.Clone(args),
		// Putting --home before the args would probably work,
		// but put --home at the end to be a little more sure
		// that it won't get ignored due to being parsed before the subcommand name.
		"--home", e.homeDir,
	)
	cmd.SetArgs(args)

	var res RunResult
	cmd.SetOut(&res.Stdout)
	cmd.SetErr(&res.Stderr)
	cmd.SetIn(in)

	res.Err = cmd.ExecuteContext(ctx)

	return res
}

type RunResult struct {
	Stdout, Stderr bytes.Buffer
	Err            error
}

func (r RunResult) NoError(t *testing.T) {
	t.Helper()

	require.NoErrorf(t, r.Err, "OUT: %s\n\nERR: %s", r.Stdout.String(), r.Stderr.String())
}