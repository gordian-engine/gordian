package main_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gordian-engine/gordian/gcosmos/internal/gci"
	"github.com/gordian-engine/gordian/internal/gtest"
	"github.com/stretchr/testify/require"
)

// Configuration for gordian consensus storage.
// If useMemstore=false and useSQLiteInMem=false,
// then on-disk storage is used with temporary database files.
// If only useSQLiteInMem is true,
// then all instances use the in-memory tmsqlite database.
// If only useMemstore is true, the tmmemstore stores are used.
// If both are true, the tests panic (see the following init function).
const (
	useMemStore    = false
	useSQLiteInMem = false
)

func init() {
	if useMemStore && useSQLiteInMem {
		panic(fmt.Errorf(
			"The useMemStore and useSQLiteInMem must both be false, or only one must be true (got useMemStore=%t and useSQLiteInMem=%t)",
			useMemStore, useSQLiteInMem,
		))
	}
}

func TestRootCmd(t *testing.T) {
	t.Parallel()

	e := NewRootCmd(t, gtest.NewLogger(t))

	e.Run("init", "defaultmoniker").NoError(t)
}

func TestRootCmd_startWithGordian_singleValidator(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	c := ConfigureChain(t, ctx, ChainConfig{
		ID:            t.Name(),
		NVals:         1,
		StakeStrategy: ConstantStakeStrategy(1_000_000_000),
	})

	httpAddr := c.Start(t, ctx, 1).HTTP[0]

	if !gci.RunCometInsteadOfGordian {
		u := "http://" + httpAddr + "/blocks/watermark"
		// TODO: we might need to delay until we get a non-error HTTP response.

		deadline := time.Now().Add(10 * time.Second)
		var maxHeight uint
		for time.Now().Before(deadline) {
			resp, err := http.Get(u)
			require.NoError(t, err)
			require.Equal(t, http.StatusOK, resp.StatusCode)

			var m map[string]uint
			require.NoError(t, json.NewDecoder(resp.Body).Decode(&m))
			resp.Body.Close()

			maxHeight = m["VotingHeight"]
			if maxHeight < 3 {
				time.Sleep(100 * time.Millisecond)
				continue
			}

			// We got at least to height 3, so quit the loop.
			break
		}

		require.GreaterOrEqual(t, maxHeight, uint(3))
	}
}

func TestRootCmd_startWithGordian_multipleValidators(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow test in short mode")
	}

	const totalVals = 11
	const interestingVals = 4

	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	chainID := t.Name()
	c := ConfigureChain(t, ctx, ChainConfig{
		ID:    chainID,
		NVals: totalVals,

		// Due to an outstanding and undocumented SDK bug,
		// we require at least 11 validators.
		// But, we don't want to run 11 validators.
		// So put the majority of the stake in the first four validators,
		// and the rest get the bare minimum.
		StakeStrategy: func(idx int) string {
			const minAmount = "1000000"
			if idx < interestingVals {
				// Validators with substantial stake.
				// Give every validator a slightly different amount,
				// so that a tracked single vote's power can be distinguished.
				return minAmount + fmt.Sprintf("%02d000000stake", idx)
			}

			// Beyond that, give them bare minimum stake.
			return minAmount + "stake"
		},
	})

	ca := c.Start(t, ctx, interestingVals)
	httpAddrs := ca.HTTP

	// Each of the interesting validators must report a height beyond the first few blocks.
	for i := range interestingVals {
		if gci.RunCometInsteadOfGordian {
			// Nothing to check in this mode.
			break
		}

		// Gratuitous deadline to get the voting height,
		// because the first proposed block is likely to time out
		// due to libp2p settle time.
		u := "http://" + httpAddrs[i] + "/blocks/watermark"
		deadline := time.Now().Add(30 * time.Second)
		var maxHeight uint
		for time.Now().Before(deadline) {
			resp, err := http.Get(u)
			require.NoErrorf(t, err, "failed to get the watermark for validator %d/%d", i, interestingVals)
			require.Equal(t, http.StatusOK, resp.StatusCode)

			var m map[string]uint
			require.NoError(t, json.NewDecoder(resp.Body).Decode(&m))
			resp.Body.Close()

			maxHeight = m["VotingHeight"]
			if maxHeight < 4 {
				time.Sleep(100 * time.Millisecond)
				continue
			}

			// We got at least to height 4, so quit the loop.
			break
		}

		require.GreaterOrEqualf(t, maxHeight, uint(4), "checking max block height on validator at index %d", i)
	}

	t.Run("adding a new validator catches up", func(t *testing.T) {
		if gci.RunCometInsteadOfGordian {
			t.Skip("skipping due to not testing Gordian")
		}

		localCtx, cancel := context.WithCancel(ctx)
		defer cancel()

		lateHTTPAddrFile := AddLateNode(t, localCtx, chainID, c.CanonicalGenesisPath, ca.P2PSeedPath)
		var httpAddr string

		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			b, err := os.ReadFile(lateHTTPAddrFile)
			if err != nil {
				time.Sleep(100 * time.Millisecond)
				continue
			}

			s, ok := strings.CutSuffix(string(b), "\n")
			if !ok {
				time.Sleep(100 * time.Millisecond)
				continue
			}

			httpAddr = s
			break
		}

		if httpAddr == "" {
			t.Fatal("did not read http address from file before deadline")
		}

		u := "http://" + httpAddr + "/blocks/watermark"
		// TODO: we might need to delay until we get a non-error HTTP response.

		deadline = time.Now().Add(10 * time.Second)
		var maxHeight uint
		for time.Now().Before(deadline) {
			resp, err := http.Get(u)
			require.NoError(t, err)
			require.Equal(t, http.StatusOK, resp.StatusCode)

			var m map[string]uint
			require.NoError(t, json.NewDecoder(resp.Body).Decode(&m))
			resp.Body.Close()

			maxHeight = m["VotingHeight"]
			if maxHeight < 4 {
				time.Sleep(100 * time.Millisecond)
				continue
			}

			// We got at least to height 4, so quit the loop.
			break
		}

		require.GreaterOrEqual(t, maxHeight, uint(4), "late-started server did not reach minimum height")
	})
}

