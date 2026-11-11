BINARY      := dra-migrate
PKG         := github.com/cb-Sigma/dra-migrate
VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT      ?= $(shell git rev-parse --short HEAD 2>/dev/null)
DATE        ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS     := -s -w -X $(PKG)/internal/version.Version=$(VERSION) -X $(PKG)/internal/version.Commit=$(COMMIT) -X $(PKG)/internal/version.Date=$(DATE)
K8S_VERSION ?= 1.34.1

# Converted fixtures that only contain kinds a bare API server knows.
DRY_RUN_FILES = examples/after/*.yaml $(filter-out testdata/golden/custom-resource/% testdata/golden/helm-rendered/%,$(wildcard testdata/golden/*/expected.yaml))

.PHONY: all
all: fmt vet lint test build

.PHONY: build
build: ## Build a static binary into bin/
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o bin/$(BINARY) ./cmd/dra-migrate

.PHONY: install
install: ## Install into GOBIN
	CGO_ENABLED=0 go install -trimpath -ldflags '$(LDFLAGS)' ./cmd/dra-migrate

.PHONY: test
test: ## Run unit and golden tests with the race detector
	go test -race ./...

.PHONY: cover
cover: ## Write a coverage report to coverage.html
	go test -race -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html

.PHONY: golden
golden: ## Regenerate testdata/golden/*/expected.* (review the diff!)
	go test ./internal/cli -run TestGolden -update

.PHONY: examples
examples: build ## Regenerate examples/after from examples/before
	rm -rf examples/after
	bin/$(BINARY) convert --quiet --output-dir examples/after examples/before

.PHONY: fmt
fmt: ## Format Go code
	gofmt -s -w cmd internal

.PHONY: vet
vet: ## Run go vet
	go vet ./...

.PHONY: lint
lint: ## Run golangci-lint
	golangci-lint run ./...

.PHONY: dry-run
dry-run: ## Server-side dry-run of all converted fixtures against a throwaway kube-apiserver
	K8S_VERSION=$(K8S_VERSION) RUNTIME_CONFIG=resource.k8s.io/v1beta1=true,resource.k8s.io/v1beta2=true \
		hack/with-envtest.sh hack/server-dry-run.sh $(DRY_RUN_FILES)

.PHONY: clean
clean: ## Remove build output
	rm -rf bin dist coverage.out coverage.html

.PHONY: help
help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-10s %s\n", $$1, $$2}'
