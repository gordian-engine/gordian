#!/bin/sh
# sh ./scripts/protocgen.sh
#
# TODO: migrate to buf?

# Generates the *.go proto files from the *.proto files into a temparary `github.com/` directory.
# This directory structure is the `option go_package` string found in the proto file header.
protoc --go_out=. --go-grpc_out=. proto/**/*.proto

# Copies the files from the termporary `github.com/**` directory to the `gcosmos` directory.
# Given the go_package path matches gordian's directory structure, only changed files are copied.
# Then, the temporary `github.com/` directory is cleaned up.
cp -r ./github.com/rollchains/gordian/gcosmos/* .
rm -rf ./github.com
