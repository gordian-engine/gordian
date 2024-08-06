// Command gcosmos wraps the Cosmos SDK simappv2
// and adds a "gstart" subcommand to run parts of Gordian.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"cosmossdk.io/simapp/v2"
	svrcmd "github.com/cosmos/cosmos-sdk/server/cmd"
	"github.com/rollchains/gordian/gcosmos/internal/gci"
)

func main() {
	rootCmd := gci.NewSimdRootCmdWithGordian(
		// We should probably use an os.SignalContext for this in order to ^c properly.
		context.Background(),

		// Setting the logger this way is likely to cause interleaved writes
		// with the SDK logging system, but this is fine as a quick solution.
		slog.New(slog.NewTextHandler(os.Stderr, nil)),
	)
	if err := svrcmd.Execute(rootCmd, "", simapp.DefaultNodeHome); err != nil {
		fmt.Fprintln(rootCmd.OutOrStderr(), err)
		os.Exit(1)
	}
}
