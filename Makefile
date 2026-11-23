BINARY  := linux-diag-mcp
PKG     := ./cmd/linux-diag-mcp
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

# Use the toolchain pinned in go.mod even when a newer local Go is first in
# PATH: analyzers like staticcheck must match the stdlib they parse.
export GOTOOLCHAIN ?= $(shell sed -n 's/^toolchain //p' go.mod)

.PHONY: build test lint run clean

build:
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o bin/$(BINARY) $(PKG)

test:
	go test -race -count=1 ./...

# go vet always runs; staticcheck and golangci-lint run only when installed.
lint:
	go vet ./...
	@if command -v staticcheck >/dev/null 2>&1; then staticcheck ./...; \
	 else echo "lint: staticcheck not installed, skipping" >&2; fi
	@if command -v golangci-lint >/dev/null 2>&1; then golangci-lint run ./...; \
	 else echo "lint: golangci-lint not installed, skipping" >&2; fi

run: build
	./bin/$(BINARY)

clean:
	rm -rf bin
