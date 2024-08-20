CURR_DIR = $(shell pwd)

.PHONY: protoc-gen
protoc-gen:
	docker run --rm -v $(CURR_DIR):/workspace --workdir /workspace ghcr.io/cosmos/proto-builder:0.15.0 sh ./scripts/protocgen.sh