func Test_single_restart(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in short mode")
	}

	if gci.RunCometInsteadOfGordian {
		// I mean, we could run it with Comet, but that doesn't seem worth the effort at this point.
		t.Skip("can only run restart test with Gordian")
	}

	if useMemStore || useSQLiteInMem {
		t.Skipf(
			"can only test restart with on-disk storage (have useMemStore=%t, useSQLiteInMem=%t)",
			useMemStore, useSQLiteInMem,
		)
	}

	t.Parallel()

	ctx1, cancel := context.WithCancel(context.Background())
	defer cancel()

	c := ConfigureChain(t, ctx1, ChainConfig{
		ID:            t.Name(),
		NVals:         1,
		StakeStrategy: ConstantStakeStrategy(1_000_000_000),

		// TODO: once this is passing with height watermarks,
		// set NFixedAccounts and confirm application state too.
	})

	httpAddr := c.Start(t, ctx1, 1).HTTP[0]

	if !gci.RunCometInsteadOfGordian {
		baseURL := "http://" + httpAddr

		// Make sure we are beyond the initial height.
		deadline := time.Now().Add(10 * time.Second)
		var maxHeight uint
		for time.Now().Before(deadline) {
			resp, err := http.Get(baseURL + "/blocks/watermark")
			require.NoError(t, err)
			require.Equal(t, http.StatusOK, resp.StatusCode)

			var m map[string]uint
			require.NoError(t, json.NewDecoder(resp.Body).Decode(&m))
			resp.Body.Close()

			maxHeight = m["VotingHeight"]
			if maxHeight < 3 {
				time.Sleep(100 * time.Millisecond)
				continue
			}

			// We are past initial height, so break out of the loop.
			break
		}

		require.GreaterOrEqual(t, maxHeight, uint(3))

		expHeight := maxHeight

		// Now cancel the context.
		// We expect the HTTP server to quit within a moment.
		cancel()
		deadline = time.Now().Add(3 * time.Second)
		gotError := false
		for time.Now().Before(deadline) {
			_, err := http.Get(baseURL + "/blocks/watermark")
			netErr := new(net.OpError)
			if errors.As(err, &netErr) {
				gotError = true
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		require.True(t, gotError, "did not get network error in time")
		t.Log("HTTP server was shut down, sleeping a moment before restarting")

		time.Sleep(2 * time.Second)

		// New context and new started instance.
		ctx2, cancel := context.WithCancel(context.Background())
		defer cancel()

		baseURL = "http://" + c.Start(t, ctx2, 1).HTTP[0]

		// c.Start blocks until the HTTP server is available,
		// so we should be able to access it immediately.
		maxHeight = 0
		resp, err := http.Get(baseURL + "/blocks/watermark")
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode)

		var m map[string]uint
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&m))
		resp.Body.Close()

		maxHeight = m["VotingHeight"]
		require.GreaterOrEqual(t, maxHeight, expHeight)

		// Now wait for the height to increase by two more.
		expHeight += 2

		deadline = time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			resp, err := http.Get(baseURL + "/blocks/watermark")
			require.NoError(t, err)
			require.Equal(t, http.StatusOK, resp.StatusCode)

			var m map[string]uint
			require.NoError(t, json.NewDecoder(resp.Body).Decode(&m))
			resp.Body.Close()

			maxHeight = m["VotingHeight"]
			if maxHeight >= expHeight {
				break
			}
		}
		require.GreaterOrEqual(t, maxHeight, expHeight)
	}
}

