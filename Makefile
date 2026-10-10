# A tagged commit builds as that version; anything else builds as "dev (commit)".
VERSION ?= $(shell git describe --tags --exact-match 2>/dev/null)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null)
LDFLAGS := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT)

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
