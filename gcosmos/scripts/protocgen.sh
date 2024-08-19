#!/usr/bin/env bash
# ./scripts/protocgen.sh

set -eox pipefail

echo "Generating proto code"

proto_dirs=$(find ./proto -path -prune -o -name '*.proto' -print0 | xargs -0 -n1 dirname | sort | uniq)
for dir in $proto_dirs; do
  for file in $(find "${dir}" -maxdepth 1 -name '*.proto'); do
      buf generate $file --template proto/buf.gen.yaml
  done
done

cp -r ./github.com/rollchains/gordian/gcosmos/* .
rm -rf github.com

# TODO: How do we do this within buf / proto directly?
# ideally this will fix the:
# `Error invoking method "gordian.server.v1.GordianGRPCService/GetBlocksWatermark": target server does not expose service "gordian.server.v1.GordianGRPCService"``
# issue?
# This does not happen with protoc.
find . -type f -name '*.pb.go' ! -path "*_cosmosvendor*" -exec sed -i -e 's|"github.com/gogo/protobuf/grpc"|"github.com/cosmos/gogoproto/grpc"|g' {} \;
find . -type f -name '*.pb.go' ! -path "*_cosmosvendor*" -exec sed -i -e 's|"github.com/gogo/protobuf/proto"|"github.com/cosmos/gogoproto/proto"|g' {} \;
