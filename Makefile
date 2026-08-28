SHELL := /bin/sh

PKG := github.com/nerdneilsfield/lark-cli-gateway
CMD := cmd/lark-gateway-server cmd/lark-gateway-cli
GOFILES := cmd internal

BINARIES := lark-gateway-server lark-gateway-cli
GORELEASER ?= goreleaser

.PHONY: all build fmt fmt-check lint vet test check release-check release-snapshot clean

all: build

build:
	go build -o lark-gateway-server ./cmd/lark-gateway-server
	go build -o lark-gateway-cli ./cmd/lark-gateway-cli

fmt:
	gofumpt -l -w $(GOFILES)
	goimports -w -local $(PKG) $(GOFILES)
	gofmt -w $(GOFILES)

fmt-check:
	@test -z "$$(gofumpt -l $(GOFILES))" || { echo "gofumpt: files need formatting"; exit 1; }
	@test -z "$$(goimports -l -local $(PKG) $(GOFILES))" || { echo "goimports: files need formatting"; exit 1; }
	@test -z "$$(gofmt -l $(GOFILES))" || { echo "gofmt: files need formatting"; exit 1; }

lint:
	golangci-lint run ./...

vet:
	go vet ./...

test:
	go test ./...

check: fmt-check lint vet test release-check

clean:
	rm -f $(BINARIES)
	rm -rf dist

release-check:
	$(GORELEASER) check

release-snapshot:
	$(GORELEASER) release --snapshot --clean
