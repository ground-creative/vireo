# Common tasks. `make` alone lists them.
#
# Nothing here lists binaries: `build` delegates to scripts/build.sh, which
# discovers cmd/*. Adding a backend does not change this file.
.DEFAULT_GOAL := help
.PHONY: help build test vet fmt check certs run run-demo run-openclaw clean

help: ## list targets
	@grep -hE '^[a-z-]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  make %-14s %s\n", $$1, $$2}'

build: ## build every binary into ./bin
	@./scripts/build.sh

test: ## run the test suite
	@go test -count=1 ./...

vet: ## go vet
	@go vet ./...

fmt: ## gofmt the tree
	@gofmt -w .

check: fmt vet test ## what to run before pushing

certs: ## generate a development certificate if there is none
	@test -f certs/cert.pem && echo "certs/ already present" || ./scripts/gen-certs.sh

run: ## run a local stack (CONFIG=demo by default)
	@./scripts/dev.sh --config $(or $(CONFIG),demo)

run-demo: ## run the local stack against the stub backend
	@./scripts/dev.sh --config demo

run-openclaw: ## run the local stack against an OpenClaw gateway
	@./scripts/dev.sh --config openclaw

clean: ## remove built binaries and local certificates
	@rm -rf bin
	@rm -f certs/cert.pem certs/key.pem
	@echo "removed bin/ and local certs (run 'make certs' to regenerate)"
