#!/bin/sh

CHAIN_ID=${CHAIN_ID:-"localchain-1"}
G_HTTP_ADDR=${G_HTTP_ADDR:-"26657"}
G_GRPC_ADDR=${G_GRPC_ADDR:-"9092"}

# cleanup previous run data as gordian can only start from height 0 currently
rm -rf ~/.simappv2/

echo "Building gcosmos binary..."
go build -o gcosmos .

./gcosmos init moniker --chain-id=${CHAIN_ID}

# example-mnemonic address: cosmos1r5v5srda7xfth3hn2s26txvrcrntldjumt8mhl
echo -n "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon art" > example-mnemonic.txt

./gcosmos keys add val --recover --source example-mnemonic.txt
./gcosmos genesis add-genesis-account val 10000000stake --keyring-backend=test
./gcosmos genesis gentx val 1000000stake --keyring-backend=test --chain-id=${CHAIN_ID}
./gcosmos genesis collect-gentxs

./gcosmos start --g-http-addr 127.0.0.1:$G_HTTP_ADDR --g-grpc-addr 127.0.0.1:$G_GRPC_ADDR
