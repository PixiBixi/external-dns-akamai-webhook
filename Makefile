.PHONY: help build lint test cover image snapshot clean
.DEFAULT_GOAL := help

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
IMAGE   ?= ghcr.io/pixibixi/external-dns-akamai-webhook

build: ## Build the local binary
	go build -ldflags "-s -w -X main.version=$(VERSION)" -o external-dns-akamai-webhook .

lint: ## Run golangci-lint (config: .golangci.yml)
	golangci-lint run

test: ## Run tests with the race detector
	go test -race ./...

cover: ## Run tests and open the coverage report
	go test -race -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out

image: ## Build the container image from source
	docker build --build-arg VERSION=$(VERSION) -t $(IMAGE):$(VERSION) .

snapshot: ## Build a goreleaser snapshot (dry-run release, nothing published)
	goreleaser release --snapshot --clean

clean: ## Remove build artifacts
	rm -f external-dns-akamai-webhook coverage.out
	rm -rf dist/

help:
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-30s\033[0m %s\n", $$1, $$2}'