func TestTx_single_basicSend(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	c := ConfigureChain(t, ctx, ChainConfig{
		ID:            t.Name(),
		NVals:         1,
		StakeStrategy: ConstantStakeStrategy(1_000_000_000),

		NFixedAccounts:             2,
		FixedAccountInitialBalance: 10_000,
	})

	httpAddr := c.Start(t, ctx, 1).HTTP[0]

	if !gci.RunCometInsteadOfGordian {
		baseURL := "http://" + httpAddr

		// Make sure we are beyond the initial height.
		deadline := time.Now().Add(10 * time.Second)
		var maxHeight uint
		for time.Now().Before(deadline) {
			resp, err := http.Get(baseURL + "/blocks/watermark")
			require.NoError(t, err)
			require.Equal(t, http.StatusOK, resp.StatusCode)

			var m map[string]uint
			require.NoError(t, json.NewDecoder(resp.Body).Decode(&m))
			resp.Body.Close()

			maxHeight = m["VotingHeight"]
			if maxHeight < 3 {
				time.Sleep(100 * time.Millisecond)
				continue
			}

			// We are past initial height, so break out of the loop.
			break
		}

		require.GreaterOrEqual(t, maxHeight, uint(3))

		// Ensure we still match the fixed account initial balance.
		resp, err := http.Get(baseURL + "/debug/accounts/" + c.FixedAddresses[0] + "/balance")
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode)
		var initBalance balance
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&initBalance))
		resp.Body.Close()
		require.Equal(t, "10000", initBalance.Balance.Amount)

		// Now that we are past the initial height,
		// make a transaction that we can submit.

		const sendAmount = "100stake"

		// First generate the transaction.
		res := c.RootCmds[0].Run(
			"tx", "bank", "send", c.FixedAddresses[0], c.FixedAddresses[1], sendAmount,
			"--chain-id", t.Name(),
			"--generate-only",
		)
		res.NoError(t)

		dir := t.TempDir()
		msgPath := filepath.Join(dir, "send.msg")
		require.NoError(t, os.WriteFile(msgPath, res.Stdout.Bytes(), 0o600))

		// TODO: is there a better way to dynamically get this?
		const accountNumber = 1

		// Sign the transaction offline so that we can send it.
		res = c.RootCmds[0].Run(
			"tx", "sign", msgPath,
			"--offline",
			"--chain-id", t.Name(),
			fmt.Sprintf("--account-number=%d", accountNumber),
			"--from", c.FixedAddresses[0],
			"--sequence=0", // We know this is the first transaction for the sender.
		)

		res.NoError(t)
		t.Logf("SIGN OUTPUT: %s", res.Stdout.String())
		t.Logf("SIGN ERROR : %s", res.Stderr.String())

		resp, err = http.Post(baseURL+"/debug/submit_tx", "application/json", &res.Stdout)
		require.NoError(t, err)

		// Just log out what it responds, for now.
		// We can't do much with the response until we actually start handling the transaction.
		b, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		t.Logf("response body: %s", b)
		require.Equal(t, http.StatusOK, resp.StatusCode)

		// TODO: make some height assertions here instead of just sleeping.
		time.Sleep(8 * time.Second)

		// Request first account balance again.
		resp, err = http.Get(baseURL + "/debug/accounts/" + c.FixedAddresses[0] + "/balance")
		require.NoError(t, err)
		var newBalance balance
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&newBalance))
		resp.Body.Close()
		require.Equal(t, "9900", newBalance.Balance.Amount) // Was at 10k, subtracted 100.

		// And second account should have increased by 100.
		resp, err = http.Get(baseURL + "/debug/accounts/" + c.FixedAddresses[1] + "/balance")
		require.NoError(t, err)
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&newBalance))
		resp.Body.Close()
		require.Equal(t, "10100", newBalance.Balance.Amount) // Was at 10k, added 100.
	}
}

