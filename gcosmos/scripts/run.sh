# script version of gcosmos/README.md for faster development

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

else
    # Run the following to reset the application state without having to reset the base data directory.
    # (This is required until Gordian can start from a >0 height)
    rm -rf ~/.simappv2/data/application.db/
fi

./gcosmos start --g-http-addr 127.0.0.1:26657 --g-grpc-addr 127.0.0.1:9092
