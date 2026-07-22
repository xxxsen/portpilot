SHELL := /usr/bin/env bash
.DEFAULT_GOAL := help

PROJECT_ROOT := $(abspath $(dir $(lastword $(MAKEFILE_LIST))))
LOCAL_GO := $(PROJECT_ROOT)/.tools/go/bin/go
ifneq ($(wildcard $(LOCAL_GO)),)
GO := $(LOCAL_GO)
else
GO ?= $(shell command -v go 2>/dev/null)
ifeq ($(strip $(GO)),)
GO := $(LOCAL_GO)
endif
endif
unexport GOROOT
GO_TOOLCHAIN_VERSION := $(shell env -u GOROOT "$(GO)" env GOVERSION 2>/dev/null)
export GOCACHE := $(PROJECT_ROOT)/.cache/go-build/$(if $(GO_TOOLCHAIN_VERSION),$(GO_TOOLCHAIN_VERSION),unknown)
export PATH := $(dir $(GO)):$(PATH)

GO_PKGS := ./cmd/... ./internal/... ./migrations/... ./test/integration/... ./test/fixtures/... ./test/acceptance/...
SERVER_BIN ?= portpilot-server
CLIENT_BIN ?= portpilot-client
GOLANGCI_LINT_VERSION ?= v2.12.2
GOFUMPT_VERSION ?= v0.9.2
GOIMPORTS_VERSION ?= v0.43.0
SHFMT_VERSION ?= v3.13.1
VERSION ?= dev
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || printf unknown)
BUILD_TIME ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w \
	-X github.com/portpilot/portpilot/internal/version.Version=$(VERSION) \
	-X github.com/portpilot/portpilot/internal/version.Commit=$(COMMIT) \
	-X github.com/portpilot/portpilot/internal/version.BuildTime=$(BUILD_TIME)

SERVER_IMAGE ?= portpilot-server
CLIENT_IMAGE ?= portpilot-client
WEB_IMAGE ?= portpilot-web
IMAGE_TAG ?= latest
IMAGE_PLATFORM ?= linux/amd64
ACCEPT_SOAK_DURATION ?= 15m
ACCEPT_CHAOS_INTERVAL ?= 1m
ACCEPT_DEV_READY_TIMEOUT_SECONDS ?= 180
ACCEPT_TCP_CONCURRENCY ?= 128
ACCEPT_TCP_HOLD_DURATION ?= 20s
ACCEPT_TCP_CYCLES ?= 1000
ACCEPT_UDP_FLOWS ?= 128
ACCEPT_UDP_DATAGRAMS ?= 10000

.PHONY: help doctor bootstrap web-install build build-server build-client \
	web-build test test-race integration-test lint lint-go lint-shell migration-check \
	config-check dockerfile-lint web-lint web-typecheck web-test web-check \
	web-e2e dev smoke-dev fault-test soak-test analytics-benchmark ci acceptance clean \
	clean-dev-data build-server-image build-client-image build-web-image build-images

help: ## Show available targets
	@awk 'BEGIN {FS = ":.*## "; printf "PortPilot targets:\n"} /^[a-zA-Z0-9_.-]+:.*## / {printf "  %-24s %s\n", $$1, $$2}' $(MAKEFILE_LIST)

doctor: ## Check development tools and local ports
	@command -v bash >/dev/null
	@test -x "$(GO)" || { echo "Go not found: $(GO)" >&2; exit 1; }
	@command -v node >/dev/null
	@command -v npm >/dev/null
	@command -v curl >/dev/null
	@command -v jq >/dev/null
	@command -v install >/dev/null
	@command -v sha256sum >/dev/null
	@command -v tar >/dev/null
	@"$(GO)" env GOVERSION | grep -Eq "^go1\.26\."
	@node --version | grep -Eq "^v20\."
	@for tool in golangci-lint gofumpt goimports shfmt shellcheck hadolint; do \
		test -x "$(PROJECT_ROOT)/bin/$${tool}" || { echo "Missing pinned tool bin/$${tool}; run make bootstrap" >&2; exit 1; }; \
	done
	@"$(PROJECT_ROOT)/bin/golangci-lint" version
	@"$(PROJECT_ROOT)/bin/shellcheck" --version | grep -F "version: 0.11.0"
	@"$(PROJECT_ROOT)/bin/hadolint" --version
	@for port in 3000 5100 6100 7000 7001 7002 7003 7004 18080 18081 18082; do \
		if ss -H -ltnu 2>/dev/null | awk '{print $$5}' | grep -Eq ":$${port}$$"; then \
			echo "Port $${port} is already in use" >&2; exit 1; \
		fi; \
	done