func TestTx_single_delegate(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const fixedAccountInitialBalance = 75_000_000
	c := ConfigureChain(t, ctx, ChainConfig{
		ID:            t.Name(),
		NVals:         1,
		StakeStrategy: ConstantStakeStrategy(1_000_000_000),

		NFixedAccounts: 1,

		FixedAccountInitialBalance: fixedAccountInitialBalance,
	})

	httpAddr := c.Start(t, ctx, 1).HTTP[0]

	if !gci.RunCometInsteadOfGordian {
		baseURL := "http://" + httpAddr

		// Make sure we are beyond the initial height.
		deadline := time.Now().Add(10 * time.Second)
		var maxHeight uint
		for time.Now().Before(deadline) {
			resp, err := http.Get(baseURL + "/blocks/watermark")
			require.NoError(t, err)
			require.Equal(t, http.StatusOK, resp.StatusCode)

			var m map[string]uint
			require.NoError(t, json.NewDecoder(resp.Body).Decode(&m))
			resp.Body.Close()

			maxHeight = m["VotingHeight"]
			if maxHeight < 3 {
				time.Sleep(100 * time.Millisecond)
				continue
			}

			// We are past initial height, so break out of the loop.
			break
		}

		require.GreaterOrEqual(t, maxHeight, uint(3))

		// Get the validator set.
		resp, err := http.Get(baseURL + "/validators")
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode)

		var output struct {
			FinalizationHeight uint64
			Validators         []struct {
				// Don't care about the pubkey now,
				// since there is only one validator.
				Power uint64
			}
		}

		require.NoError(t, json.NewDecoder(resp.Body).Decode(&output))
		require.Len(t, output.Validators, 1)

		// We need the starting power, because it should increase once we delegate.
		startingPow := output.Validators[0].Power

		delegateAmount := fmt.Sprintf("%dstake", fixedAccountInitialBalance)

		// First generate the transaction.
		res := c.RootCmds[0].Run(
			// val0 is the name of the first validator key,
			// which should be available on the first root command.
			"tx", "staking", "delegate", "val0", delegateAmount, "--from", c.FixedAddresses[0],
			"--generate-only",
		)
		res.NoError(t)

		dir := t.TempDir()
		msgPath := filepath.Join(dir, "delegate.msg")
		require.NoError(t, os.WriteFile(msgPath, res.Stdout.Bytes(), 0o600))

		// TODO: is there a better way to dynamically get this?
		const accountNumber = 1

		// Sign the transaction offline so that we can send it.
		res = c.RootCmds[0].Run(
			"tx", "sign", msgPath,
			"--offline",
			"--chain-id", t.Name(),
			fmt.Sprintf("--account-number=%d", accountNumber),
			"--from", c.FixedAddresses[0],
			"--sequence=0", // We know this is the first transaction for the sender.
		)

		res.NoError(t)
		t.Logf("SIGN OUTPUT: %s", res.Stdout.String())
		t.Logf("SIGN ERROR : %s", res.Stderr.String())

		resp, err = http.Post(baseURL+"/debug/submit_tx", "application/json", &res.Stdout)
		require.NoError(t, err)

		// Just log out what it responds, for now.
		b, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		t.Logf("response body: %s", b)
		require.Equal(t, http.StatusOK, resp.StatusCode)

		// TODO: make some height assertions here instead of just sleeping.
		time.Sleep(8 * time.Second)

		// First account should have a balance of zero,
		// now that everything has been delegated.
		resp, err = http.Get(baseURL + "/debug/accounts/" + c.FixedAddresses[0] + "/balance")
		require.NoError(t, err)
		var newBalance balance
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&newBalance))
		resp.Body.Close()
		require.Equal(t, "0", newBalance.Balance.Amount) // Entire balance was delegated.

		resp, err = http.Get(baseURL + "/validators")
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode)
		clear(output.Validators)

		require.NoError(t, json.NewDecoder(resp.Body).Decode(&output))
		require.Len(t, output.Validators, 1)

		endingPow := output.Validators[0].Power
		require.Greater(t, endingPow, startingPow)
		t.Logf("After delegation, power increased from %d to %d", startingPow, endingPow)
	}
}

