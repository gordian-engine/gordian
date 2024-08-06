package main_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"cosmossdk.io/core/transaction"
	simdcmd "cosmossdk.io/simapp/v2/simdv2/cmd"
	svrcmd "github.com/cosmos/cosmos-sdk/server/cmd"
	"github.com/rollchains/gordian/gcosmos/internal/gci"
	"github.com/rollchains/gordian/internal/gtest"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

var FixedMnemonics = [...]string{
	"abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon art",
	"ability ability ability ability ability ability ability ability ability ability ability ability ability ability ability ability ability ability ability ability ability ability ability able",
}

type ChainConfig struct {
	ID string

	NVals int

	// Affects only the validators.
	StakeStrategy StakeStrategy

	// How many "fixed accounts" to create.
	// The fixed accounts are created with mnemonics from the FixedMnemonics array.
	NFixedAccounts int
}

type StakeStrategy func(idx int) string

func ConstantStakeStrategy(amount uint64) StakeStrategy {
	stake := fmt.Sprintf("%dstake", amount)
	return func(int) string {
		return stake
	}
}

func DecrementingStakeStrategy(initialCount uint64) StakeStrategy {
	return func(idx int) string {
		return fmt.Sprintf("%dstake", initialCount-uint64(idx))
	}
}

type Chain struct {
	RootCmds []CmdEnv

	FixedAddresses []string
}

func ConfigureChain(t *testing.T, ctx context.Context, cfg ChainConfig) Chain {
	t.Helper()

	if cfg.ID == "" {
		panic("test setup issue: ChainConfig.ID must not be empty")
	}

	if cfg.NVals <= 0 {
		panic("test setup issue: ChainConfig.NVals must be greater than zero")
	}

	if cfg.NFixedAccounts > len(FixedMnemonics) {
		panic(fmt.Errorf("got NFixedAccounts=%d but can only support up to %d (unless FixedMnemonics is updated)", cfg.NFixedAccounts, len(FixedMnemonics)))
	}

	rootCmds := make([]CmdEnv, cfg.NVals)
	keyAddresses := make([]string, cfg.NVals)

	log := gtest.NewLogger(t)

	for i := range cfg.NVals {
		// Each validator gets its own command environment
		// and therefore its own home directory.
		e := NewRootCmd(t, log.With("val_idx", i))
		rootCmds[i] = e

		// Each validator needs its own initialized config and genesis.
		valName := fmt.Sprintf("val%d", i)
		e.Run("init", valName, "--chain-id", cfg.ID).NoError(t)

		// Each validator needs its own key.
		res := e.Run("keys", "add", valName, "--output=json")
		res.NoError(t)

		// Collect the addresses from the JSON output.
		var keyOut keyAddOutput
		require.NoError(t, json.Unmarshal(res.Stdout.Bytes(), &keyOut))
		keyAddresses[i] = keyOut.Address
	}

	// Add each key as a genesis account on the first validator's environment.
	for i, a := range keyAddresses {
		rootCmds[0].Run(
			"genesis", "add-genesis-account",
			a, cfg.StakeStrategy(i),
		).NoError(t)
	}

	var mnemonicDir string
	var fixedAddresses []string
	if cfg.NFixedAccounts > 0 {
		mnemonicDir = t.TempDir()
		fixedAddresses = make([]string, cfg.NFixedAccounts)
	}
	for i := range cfg.NFixedAccounts {
		m := FixedMnemonics[i]
		mPath := filepath.Join(mnemonicDir, fmt.Sprintf("%d.txt", i))
		require.NoError(t, os.WriteFile(mPath, []byte(m), 0o600))

		keyName := fmt.Sprintf("fixed%d", i)

		// Teach each validator environment about the fixed address keys.
		for j, e := range rootCmds {
			res := e.RunWithInput(
				strings.NewReader(m),
				"keys", "add", keyName, "--output=json", "--recover", "--source", mPath,
			)

			res.NoError(t)

			if j == 0 {
				var keyOut keyAddOutput
				require.NoError(t, json.Unmarshal(res.Stdout.Bytes(), &keyOut))
				fixedAddresses[i] = keyOut.Address
			}
		}

		// And add each fixed address as a genesis account.
		// This only needs to happen on the first validator.
		rootCmds[0].Run(
			"genesis", "add-genesis-account",
			keyName, "10000stake",
		).NoError(t)
	}

	// Add a gentx for every validator, to a temporary gentx directory.
	gentxDir := t.TempDir()
	for i, e := range rootCmds {
		vs := fmt.Sprintf("val%d", i)
		stake := cfg.StakeStrategy(i)
		if i > 0 {
			// The first validator has every genesis account already,
			// but each validator needs its own address as a genesis account
			// in order to do a gentx.
			e.Run(
				"genesis", "add-genesis-account",
				vs, stake,
			).NoError(t)
		}
		e.Run(
			"genesis", "gentx",
			"--chain-id", cfg.ID,
			"--output-document", filepath.Join(gentxDir, vs+".gentx.json"),
			vs, stake,
		).NoError(t)
	}

	// Collect the gentxs on the first validator, then copy it to the other validators.
	rootCmds[0].Run(
		"genesis", "collect-gentxs", "--gentx-dir", gentxDir,
	).NoError(t)

	writers := make([]io.Writer, cfg.NVals-1)
	for i := 1; i < cfg.NVals; i++ {
		gPath := filepath.Join(rootCmds[i].homeDir, "config", "genesis.json")
		f, err := os.Create(gPath)
		require.NoError(t, err)
		defer f.Close() // Yes, we are deferring a close in a loop here.
		writers[i-1] = f
	}

	origGF, err := os.Open(filepath.Join(rootCmds[0].homeDir, "config", "genesis.json"))
	require.NoError(t, err)
	defer origGF.Close()

	_, err = io.Copy(io.MultiWriter(writers...), origGF)
	require.NoError(t, err)

	// Final configuration on every validator.
	for _, c := range rootCmds {
		// The gRPC server defaults to listening on port 9090,
		// and the test will fail if the gRPC server cannot bind,
		// so just use an anonymous port.
		// We are not disabling the server since we expect to need it for later tests anyway.
		c.Run("config", "set", "app", "grpc.address", "localhost:0", "--skip-validate").NoError(t)
	}

	return Chain{
		RootCmds: rootCmds,

		FixedAddresses: fixedAddresses,
	}
}

