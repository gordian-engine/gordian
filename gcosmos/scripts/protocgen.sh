#!/bin/sh
# sh ./scripts/protocgen.sh
#
# TODO: migrate to buf?

protoc --go_out=. --go-grpc_out=. proto/**/*.proto

cp -r ./github.com/rollchains/gordian/gcosmos/* .
rm -rf ./github.com
