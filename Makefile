# CGO is off everywhere: Yumlab ships as a single static binary so it runs in
# any CI container, including distroless and Alpine images.
export CGO_ENABLED := 0

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: all build test check fmt vet cover clean install

all: check build

build:
	go build -ldflags '$(LDFLAGS)' -o bin/yumlab ./cmd/yumlab

install:
	go install -ldflags '$(LDFLAGS)' ./cmd/yumlab

test:
	go test ./...

cover:
	go test -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out | tail -1

fmt:
	gofmt -l -w .

vet:
	go vet ./...

check: vet test
	@test -z "$$(gofmt -l .)" || { echo "gofmt needed:"; gofmt -l .; exit 1; }

clean:
	rm -rf bin coverage.out dist
