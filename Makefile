GO ?= go
BIN := bin/mailcontroller
GOFLAGS := -trimpath
LDFLAGS := -s -w

.PHONY: all build run test test-race lint fmt tidy vet clean help

all: lint test build

build: ## Build the static binary
	CGO_ENABLED=0 $(GO) build $(GOFLAGS) -ldflags="$(LDFLAGS)" -o $(BIN) ./cmd/mailcontroller

run: ## Run the service locally
	$(GO) run ./cmd/mailcontroller

test: ## Run all tests
	$(GO) test ./...

test-race: ## Run all tests with the race detector
	$(GO) test -race ./...

lint: ## Run golangci-lint
	golangci-lint run

fmt: ## Format Go sources
	gofmt -w $$(find . -name '*.go' -not -path './vendor/*')

vet: ## Run go vet
	$(GO) vet ./...

tidy: ## Tidy module dependencies
	$(GO) mod tidy

clean: ## Remove build artifacts
	rm -rf bin coverage.out coverage.html

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "%-12s %s\n", $$1, $$2}'
