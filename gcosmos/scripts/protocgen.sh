# protoc --go_out=. --go_opt=paths=source_relative --go-grpc_out=. --go-grpc_opt=paths=source_relative proto/**/*.proto
protoc --go_out=. --go-grpc_out=. proto/**/*.proto


# cargo install protoc-gen-tonic
mkdir -p rust
touch rust/server.rs
protoc -I proto --tonic_out=rust proto/**/*.proto
# protoc --rust_out=experimental-codegen=enabled,kernel=cpp:. proto/**/*.proto

# move all .rs and .cc files from within proto -> the rust/ dir
mv proto/**/*.rs proto/**/*.cc rust/

cp ./github.com/rollchains/gordian/gcosmos/gserver/internal/grpc/* ./gserver/internal/grpc
rm -rf ./github.com


# TODO: generate rust grpc client