type ChainAddresses struct {
	HTTP []string
	// TODO: this can also include GRPC when we need it.
}

func (c Chain) Start(t *testing.T, ctx context.Context, nVals int) ChainAddresses {
	t.Helper()

	addrDir := t.TempDir()
	var seedAddrs string
	if nVals > 1 {
		// We only need to start a seed node when there are multiple validators.
		p2pSeedPath := filepath.Join(addrDir, "p2p.seed.txt")

		seedDone := make(chan struct{})
		go func() {
			defer close(seedDone)
			// Just discard the run result.
			// If the seed fails to start, we will have obvious failures later.
			_ = c.RootCmds[0].RunC(ctx, "gordian", "seed", p2pSeedPath)
		}()
		t.Cleanup(func() {
			<-seedDone
		})

		// The seed should start up relatively quickly.
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			b, err := os.ReadFile(p2pSeedPath)
			if err != nil {
				if os.IsNotExist(err) {
					time.Sleep(20 * time.Millisecond)
					continue
				}
				t.Fatalf("failed to read p2p seed path file %q: %v", p2pSeedPath, err)
			}

			if !bytes.HasSuffix(b, []byte("\n")) {
				// Very unlikely partial write to file;
				// delay and try again.
				time.Sleep(20 * time.Millisecond)
				continue
			}

			// Otherwise it does end with a \n.
			// We will assume that we have sufficient addresses to connect to,
			// if there is at least one entry.
			seedAddrs = strings.TrimSuffix(string(b), "\n")
		}
	}

	// Now we start the validators.
	httpAddrFiles := make([]string, nVals)

	var wg sync.WaitGroup
	wg.Add(nVals)
	for i := range nVals {
		httpAddrFiles[i] = filepath.Join(addrDir, fmt.Sprintf("http_addr_%d.txt", i))

		go func(i int) {
			defer wg.Done()

			startCmd := []string{"start"}
			if !runCometInsteadOfGordian {
				// Then include the HTTP server flags.
				startCmd = append(
					startCmd,
					"--g-http-addr", "127.0.0.1:0",
					"--g-http-addr-file", httpAddrFiles[i],
				)

				if nVals > 1 {
					// Only include the seed addresses when there are multiple validators.
					startCmd = append(startCmd, "--g-seed-addrs", seedAddrs)
				}
			}

			_ = c.RootCmds[i].RunC(ctx, startCmd...)
		}(i)
	}
	t.Cleanup(wg.Wait)

	ca := ChainAddresses{
		HTTP: make([]string, nVals),
	}

	// Now all the goroutines to start the servers should be running.
	// Gather their reported HTTP addresses.
	for i := range nVals {
		if runCometInsteadOfGordian {
			// Nothing to check in this mode.
			break
		}

		var httpAddr string
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			a, err := os.ReadFile(httpAddrFiles[i])
			if err != nil {
				// Swallow the error and delay.
				time.Sleep(25 * time.Millisecond)
				continue
			}
			if !bytes.HasSuffix(a, []byte("\n")) {
				// Very unlikely incomplete write/read.
				time.Sleep(25 * time.Millisecond)
				continue
			}

			httpAddr = strings.TrimSuffix(string(a), "\n")
			break
		}

		if httpAddr == "" {
			t.Fatalf("did not read http address from %s in time", httpAddrFiles[i])
		}

		ca.HTTP[i] = httpAddr
	}

	return ca
}

func NewRootCmd(
	t *testing.T,
	log *slog.Logger,
) CmdEnv {
	t.Helper()

	return CmdEnv{
		log:     log,
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

// keyAddOutput is used for unmarshaling the output of the keys add command,
// so that we can collect the bech32 address for the key that was added.
type keyAddOutput struct {
	Address string
}
