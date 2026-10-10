VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build test lint run snapshot clean

build:
	go build -ldflags "$(LDFLAGS)" -o bin/bauddy ./cmd/bauddy

test:
	go test -race ./...

lint:
	go vet ./...

run: build
	./bin/bauddy

snapshot:
	go run github.com/goreleaser/goreleaser/v2@latest release --snapshot --clean

clean:
	rm -rf bin dist
