BINARY := relayward
GOEXE := $(shell go env GOEXE)
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

.PHONY: build test lint run clean

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=$(VERSION)" -o bin/$(BINARY)$(GOEXE) ./cmd/relayward

# The race detector requires cgo; a C compiler must be on PATH.
test:
	CGO_ENABLED=1 go test -race ./...

lint:
	golangci-lint run

run: build
	./bin/$(BINARY)$(GOEXE) serve -config config.yaml

clean:
	rm -rf bin