bootstrap: web-install ## Install pinned repository-managed tools
	"$(PROJECT_ROOT)/scripts/bootstrap-tools.sh"
	GOBIN="$(PROJECT_ROOT)/bin" "$(GO)" install mvdan.cc/gofumpt@$(GOFUMPT_VERSION)
	GOBIN="$(PROJECT_ROOT)/bin" "$(GO)" install golang.org/x/tools/cmd/goimports@$(GOIMPORTS_VERSION)
	GOBIN="$(PROJECT_ROOT)/bin" "$(GO)" install mvdan.cc/sh/v3/cmd/shfmt@$(SHFMT_VERSION)
	GOBIN="$(PROJECT_ROOT)/bin" "$(GO)" install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

web-install: ## Install exact Web dependencies from package-lock.json
	"$(PROJECT_ROOT)/scripts/web-install.sh"

build: build-server build-client web-build ## Build server, client, and Web

build-server: ## Build the server binary
	mkdir -p "$(PROJECT_ROOT)/bin"
	CGO_ENABLED=0 "$(GO)" build -trimpath -ldflags "$(LDFLAGS)" -o "$(PROJECT_ROOT)/bin/$(SERVER_BIN)" ./cmd/portpilot-server

build-client: ## Build the client binary
	mkdir -p "$(PROJECT_ROOT)/bin"
	CGO_ENABLED=0 "$(GO)" build -trimpath -ldflags "$(LDFLAGS)" -o "$(PROJECT_ROOT)/bin/$(CLIENT_BIN)" ./cmd/portpilot-client

web-build: web-install ## Build the Next.js production application
	npm --prefix "$(PROJECT_ROOT)/web" run build

test: ## Run Go unit tests
	"$(GO)" test $(GO_PKGS)

test-race: ## Run Go tests with the race detector
	"$(GO)" test -race $(GO_PKGS)

integration-test: ## Run Go integration tests with real loopback sockets
	"$(GO)" test -count=1 ./test/integration/...

lint: lint-go web-lint web-typecheck lint-shell config-check dockerfile-lint migration-check ## Run all static gates

lint-go: ## Run formatting, module, vet, and golangci-lint gates
	@files="$$(find cmd internal migrations test -type f -name "*.go" -print)"; \
		unformatted="$$($(PROJECT_ROOT)/bin/gofumpt -l $${files})"; \
		test -z "$${unformatted}" || { printf "gofumpt required:\n%s\n" "$${unformatted}" >&2; exit 1; }; \
		unorganized="$$($(PROJECT_ROOT)/bin/goimports -l $${files})"; \
		test -z "$${unorganized}" || { printf "goimports required:\n%s\n" "$${unorganized}" >&2; exit 1; }
	"$(GO)" mod tidy -diff
	"$(GO)" vet $(GO_PKGS)
	"$(PROJECT_ROOT)/bin/golangci-lint" run --config .golangci.yml $(GO_PKGS)

