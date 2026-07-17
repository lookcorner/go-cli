.PHONY: build test vet check

build:
	go build -o gork ./cmd/gork

test:
	go test ./...

vet:
	go vet ./...

check: test vet
