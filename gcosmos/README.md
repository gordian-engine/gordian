# gcosmos

This is a temporary workspace for integrating Gordian with [the Cosmos SDK](https://github.com/cosmos/cosmos-sdk).
As Gordian core reaches a stable release, the gcosmos tree will move to its own repository.

## Setup

We are currently using a local, unmodified clone of the SDK in tandem with Go workspaces.
It is a bit unconventional to commit a Go workspace file, but while both Gordian and the Cosmos SDK
are being actively changed, a fixed Go workspace fits well for now.

From the `gcosmos` directory, run `./_cosmosvendor/sync_sdk.bash` to clone or fetch and checkout
a "known working" version of the Cosmos SDK compatible with the current gcosmos tree,
and then apply any currently necessary patches to the SDK.
You may need to run `go work sync` from the `gcosmos` directory again.

### New patches

New patches to the SDK should build upon the existing patches,
so long as the existing patches are necessary.

To continue adding patches on top of the existing ones,
the simplest workflow is:

1. Ensure you are already synced via the `sync_sdk.bash` script.
2. Ensure you have the latest SDK commit, typically via `git fetch` inside the `_cosmosvendor/cosmos-sdk` directory.
3. Rebase the existing patch set onto the latest commit, typically with `git rebase origin/main`. Address conflicts as needed.
4. Optionally commit new code to your SDK checkout.
4. From your new rebased set of patches, within the `_cosmosvendor/cosmos-sdk` directory,
   run `git format-patch -o ../patches origin/main` to overwrite the existing set of patches with a new set that no longer has conflicts.
5. Be sure to update `_cosmosvendor/COSMOS_SDK.commit`.

Of course, upstreaming changes to the actual Cosmos SDK repository would be preferred,
but sometimes a local patch makes more sense.

## Running

Begin running the updated siampp commands from the `gcosmos` directory.

```bash
rm -rf ~/.simappv2/
go build -o gcosmos .

./gcosmos init moniker
echo -n "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon art" > mnemonic.txt

./gcosmos keys add val --recover --source mnemonic.txt
./gcosmos genesis add-genesis-account val 1000000stake --keyring-backend=test
./gcosmos genesis gentx val 1000000stake --keyring-backend=test --chain-id=gcosmos
./gcosmos genesis collect-gentxs

# rm -rf ~/.simappv2/data/application.db/
./gcosmos start --g-http-addr 127.0.0.1:26657
```

# Interact
```bash
# go install github.com/fullstorydev/grpcurl/cmd/grpcurl@latest

grpcurl -plaintext localhost:9092 list
grpcurl -plaintext localhost:9092 server.GordianGRPC/GetBlocksWatermark

```