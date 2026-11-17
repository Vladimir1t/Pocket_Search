.PHONY: run test check bench build fmt

run:
	go run ./cmd/pocketsearch

test:
	go test -race -count=1 ./...

fmt:
	gofmt -l -w .

check:
	@test -z "$$(gofmt -l .)" || { echo "gofmt needed:"; gofmt -l .; exit 1; }
	go vet ./...
	go test -race -count=1 ./...

bench:
	go test ./internal/search -run '^$$' -bench . -benchmem

build:
	go build -o bin/pocketsearch ./cmd/pocketsearch
