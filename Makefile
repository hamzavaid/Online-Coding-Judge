.PHONY: test test-go test-web build fmt

# Explicit package roots avoid walking frontend dependencies on mounted filesystems.
test-go:
	go test -race ./cmd/... ./internal/... ./tests/...
	go vet ./cmd/... ./internal/... ./tests/...

test-web:
	cd web && npm test

test: test-go test-web

build:
	go build ./cmd/...
	cd web && npm run build

fmt:
	gofmt -w cmd internal tests