func TestTx_single_addAndRemoveNewValidator(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const fixedAccountInitialBalance = 2_000_000_000
	c := ConfigureChain(t, ctx, ChainConfig{
		ID:    t.Name(),
		NVals: 1,
		// Arbitrarily larger stake for first validator,
		// so we can continue consensus without the second validator really contributing
		// while remaining offline.
		StakeStrategy: ConstantStakeStrategy(12 * fixedAccountInitialBalance),

		NFixedAccounts: 1,

		FixedAccountInitialBalance: fixedAccountInitialBalance,
	})

	httpAddr := c.Start(t, ctx, 1).HTTP[0]

	if !gci.RunCometInsteadOfGordian {
		chainID := t.Name()

		baseURL := "http://" + httpAddr

		// Make sure we are beyond the initial height.
		deadline := time.Now().Add(10 * time.Second)
		var maxHeight uint
		for time.Now().Before(deadline) {
			resp, err := http.Get(baseURL + "/blocks/watermark")
			require.NoError(t, err)
			require.Equal(t, http.StatusOK, resp.StatusCode)

			var m map[string]uint
			require.NoError(t, json.NewDecoder(resp.Body).Decode(&m))
			resp.Body.Close()

			maxHeight = m["VotingHeight"]
			if maxHeight < 3 {
				time.Sleep(100 * time.Millisecond)
				continue
			}

			// We are past initial height, so break out of the loop.
			break
		}

		require.GreaterOrEqual(t, maxHeight, uint(3))

		// Now the fixed address wants to become a validator.
		// Use its mnemonic to create a new environment.
		newValRootCmd := NewRootCmd(t, gtest.NewLogger(t).With("owner", "newVal"))
		newValRootCmd.RunWithInput(
			strings.NewReader(FixedMnemonics[0]),
			"init", "newVal", "--recover",
		).NoError(t)

		// And we need to add the actual key,
		// which also involves storing the mnemonic on disk.
		scratchDir := t.TempDir()
		mPath := filepath.Join(scratchDir, "fixed_mnemonic.txt")
		require.NoError(t, os.WriteFile(mPath, []byte(FixedMnemonics[0]), 0o600))
		res := newValRootCmd.Run(
			"keys", "add", "newVal",
			"--recover", "--source", mPath,
		)
		res.NoError(t)

		// First we have to generate the JSON for creating a validator.
		// We'll have to use a map to serialize this,
		// as the struct type in x/staking is not exported.

		delegateAmount := fmt.Sprintf("%dstake", fixedAccountInitialBalance/2)
		createJson := map[string]any{
			"amount":  delegateAmount,
			"moniker": "newVal",

			"min-self-delegation": "1000", // Unsure how this will play out in undelegating.

			// Values here copied from output of create-validator --help.
			// Shouldn't really matter for this test.
			"commission-rate":            "0.1",
			"commission-max-rate":        "0.2",
			"commission-max-change-rate": "0.01",
		}

		// And we have to add the pubkey field,
		// which we have to retrieve from keys show.
		res = newValRootCmd.Run("gordian", "val-pub-key")
		res.NoError(t)

		var pubKeyObj map[string]string
		require.NoError(t, json.Unmarshal([]byte(res.Stdout.Bytes()), &pubKeyObj))

		createJson["pubkey"] = pubKeyObj

		jCreate, err := json.Marshal(createJson)
		require.NoError(t, err)

		// staking create-validator reads the JSON from disk.
		createPath := filepath.Join(scratchDir, "create.json")
		require.NoError(t, os.WriteFile(createPath, jCreate, 0o600))

		res = newValRootCmd.Run(
			"tx", "staking",
			"create-validator", createPath,
			"--from", "newVal",
			"--chain-id", chainID,
			"--generate-only",
		)
		res.NoError(t)

		stakePath := filepath.Join(scratchDir, "stake.msg")
		require.NoError(t, os.WriteFile(stakePath, res.Stdout.Bytes(), 0o600))

		// TODO: is there a better way to dynamically get this?
		const accountNumber = 1

		// Sign the transaction offline so that we can send it.
		res = newValRootCmd.Run(
			"tx", "sign", stakePath,
			"--offline",
			"--chain-id", chainID,
			fmt.Sprintf("--account-number=%d", accountNumber),
			"--from", "newVal",
			"--sequence=0", // We know this is the first transaction for the account.
		)
		res.NoError(t)

		resp, err := http.Post(baseURL+"/debug/submit_tx", "application/json", &res.Stdout)
		require.NoError(t, err)

		b, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		t.Logf("Response body from submitting tx: %s", b)
		resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)

		// Wait for create-validator transaction to flush.
		deadline = time.Now().Add(time.Minute)
		pendingTxFlushed := false
		for time.Now().Before(deadline) {
			resp, err := http.Get(baseURL + "/debug/pending_txs")
			require.NoError(t, err)
			if resp.StatusCode == http.StatusInternalServerError {
				// There is an issue with printing pending transactions containing a create validator message.

				// Avoid printing body when it's still erroring.
				time.Sleep(2 * time.Second)
				continue
			}

			b, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			resp.Body.Close()

			if have := string(b); have == "[]" || have == "[]\n" {
				pendingTxFlushed = true
				break
			}

			t.Logf("pending tx body: %s", string(b))
			time.Sleep(time.Second)
		}

		require.True(t, pendingTxFlushed, "pending tx not flushed within a minute")

		// Now, we should soon see two validators.
		deadline = time.Now().Add(time.Minute)
		sawTwoVals := false
		for time.Now().Before(deadline) {
			resp, err := http.Get(baseURL + "/validators")
			require.NoError(t, err)
			require.Equal(t, http.StatusOK, resp.StatusCode)

			var valOutput struct {
				Validators []struct {
					// Don't care about any validator fields.
					// Just need two entries.
				}
			}

			require.NoError(t, json.NewDecoder(resp.Body).Decode(&valOutput))
			resp.Body.Close()
			if len(valOutput.Validators) == 1 {
				time.Sleep(time.Second)
				continue
			}

			require.Len(t, valOutput.Validators, 2)
			sawTwoVals = true
			break
		}

		require.True(t, sawTwoVals, "did not see two validators listed within a minute of submitting create-validator tx")

		t.Run("undelegating from the new validator", func(t *testing.T) {
			// If we just restake everything away from the new validator --
			// which we should be allowed to do immediately --
			// then the new validator should be below the threshold,
			// and should be removed from the list.

			res := newValRootCmd.Run(
				"keys", "show", "newVal",
				"--bech", "val",
				"--address",
			)
			res.NoError(t)
			newValOperAddr := strings.TrimSpace(res.Stdout.String())

			res = c.RootCmds[0].Run(
				"keys", "show", "val0",
				"--bech", "val",
				"--address",
			)
			res.NoError(t)
			origValOperAddr := strings.TrimSpace(res.Stdout.String())

			res = newValRootCmd.Run(
				"tx", "staking", "redelegate",
				newValOperAddr, origValOperAddr, // From new, to original.
				delegateAmount, // The same amount we fully delegated to the new validator.
				"--from", "newVal",
				"--generate-only",
				"--chain-id", chainID,
			)
			res.NoError(t)
			t.Logf("redelegate stdout: %s", res.Stdout.String())
			t.Logf("redelegate stderr: %s", res.Stderr.String())

			// Now write the redelegate message to disk and sign it.
			redelegatePath := filepath.Join(scratchDir, "redelegate.msg")
			require.NoError(t, os.WriteFile(redelegatePath, res.Stdout.Bytes(), 0o600))
			res = newValRootCmd.Run(
				"tx", "sign", redelegatePath,
				"--offline",
				"--chain-id", chainID,
				fmt.Sprintf("--account-number=%d", accountNumber),
				"--from", "newVal",
				"--sequence=1", // Already sent one transaction, next sequence is 1.
			)
			res.NoError(t)

			// Submit the transaction.
			resp, err := http.Post(baseURL+"/debug/submit_tx", "application/json", &res.Stdout)
			require.NoError(t, err)

			require.Equal(t, http.StatusOK, resp.StatusCode)
			b, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			t.Logf("Response body from submitting tx: %s", b)
			resp.Body.Close()

			// Wait for create-validator transaction to flush.
			deadline = time.Now().Add(time.Minute)
			pendingTxFlushed := false
			for time.Now().Before(deadline) {
				resp, err := http.Get(baseURL + "/debug/pending_txs")
				require.NoError(t, err)

				// We don't have the serializing issue with the redelegate message,
				// like we did with create-validator.
				require.Equal(t, http.StatusOK, resp.StatusCode)

				b, err := io.ReadAll(resp.Body)
				require.NoError(t, err)
				resp.Body.Close()

				if have := string(b); have == "[]" || have == "[]\n" {
					pendingTxFlushed = true
					break
				}

				t.Logf("pending tx body: %s", string(b))
				time.Sleep(time.Second)
			}

			require.True(t, pendingTxFlushed, "pending tx not flushed within a minute")

			// Now, we should soon see just the one validator again.
			deadline = time.Now().Add(time.Minute)
			sawOneVal := false
			for time.Now().Before(deadline) {
				resp, err := http.Get(baseURL + "/validators")
				require.NoError(t, err)
				require.Equal(t, http.StatusOK, resp.StatusCode)

				var valOutput struct {
					Validators []struct {
						// Don't care about any validator fields.
						// Just need two entries.
					}
				}

				require.NoError(t, json.NewDecoder(resp.Body).Decode(&valOutput))
				resp.Body.Close()
				if len(valOutput.Validators) != 1 {
					time.Sleep(time.Second)
					continue
				}

				sawOneVal = true
				break
			}

			require.True(t, sawOneVal, "should have been back down to one validator")
		})
	}
}

