# logscry — Makefile
# Standard targets for build, run, test, lint, and cross-compilation.

BINARY      := logscry
PKG         := ./cmd/logscry
BIN_DIR     := bin
CROSS_DIR   := $(BIN_DIR)/cross

# linux/darwin x amd64/arm64
PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64

.PHONY: all build run test lint fmt vet cross clean

all: build

build:
	@mkdir -p $(BIN_DIR)
	go build -o $(BIN_DIR)/$(BINARY) $(PKG)

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
		GOOS=$$os GOARCH=$$arch CGO_ENABLED=0 go build -o $$out $(PKG) || exit 1; \
	done

clean:
	rm -rf $(BIN_DIR)
