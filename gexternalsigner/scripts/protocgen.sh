#!/usr/bin/env bash

set -eox pipefail

proto_dirs=$(find ./proto -path -prune -o -name '*.proto' -print0 | xargs -0 -n1 dirname | sort | uniq)
for dir in $proto_dirs; do
  for file in $(find "${dir}" -maxdepth 1 -name '*.proto'); do
      protoc "${file}" \
         --go_out=. \
         --go-grpc_out=.
  done
done

mv github.com/rollchains/gordian/gexternalsigner/* .
rm -rf github.com