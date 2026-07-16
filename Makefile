# TikTelemetry Makefile
#
# Common developer tasks. Run `make help` for a summary.

BINARY      := tiktelemetry
PKG         := ./cmd/tiktelemetry
VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS     := -s -w -X main.version=$(VERSION)
IMAGE       ?= ghcr.io/jutaz/tiktelemetry:latest
PLATFORMS   ?= linux/amd64,linux/arm64,linux/arm/v6

# Map a friendly arch name to Docker platform + tar suffix for `image-tar`.
# Override ARCH to build a single-arch tar, e.g. `make image-tar ARCH=arm64`.
ARCH        ?= arm64

.DEFAULT_GOAL := help

.PHONY: help
help: ## Show this help
	@grep -E '^[a-zA-Z0-9_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'

.PHONY: build
build: ## Build the binary for the host platform
	go build -ldflags="$(LDFLAGS)" -o $(BINARY) $(PKG)

.PHONY: build-arm
build-arm: ## Cross-compile a static ARM32 binary for MikroTik (GOARM=6)
	# GOARM=6, not 7: GOARM=7's VFPv3 float instructions SIGILL on several
	# MikroTik ARM SoCs. GOARM=6 runs on all of them except the ARMv5 EN7562CT
	# boards (hEX Refresh, hEX S 2025) — see build-arm5 — at no meaningful cost
	# for this workload. See the Dockerfile.
	CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=6 \
		go build -ldflags="$(LDFLAGS)" -o $(BINARY)-arm $(PKG)

.PHONY: build-arm5
build-arm5: ## Cross-compile a static ARMv5 binary for EN7562CT boards (GOARM=5)
	# For the FPU-less ARMv5 EN7562CT boards (hEX Refresh, hEX S 2025), which
	# SIGILL on the GOARM=6 build. Soft float; negligible cost for this agent.
	CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=5 \
		go build -ldflags="$(LDFLAGS)" -o $(BINARY)-armv5 $(PKG)

.PHONY: build-arm64
build-arm64: ## Cross-compile a static ARM64 binary
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 \
		go build -ldflags="$(LDFLAGS)" -o $(BINARY)-arm64 $(PKG)

.PHONY: test
test: ## Run unit tests
	go test ./... -count=1

.PHONY: test-race
test-race: ## Run unit tests with the race detector
	go test ./... -count=1 -race

.PHONY: cover
cover: ## Run unit tests and print per-package coverage
	go test ./... -count=1 -cover

.PHONY: cover-html
cover-html: ## Generate an HTML coverage report (coverage.html)
	go test ./... -count=1 -covermode=atomic -coverprofile=coverage.out
	go tool cover -html=coverage.out -o coverage.html
	@echo "wrote coverage.html"

.PHONY: test-e2e
test-e2e: ## Run end-to-end tests (starts RouterOS via testcontainers; needs Docker)
	go test -tags e2e -count=1 -v ./test/e2e/...

.PHONY: e2e-up
e2e-up: ## Start a long-lived RouterOS container for e2e runs
	docker compose -f docker-compose.e2e.yml up -d

.PHONY: e2e-down
e2e-down: ## Stop the long-lived RouterOS container
	docker compose -f docker-compose.e2e.yml down

.PHONY: vet
vet: ## Run go vet across all packages (including e2e-tagged files)
	go vet ./...
	go vet -tags e2e ./test/e2e/...

.PHONY: lint
lint: ## Run golangci-lint (install from https://golangci-lint.run if missing)
	golangci-lint run ./...
	golangci-lint run --build-tags e2e ./...

.PHONY: vuln
vuln: ## Scan for known vulnerabilities with govulncheck
	go run golang.org/x/vuln/cmd/govulncheck@latest ./...

.PHONY: docs
docs: ## Verify #sym: doc references against the code (needs Node/npx)
	npx --yes symtether@latest check --strict
	npx --yes symtether@latest update --check

.PHONY: docs-update
docs-update: ## Re-stamp symtether.sum after intentionally changing referenced code
	npx --yes symtether@latest update

.PHONY: fmt
fmt: ## Format all Go sources
	gofmt -w cmd internal test

.PHONY: fmt-check
fmt-check: ## Fail if any source is not gofmt-clean
	@unformatted=$$(gofmt -l cmd internal test); \
	if [ -n "$$unformatted" ]; then \
		echo "Not gofmt-clean:"; echo "$$unformatted"; exit 1; \
	fi

.PHONY: check
check: fmt vet lint docs test ## Format, vet, lint, verify docs, and unit-test

.PHONY: image
image: ## Build and push the multi-arch container image
	docker buildx build --platform $(PLATFORMS) --build-arg VERSION=$(VERSION) -t $(IMAGE) --push .

.PHONY: image-tar
image-tar: ## Build a single-arch image and save it as a .tar for offline RouterOS import (ARCH=arm|armv5|arm64|amd64)
	@case "$(ARCH)" in \
	  arm)   platform=linux/arm/v6 ;; \
	  armv5) platform=linux/arm/v5 ;; \
	  arm64) platform=linux/arm64 ;; \
	  amd64) platform=linux/amd64 ;; \
	  *) echo "unknown ARCH=$(ARCH) (use arm, armv5, arm64, or amd64)"; exit 1 ;; \
	esac; \
	echo "Building $(IMAGE) for $$platform ..."; \
	docker buildx build --platform $$platform --build-arg VERSION=$(VERSION) \
	  -t tiktelemetry:$(ARCH) --load . && \
	docker save tiktelemetry:$(ARCH) -o tiktelemetry-$(ARCH).tar && \
	echo "wrote tiktelemetry-$(ARCH).tar (upload this to the router for '/container/add file=...')"

.PHONY: tidy
tidy: ## Tidy go.mod / go.sum
	go mod tidy

.PHONY: run
run: ## Build and run locally (reads .env if present via your shell)
	go run $(PKG)

.PHONY: clean
clean: ## Remove build artifacts
	rm -f $(BINARY) $(BINARY)-arm $(BINARY)-armv5 $(BINARY)-arm64 coverage.out coverage.html tiktelemetry-*.tar
