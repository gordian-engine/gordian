DOCKER := $(shell which docker)
protoVer=v5.0.0
protoImageName=rvolosatovs/protoc:$(protoVer)
protoImage=$(DOCKER) run --rm -v $(CURDIR):/workspace --workdir /workspace --entrypoint "" $(protoImageName)

proto-all: proto-lint proto-gen

proto-gen:
	@echo "Generating Protobuf files"
	@$(protoImage) sh ./scripts/protocgen.sh

proto-lint:
	@$(protoImage) protoc --lint_out=. proto/rollchains/gordian/externalsigner/*.proto