func TestTx_multiple_simpleSend(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow test in short mode")
	}

	const totalVals = 11
	const interestingVals = 4

	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	chainID := t.Name()
	c := ConfigureChain(t, ctx, ChainConfig{
		ID:    chainID,
		NVals: totalVals,

		// Due to an outstanding and undocumented SDK bug,
		// we require at least 11 validators.
		// But, we don't want to run 11 validators.
		// So put the majority of the stake in the first four validators,
		// and the rest get the bare minimum.
		StakeStrategy: func(idx int) string {
			const minAmount = "1000000"
			if idx < interestingVals {
				// Validators with substantial stake.
				// Give every validator a slightly different amount,
				// so that a tracked single vote's power can be distinguished.
				return minAmount + fmt.Sprintf("%02d000000stake", idx)
			}

			// Beyond that, give them bare minimum stake.
			return minAmount + "stake"
		},

		NFixedAccounts:             2,
		FixedAccountInitialBalance: 10_000,
	})

	ca := c.Start(t, ctx, interestingVals)
	httpAddrs := ca.HTTP

	// Each of the interesting validators must report a height beyond the first few blocks.
	for i := range interestingVals {
		if gci.RunCometInsteadOfGordian {
			// Nothing to check in this mode.
			break
		}

		u := "http://" + httpAddrs[i] + "/blocks/watermark"
		// TODO: we might need to delay until we get a non-error HTTP response.

		// Another deadline to confirm the voting height.
		// This is a lot longer than the deadline to check HTTP addresses
		// because the very first proposed block at 1/0 is expected to time out.
		deadline := time.Now().Add(30 * time.Second)
		var maxHeight uint
		for time.Now().Before(deadline) {
			resp, err := http.Get(u)
			require.NoErrorf(t, err, "failed to get the watermark for validator %d/%d", i, interestingVals)
			require.Equal(t, http.StatusOK, resp.StatusCode)

			var m map[string]uint
			require.NoError(t, json.NewDecoder(resp.Body).Decode(&m))
			resp.Body.Close()

			maxHeight = m["VotingHeight"]
			if maxHeight < 3 {
				time.Sleep(100 * time.Millisecond)
				continue
			}

			// We got at least to height 3, so quit the loop.
			break
		}

		require.GreaterOrEqualf(t, maxHeight, uint(3), "checking max block height on validator at index %d", i)
	}

	// Now that the validators are all a couple blocks past initial height,
	// it's time to submit a transaction.
	// TODO: the consensus strategy should be updated to skip the low power validators;
	// they are going to delay everything since they aren't running.

	const sendAmount = "100stake"

	// First generate the transaction.
	res := c.RootCmds[0].Run(
		"tx", "bank", "send", c.FixedAddresses[0], c.FixedAddresses[1], sendAmount,
		"--chain-id", t.Name(),
		"--generate-only",
	)
	res.NoError(t)

	dir := t.TempDir()
	msgPath := filepath.Join(dir, "send.msg")
	require.NoError(t, os.WriteFile(msgPath, res.Stdout.Bytes(), 0o600))

	// The validator accounts start at zero,
	// and this account is the first one after the validator set.
	const accountNumber = totalVals

	// Sign the transaction offline so that we can send it.
	res = c.RootCmds[0].Run(
		"tx", "sign", msgPath,
		"--offline",
		"--chain-id", t.Name(),
		fmt.Sprintf("--account-number=%d", accountNumber),
		"--from", c.FixedAddresses[0],
		"--sequence=0", // We know this is the first transaction for the sender.
	)

	res.NoError(t)

	resp, err := http.Post("http://"+httpAddrs[0]+"/debug/submit_tx", "application/json", &res.Stdout)
	require.NoError(t, err)

	// Just log out what it responds, for now.
	// We can't do much with the response until we actually start handling the transaction.
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	t.Logf("response body: %s", b)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	deadline := time.Now().Add(time.Minute)
	u := "http://" + httpAddrs[0] + "/debug/pending_txs"
	pendingTxFlushed := false
	for time.Now().Before(deadline) {
		resp, err := http.Get(u)
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode)

		b, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		resp.Body.Close()

		if have := string(b); have == "[]" || have == "[]\n" {
			pendingTxFlushed = true
			break
		}

		t.Logf("pending tx body: %s", string(b))
		time.Sleep(time.Second)
	}

	require.True(t, pendingTxFlushed, "pending tx not flushed within a minute")

	// Since the pending transaction was flushed, then every validator should report
	// that the sender's balance has decreased and the receiver's balance has increased.

	// Start checking immediately
	deadline = time.Now().Add(5 * time.Second)
	for i := range httpAddrs {
	RECHECK:
		if time.Now().After(deadline) {
			t.Fatalf("took too long to see correct balance; stuck on validator_index=%d", i)
		}

		resp, err = http.Get("http://" + httpAddrs[i] + "/debug/accounts/" + c.FixedAddresses[0] + "/balance")
		require.NoError(t, err)
		var newBalance balance
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&newBalance))
		resp.Body.Close()

		if newBalance.Balance.Amount == "10000" {
			time.Sleep(100 * time.Millisecond)
			goto RECHECK
		}

		require.Equalf(t, "9900", newBalance.Balance.Amount, "validator at index %d reported wrong sender balance", i) // Was at 10k, subtracted 100.

		resp, err = http.Get("http://" + httpAddrs[i] + "/debug/accounts/" + c.FixedAddresses[1] + "/balance")
		require.NoError(t, err)
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&newBalance))
		resp.Body.Close()
		require.Equalf(t, "10100", newBalance.Balance.Amount, "validator at index %d reported wrong receiver balance", i) // Was at 10k, added 100.
	}

	t.Run("adding a new validator catches up", func(t *testing.T) {
		if gci.RunCometInsteadOfGordian {
			t.Skip("skipping due to not testing Gordian")
		}

		localCtx, cancel := context.WithCancel(ctx)
		defer cancel()

		lateHTTPAddrFile := AddLateNode(t, localCtx, chainID, c.CanonicalGenesisPath, ca.P2PSeedPath)
		var httpAddr string

		// We've done a few things since our last watermark check.
		// See what the first validator is on,
		// and that will be our target height.

		heightResp, err := http.Get("http://" + httpAddrs[0] + "/blocks/watermark")
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, heightResp.StatusCode)

		var m map[string]uint
		require.NoError(t, json.NewDecoder(heightResp.Body).Decode(&m))
		heightResp.Body.Close()

		targetHeight := m["VotingHeight"]

		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			b, err := os.ReadFile(lateHTTPAddrFile)
			if err != nil {
				time.Sleep(100 * time.Millisecond)
				continue
			}

			s, ok := strings.CutSuffix(string(b), "\n")
			if !ok {
				time.Sleep(100 * time.Millisecond)
				continue
			}

			httpAddr = s
			break
		}

		if httpAddr == "" {
			t.Fatal("did not read http address from file before deadline")
		}

		u := "http://" + httpAddr + "/blocks/watermark"
		// TODO: we might need to delay until we get a non-error HTTP response.

		deadline = time.Now().Add(10 * time.Second)
		var maxHeight uint
		for time.Now().Before(deadline) {
			resp, err := http.Get(u)
			require.NoError(t, err)
			require.Equal(t, http.StatusOK, resp.StatusCode)

			var m map[string]uint
			require.NoError(t, json.NewDecoder(resp.Body).Decode(&m))
			resp.Body.Close()

			maxHeight = m["VotingHeight"]
			if maxHeight < targetHeight {
				time.Sleep(100 * time.Millisecond)
				continue
			}

			// We got at least to the target height, so quit the loop.
			break
		}

		require.GreaterOrEqual(t, maxHeight, targetHeight, "late-started server did not reach target height of earlier validator")

		resp, err = http.Get("http://" + httpAddr + "/debug/accounts/" + c.FixedAddresses[0] + "/balance")
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode)
		var newBalance balance
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		t.Logf("response body for balance account 0: %q", body)
		require.NoError(t, json.NewDecoder(bytes.NewReader(body)).Decode(&newBalance))
		resp.Body.Close()
		require.Equal(t, "9900", newBalance.Balance.Amount, "late validator reported wrong sender balance") // Was at 10k, subtracted 100.

		resp, err = http.Get("http://" + httpAddr + "/debug/accounts/" + c.FixedAddresses[1] + "/balance")
		require.NoError(t, err)
		require.Equal(t, http.StatusOK, resp.StatusCode)
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&newBalance))
		resp.Body.Close()
		require.Equal(t, "10100", newBalance.Balance.Amount, "validator reported wrong receiver balance") // Was at 10k, added 100.
	})
}

type balance struct {
	Balance struct {
		Amount string
	}
}
