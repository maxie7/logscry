# logscry — Makefile
# Standard targets for build, run, test, lint, and cross-compilation.

BINARY      := logscry
PKG         := ./cmd/logscry
BIN_DIR     := bin
CROSS_DIR   := $(BIN_DIR)/cross

# linux/darwin x amd64/arm64
PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64

# Stamp the version into the binary. Resolution order: an explicit VERSION (command line
# or env) verbatim, then `git describe`, then nothing — an unstamped binary falls back to
# BuildInfo and then "dev" in cmd/logscry/version.go, which is the single source of truth
# for that fallback, so never inject a literal here. `go install ...@latest` never runs
# this Makefile and relies on that same BuildInfo path.
# Building from a release source archive? `make build VERSION=v0.7.0` stamps it exactly.
GIT_VERSION := $(shell git describe --tags --always --dirty 2>/dev/null)
VERSION     := $(or $(strip $(VERSION)),$(strip $(GIT_VERSION)))
LDFLAGS     := $(if $(VERSION),-ldflags "-X main.version=$(VERSION)")

.PHONY: all build run test lint fmt vet cross clean

all: build

build:
	@mkdir -p $(BIN_DIR)
	go build $(LDFLAGS) -o $(BIN_DIR)/$(BINARY) $(PKG)

run: build
	./$(BIN_DIR)/$(BINARY)

test:
	go test ./...

fmt:
	gofmt -l -w .

vet:
	go vet ./...

lint:
	@command -v golangci-lint >/dev/null 2>&1 || { \
		echo "golangci-lint not installed — see https://golangci-lint.run/usage/install/"; exit 1; }
	golangci-lint run

cross:
	@mkdir -p $(CROSS_DIR)
	@for platform in $(PLATFORMS); do \
		os=$${platform%/*}; arch=$${platform#*/}; \
		out=$(CROSS_DIR)/$(BINARY)-$$os-$$arch; \
		echo "building $$out"; \
		GOOS=$$os GOARCH=$$arch CGO_ENABLED=0 go build $(LDFLAGS) -o $$out $(PKG) || exit 1; \
	done

clean:
	rm -rf $(BIN_DIR)
