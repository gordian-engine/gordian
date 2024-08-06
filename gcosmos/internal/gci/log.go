package gci

import (
	"log/slog"

	"github.com/cosmos/cosmos-sdk/server"
	"github.com/rollchains/gordian/gcosmos/slogcosmos"
	"github.com/spf13/cobra"
)

func overwriteServerContextLogger(cmd *cobra.Command, log *slog.Logger) {
	serverCtx := server.GetServerContextFromCmd(cmd)
	serverCtx.Logger = slogcosmos.NewLogger(log)
	if err := server.SetCmdServerContext(cmd, serverCtx); err != nil {
		panic(err)
	}
}
