package gserver

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/cometbft/cometbft/privval"
	"github.com/cosmos/cosmos-sdk/client"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/gordian-engine/gordian/tm/tmp2p/tmlibp2p"
	"github.com/libp2p/go-libp2p"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	libp2phost "github.com/libp2p/go-libp2p/core/host"
	libp2ppeer "github.com/libp2p/go-libp2p/core/peer"
	"github.com/spf13/cobra"
)

func newSeedCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "seed",
		Short: `Run a "seed node" as a central discovery point for other libp2p nodes`,
		Args:  cobra.ExactArgs(1),

		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithCancel(cmd.Context())
			defer cancel()

			h, err := tmlibp2p.NewHost(
				ctx,
				tmlibp2p.HostOptions{
					Options: []libp2p.Option{
						// Since unspecified, use a dynamic identity and a random listening address.
						// Specify only tcp for the protocol, just for simplicity in stack traces.
						libp2p.ListenAddrStrings("/ip4/0.0.0.0/tcp/0"),

						// Unsure if this is something we always want.
						// Can be controlled by a flag later if undesirable by default.
						libp2p.ForceReachabilityPublic(),
					},
				},
			)
			if err != nil {
				return fmt.Errorf("failed to create libp2p host: %w", err)
			}

			host := h.Libp2pHost()

			// We currently rely on the libp2p DHT for peer discovery,
			// so the seed node needs to run the DHT as well.
			// The validators connect to the DHT through the tmlibp2p.Connection type.
			if _, err := dht.New(ctx, host, dht.ProtocolPrefix("/gordian")); err != nil {
				return fmt.Errorf("failed to create DHT peer for seed: %w", err)
			}

			hostInfo := libp2phost.InfoFromHost(host)
			p2pAddrs, err := libp2ppeer.AddrInfoToP2pAddrs(hostInfo)
			if err != nil {
				return fmt.Errorf("failed to get host p2p addrs: %w", err)
			}

			var hostAddrs []string
			for _, a := range p2pAddrs {
				hostAddrs = append(hostAddrs, a.String())
			}

			joinedAddrs := strings.Join(hostAddrs, "\n") + "\n" // Trailing newline indicates proper end of input.

			if err := os.WriteFile(args[0], []byte(joinedAddrs), 0600); err != nil {
				return fmt.Errorf("failed to write seed address output file %q: %w", args[0], err)
			}

			fmt.Fprintf(cmd.ErrOrStderr(), "Seed running at multiaddrs: %s\n", joinedAddrs)

			return nil
		},
	}
}

func newPrintValPubKeyCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "val-pub-key",
		Short: `Print the JSON for the validator public key, suitable to use in the pubkey field of the create-validator JSON`,
		Args:  cobra.NoArgs,

		RunE: func(cmd *cobra.Command, args []string) error {
			cometConfig := client.GetConfigFromCmd(cmd)
			fpv := privval.LoadFilePV(cometConfig.PrivValidatorKeyFile(), cometConfig.PrivValidatorStateFile())

			sdkPK, err := cryptocodec.FromCmtPubKeyInterface(fpv.Key.PubKey)
			if err != nil {
				return fmt.Errorf("failed to extract SDK public key: %w", err)
			}

			clientCtx := client.GetClientContextFromCmd(cmd)
			j, err := clientCtx.Codec.MarshalInterfaceJSON(sdkPK)
			if err != nil {
				return fmt.Errorf("failed to marshal SDK key to JSON: %w", err)
			}
			fmt.Fprintln(cmd.OutOrStdout(), string(j))
			return nil
		},
	}
}
