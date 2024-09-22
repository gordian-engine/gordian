# script version of gcosmos/README.md for faster development
# CLEAN=true sh scripts/run.sh

echo "Building gcosmos..."
go build -o gcosmos .

if [ "$CLEAN" == "true" ]; then
  rm -rf ~/.simappv2/

  ./gcosmos init moniker

    # example-mnemonic address: cosmos1r5v5srda7xfth3hn2s26txvrcrntldjumt8mhl
    echo -n "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon art" > example-mnemonic.txt

    ./gcosmos keys add val --recover --source example-mnemonic.txt
    ./gcosmos genesis add-genesis-account val 10000000stake --keyring-backend=test
    ./gcosmos genesis gentx val 1000000stake --keyring-backend=test --chain-id=gcosmos
    ./gcosmos genesis collect-gentxs

    # set the grpc-address for the client
    ./gcosmos config set client chain-id clkient-test-chain-cfg-1
    ./gcosmos config set client grpc-address 127.0.0.1:9090
    ./gcosmos config set client grpc-insecure true
    ./gcosmos config set client output json

else
    # Run the following to reset the application state without having to reset the base data directory.
    # (This is required until Gordian can start from a >0 height)
    rm -rf ~/.simappv2/data/application.db/
fi

./gcosmos start --g-http-addr 127.0.0.1:26657 --g-grpc-addr 127.0.0.1:9092


# ./gcosmos keys list

# # uses the simappv2/client.toml grpc & output settings on the simapp directly.
# ./gcosmos q bank balances cosmos1r5v5srda7xfth3hn2s26txvrcrntldjumt8mhl

# ./gcosmos tx bank send val cosmos10r39fueph9fq7a6lgswu4zdsg8t3gxlqvvvyvn 1stake --grpc-addr=127.0.0.1:9090 --grpc-insecure --keyring-backend=test --broadcast-mode=grpc --yes


# ./gcosmos q tx D8FF0A405957A3D090A485CA3C997A25E2964F2E7840DDBCBFE805EC97192651 --grpc-addr 127.0.0.1:9090 --grpc-insecure --output=json
