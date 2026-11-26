GO ?= go
BIN := bin/mailcontroller
GOFLAGS := -trimpath
LDFLAGS := -s -w
BUNDLE_LIMIT := 102400

.PHONY: all build run test test-race lint fmt tidy vet bundle-size clean help

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

bundle-size: ## Check that the gzipped frontend stays under 100 KB
	@size=$$(find web -type f \( -name '*.html' -o -name '*.css' -o -name '*.js' -o -name '*.mjs' -o -name '*.svg' -o -name '*.png' \) \
		-print0 | sort -z | xargs -0 cat | gzip -9 | wc -c); \
	echo "Frontend bundle: $$size bytes gzipped (limit $(BUNDLE_LIMIT))"; \
	if [ "$$size" -ge "$(BUNDLE_LIMIT)" ]; then \
		echo "Frontend bundle exceeds the 100 KB target"; \
		exit 1; \
	fi

clean: ## Remove build artifacts
	rm -rf bin coverage.out coverage.html

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "%-12s %s\n", $$1, $$2}'
