export CGO_ENABLED := 0

VERSION ?= $(shell cat VERSION 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
LDFLAGS := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT)

.PHONY: build test lint fmt clean dist

build:
	go build -trimpath -ldflags '$(LDFLAGS)' -o bin/ ./cmd/lino ./cmd/lino-core

test:
	go test ./...

lint:
	@test -z "$$(gofmt -l .)" || (gofmt -l .; echo "gofmt needed"; exit 1)
	go vet ./...

fmt:
	gofmt -w .

dist:
	VERSION=$(VERSION) COMMIT=$(COMMIT) ./scripts/dist.sh

clean:
	rm -rf bin dist
