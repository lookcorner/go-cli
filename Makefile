.PHONY: build test vet check

VERSION ?= 0.1.0-dev

build:
	go build -ldflags "-X github.com/lookcorner/go-cli/internal/version.Current=$(VERSION)" -o gork ./cmd/gork

test:
	go test ./...

vet:
	go vet ./...

check: test vet
