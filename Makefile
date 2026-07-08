.PHONY: fmt test build run

fmt:
	gofmt -w ./cmd ./internal

test:
	go test ./...

build:
	go build ./cmd/vff-fiscal

run:
	go run ./cmd/vff-fiscal
