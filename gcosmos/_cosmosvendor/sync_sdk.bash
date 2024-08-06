#!/bin/bash

set -eu -o pipefail

VENDOR_DIR=$( cd -- "$( dirname -- "${BASH_SOURCE[0]}" )" &> /dev/null && pwd )
readonly VENDOR_DIR
cd "$VENDOR_DIR"

readonly SDK_DIR="${VENDOR_DIR}/cosmos-sdk"
echo "SDK_DIR=$SDK_DIR"
if ! test -d "$SDK_DIR"; then
  >&2 echo "$SDK_DIR does not exist; cloning it"

  # Clone over HTTPS, but try to borrow objects from the two most likely locations
  # where a user may have an existing clone.
  # 1. GOPATH style, assuming we are in GOPATH/src/github.com/rollchains/gordian/gcosmos/_cosmosvendor
  #    and the other repo is GOPATH/src/github.com/cosmos/cosmos-sdk
  # 2. Go module style, where we are in $X/gordian/gcosmos
  #    and the other repo is in $X/cosmos-sdk
  #
  # And --dissociate to ensure that our vendored copy works
  # even if the referenced repo goes away for whatever reason.
  git clone \
    --reference-if-able ../../../../cosmos/cosmos-sdk \
    --reference-if-able ../../../cosmos-sdk \
    --dissociate \
    https://github.com/cosmos/cosmos-sdk \
    "$SDK_DIR"
fi

cd cosmos-sdk
if ! git diff --quiet && git diff --cached --quiet; then
  >&2 echo 'Refusing to sync when there are local changes'
  >&2 git diff --name-only || true
  >&2 git diff --cached --name-only || true
  exit 1
fi

git fetch -p
git checkout "$(sed '/#/d' "${VENDOR_DIR}/COSMOS_SDK.commit")"

# TODO: gracefully handle when there are no files in the patches directory.
git am ../patches/*.patch
