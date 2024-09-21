module github.com/rollchains/gordian/gexternalsigner

go 1.23

require (
	github.com/rollchains/gordian v0.0.0
	google.golang.org/grpc v1.67.0
	google.golang.org/protobuf v1.34.2
)

replace github.com/rollchains/gordian => ../

require (
	github.com/bits-and-blooms/bitset v1.13.0 // indirect
	golang.org/x/net v0.29.0 // indirect
	golang.org/x/sys v0.25.0 // indirect
	golang.org/x/text v0.18.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20240814211410-ddb44dafa142 // indirect
)