lint-shell: ## Validate repository shell scripts
	@for file in scripts/*.sh; do bash -n "$${file}"; done
	"$(PROJECT_ROOT)/bin/shellcheck" scripts/*.sh
	"$(PROJECT_ROOT)/bin/shfmt" -d -i 2 -ci scripts/*.sh

config-check: ## Validate all development configuration files
	"$(GO)" run ./cmd/portpilot-server validate-config --config configs/dev/server.json
	"$(GO)" run ./cmd/portpilot-client validate-config --config configs/dev/client.json
	jq -e . configs/dev/server.json configs/dev/clients.json configs/dev/client.json >/dev/null
	"$(PROJECT_ROOT)/scripts/check-nginx.sh"

migration-check: ## Validate embedded migrations, naming, and DDL ownership
	@invalid="$$(find migrations -maxdepth 1 -type f -name "*.sql" -printf "%f\n" | grep -Ev '^[0-9]{4}_[a-z0-9_]+\.sql$$' || true)"; \
		test -z "$${invalid}" || { printf "Invalid migration filename(s):\n%s\n" "$${invalid}" >&2; exit 1; }
	"$(GO)" test ./migrations/... ./internal/store -run 'Migration|DDL|Schema'
	@set +e; \
		matches="$$(grep -RInE --include="*.go" --exclude="*_test.go" \
			'CREATE[[:space:]]+(TABLE|INDEX|VIEW|TRIGGER)|ALTER[[:space:]]+TABLE|DROP[[:space:]]+TABLE' \
			cmd internal 2>&1)"; \
		status=$$?; \
		set -e; \
		if ((status > 1)); then printf "%s\n" "$${matches}" >&2; exit "$${status}"; fi; \
		matches="$$(printf "%s\n" "$${matches}" | awk '!/schema_migrations/')"; \
		test -z "$${matches}" || { printf "Business DDL must live in migrations/:\n%s\n" "$${matches}" >&2; exit 1; }

dockerfile-lint: ## Statically validate Dockerfiles without running containers
	"$(PROJECT_ROOT)/bin/hadolint" docker/Dockerfile.server docker/Dockerfile.client web/Dockerfile

web-lint: web-install ## Run Web ESLint with zero warnings
	npm --prefix "$(PROJECT_ROOT)/web" run lint
	@set +e; \
		matches="$$(grep -RInE --exclude-dir=node_modules --exclude-dir=.next --exclude-dir=playwright-report \
			'window\.(prompt|confirm|alert)[[:space:]]*\(' web 2>&1)"; \
		status=$$?; \
		set -e; \
		if ((status > 1)); then printf "%s\n" "$${matches}" >&2; exit "$${status}"; fi; \
		test -z "$${matches}" || { printf "Native browser dialogs are forbidden:\n%s\n" "$${matches}" >&2; exit 1; }

web-typecheck: web-install ## Run strict TypeScript checks
	npm --prefix "$(PROJECT_ROOT)/web" run typecheck

web-test: web-install ## Run Web unit tests
	TMPDIR=/tmp npm --prefix "$(PROJECT_ROOT)/web" run test:ci

web-check: web-lint web-typecheck web-test web-build ## Run all Web gates

web-e2e: web-install ## Run Playwright against an active make dev environment
	npm --prefix "$(PROJECT_ROOT)/web" run test:e2e

dev: web-install ## Start the real server, client, Web, and echo fixture
	"$(PROJECT_ROOT)/scripts/dev.sh"

smoke-dev: ## Verify an active make dev environment
	"$(PROJECT_ROOT)/scripts/smoke-dev.sh"

fault-test: ## Inject session, ticket, frame, target, and resource faults into make dev
	"$(PROJECT_ROOT)/scripts/fault-test.sh"

soak-test: ## Run concurrent relay load against make dev
	ACCEPT_SOAK_DURATION="$(ACCEPT_SOAK_DURATION)" ACCEPT_CHAOS_INTERVAL="$(ACCEPT_CHAOS_INTERVAL)" \
		ACCEPT_TCP_CONCURRENCY="$(ACCEPT_TCP_CONCURRENCY)" ACCEPT_TCP_HOLD_DURATION="$(ACCEPT_TCP_HOLD_DURATION)" \
		ACCEPT_TCP_CYCLES="$(ACCEPT_TCP_CYCLES)" ACCEPT_UDP_FLOWS="$(ACCEPT_UDP_FLOWS)" \
		ACCEPT_UDP_DATAGRAMS="$(ACCEPT_UDP_DATAGRAMS)" "$(PROJECT_ROOT)/scripts/soak-test.sh"

analytics-benchmark: ## Benchmark historical analytics at reference cardinality
	"$(GO)" test ./internal/store -run '^$$' \
		-bench '^BenchmarkNetworkAnalyticsReferenceScale$$' -benchtime=5x -benchmem

ci: lint build test test-race web-test integration-test ## Run the deterministic CI gate

acceptance: ## Run full-service merge acceptance against make dev
	ACCEPT_SOAK_DURATION="$(ACCEPT_SOAK_DURATION)" ACCEPT_CHAOS_INTERVAL="$(ACCEPT_CHAOS_INTERVAL)" \
		ACCEPT_DEV_READY_TIMEOUT_SECONDS="$(ACCEPT_DEV_READY_TIMEOUT_SECONDS)" \
		"$(PROJECT_ROOT)/scripts/acceptance.sh"

build-server-image: ## Build portpilot-server; never starts the image
	docker buildx build --load --platform "$(IMAGE_PLATFORM)" \
		--build-arg VERSION="$(VERSION)" --build-arg COMMIT="$(COMMIT)" --build-arg BUILD_TIME="$(BUILD_TIME)" \
		-t "$(SERVER_IMAGE):$(IMAGE_TAG)" -f docker/Dockerfile.server .

build-client-image: ## Build portpilot-client; never starts the image
	docker buildx build --load --platform "$(IMAGE_PLATFORM)" \
		--build-arg VERSION="$(VERSION)" --build-arg COMMIT="$(COMMIT)" --build-arg BUILD_TIME="$(BUILD_TIME)" \
		-t "$(CLIENT_IMAGE):$(IMAGE_TAG)" -f docker/Dockerfile.client .

build-web-image: ## Build portpilot-web; never starts the image
	docker buildx build --load --platform "$(IMAGE_PLATFORM)" \
		-t "$(WEB_IMAGE):$(IMAGE_TAG)" -f web/Dockerfile .

build-images: build-server-image build-client-image build-web-image ## Build all three images without running them

clean: ## Remove build output while preserving runtime data
	rm -rf "$(PROJECT_ROOT)/bin" "$(PROJECT_ROOT)/web/.next"

clean-dev-data: ## Remove local runtime data after explicit confirmation
	@test "$(CONFIRM)" = "yes" || { echo "Run make clean-dev-data CONFIRM=yes" >&2; exit 1; }
	rm -rf "$(PROJECT_ROOT)/.dev-data"
