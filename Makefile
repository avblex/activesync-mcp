# Copyright (C) 2026 Henry Stern
# SPDX-License-Identifier: MIT

# activesync-mcp — top-level developer commands.
#
# `make` (no target) prints the help. The everyday targets:
#   make ci             run everything CI runs (gofmt + vet + tidy + vuln + race tests)
#   make integration    cross-platform integration tests (real OS surfaces, no server)
#   make e2e            end-to-end against the testenv Z-Push (Linux + docker)
#   make scenario       multi-call agent-shaped flows against the testenv

# Tools installed on demand into $GOPATH/bin (matches the CI behaviour).
GOBIN ?= $(shell go env GOPATH)/bin
GOVULNCHECK := $(GOBIN)/govulncheck

# The integration testenv lives in the sibling go-activesync repo. Set
# GO_ACTIVESYNC_DIR to override (default: ../go-activesync). STACK
# picks the testenv subdir (`zpush`, `zpush-2.6`, …); each stack
# defines its own port and device ID so the per-test config / env
# vars below can stay generic.
GO_ACTIVESYNC_DIR ?= ../go-activesync
STACK             ?= zpush

# Per-stack EAS endpoint + device ID. Keep in sync with
# go-activesync/.github/workflows/ci.yml's integration matrix.
ifeq ($(STACK),zpush-2.6)
EAS_INTEGRATION_URL    ?= http://localhost:8583/Microsoft-Server-ActiveSync
EAS_INTEGRATION_DEVICE ?= integration00000000000000000003
else
EAS_INTEGRATION_URL    ?= http://localhost:8580/Microsoft-Server-ActiveSync
EAS_INTEGRATION_DEVICE ?= integration00000000000000000000
endif

# Surfaced to the test process so e2eServerURL() and skipOnStack pick
# up the active stack.
E2E_ENV := \
    EAS_INTEGRATION_URL=$(EAS_INTEGRATION_URL) \
    EAS_INTEGRATION_DEVICE=$(EAS_INTEGRATION_DEVICE) \
    EAS_INTEGRATION_STACK=$(STACK)

.DEFAULT_GOAL := help

# ---------------------------------------------------------------------------
# Discoverable help (anything with `## ` after the target name shows up).
# ---------------------------------------------------------------------------

.PHONY: help
help:
	@awk 'BEGIN{FS=":.*## "} /^[a-zA-Z0-9_-]+:.*## / {printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST) | sort

# ---------------------------------------------------------------------------
# CI parity — run the same checks the GitHub workflow runs.
# ---------------------------------------------------------------------------

.PHONY: ci
ci: lint test ## Run everything CI runs (lint + race tests + coverage)

.PHONY: lint
lint: gofmt-check vet tidy-check vulncheck ## Static checks (no side effects)

.PHONY: gofmt-check
gofmt-check: ## Fail if any file would be modified by gofmt
	@out=$$({ find . -path ./vendor -prune -o -name '*.go' -print; \
		find vendor/github.com/hstern/go-activesync/eas -maxdepth 1 -name '*.go' -print; \
	} | xargs gofmt -l); \
	if [ -n "$$out" ]; then \
		echo "gofmt drift in:"; echo "$$out"; \
		echo "run: make fmt"; \
		exit 1; \
	fi

.PHONY: fmt
fmt: ## gofmt -w on every Go file
	find . -path ./vendor -prune -o -name '*.go' -print | xargs gofmt -w
	find vendor/github.com/hstern/go-activesync/eas -maxdepth 1 -name '*.go' -exec gofmt -w {} +

.PHONY: vet
vet: ## go vet
	go vet ./...

.PHONY: tidy-check
tidy-check: ## Fail if `go mod tidy` would change go.mod/go.sum
	@cp go.mod /tmp/go.mod.bak; cp go.sum /tmp/go.sum.bak; \
	go mod tidy; \
	if ! diff -q /tmp/go.mod.bak go.mod >/dev/null || ! diff -q /tmp/go.sum.bak go.sum >/dev/null; then \
		mv /tmp/go.mod.bak go.mod; mv /tmp/go.sum.bak go.sum; \
		echo "go.mod/go.sum out of sync; run: go mod tidy"; \
		exit 1; \
	fi; \
	rm -f /tmp/go.mod.bak /tmp/go.sum.bak

.PHONY: vulncheck
vulncheck: $(GOVULNCHECK) ## govulncheck
	$(GOVULNCHECK) ./...

$(GOVULNCHECK):
	go install golang.org/x/vuln/cmd/govulncheck@latest

.PHONY: build
build: ## Build ./activesync-mcp from cmd/activesync-mcp
	go build -o activesync-mcp ./cmd/activesync-mcp

.PHONY: test
test: ## Race detector + coverage (matches CI)
	go test -race -count=1 -covermode=atomic -coverprofile=coverage.out ./...
	go test -race -count=1 github.com/hstern/go-activesync/eas
	@go tool cover -func=coverage.out | tail -5

.PHONY: cover
cover: test ## Open coverage HTML in your browser
	go tool cover -html=coverage.out

# ---------------------------------------------------------------------------
# Tier 2: integration tests. Hits real OS surfaces (keyring, bbolt,
# subprocess spawn). Cross-platform; no docker / no Z-Push required.
# ---------------------------------------------------------------------------

.PHONY: integration
integration: ## Tier 2 — integration tests against real OS surfaces
	go test -tags integration -count=1 -timeout 5m ./...

# ---------------------------------------------------------------------------
# Tier 3: end-to-end tests. Spawns the binary, talks JSON-RPC over stdio
# against the go-activesync testenv (Z-Push docker stack). Linux only.
# ---------------------------------------------------------------------------

.PHONY: e2e
e2e: testenv-up ## Tier 3 — end-to-end binary spawn against testenv (STACK=zpush|zpush-2.6)
	$(E2E_ENV) go test -tags e2e -count=1 -timeout 10m ./...

# ---------------------------------------------------------------------------
# Tier 4: scenario tests. Multi-call agent-shaped flows. Linux only.
# ---------------------------------------------------------------------------

.PHONY: scenario
scenario: testenv-up ## Tier 4 — synthetic agent-shaped flows against testenv (STACK=zpush|zpush-2.6)
	$(E2E_ENV) go test -tags scenario -count=1 -timeout 10m ./...

# ---------------------------------------------------------------------------
# testenv plumbing — bring the sibling go-activesync testenv up / down.
# Idempotent: skips if the go-activesync sibling isn't present.
# ---------------------------------------------------------------------------

.PHONY: testenv-up
testenv-up: ## Bring the sibling go-activesync testenv up (STACK=zpush|zpush-2.6)
	@if [ ! -d "$(GO_ACTIVESYNC_DIR)/testenv/$(STACK)" ]; then \
		echo "$(GO_ACTIVESYNC_DIR)/testenv/$(STACK) not found"; \
		echo "clone github.com/hstern/go-activesync as a sibling, or set GO_ACTIVESYNC_DIR=/path/to/clone"; \
		exit 1; \
	fi
	$(MAKE) -C $(GO_ACTIVESYNC_DIR)/testenv STACK=$(STACK) up

.PHONY: testenv-down
testenv-down: ## Tear the sibling go-activesync testenv down (STACK=zpush|zpush-2.6)
	@if [ -d "$(GO_ACTIVESYNC_DIR)/testenv/$(STACK)" ]; then \
		$(MAKE) -C $(GO_ACTIVESYNC_DIR)/testenv STACK=$(STACK) down; \
	fi

# ---------------------------------------------------------------------------
# Housekeeping.
# ---------------------------------------------------------------------------

.PHONY: clean
clean: ## Remove built binary + coverage artefacts
	rm -f coverage.out coverage.html activesync-mcp

.PHONY: all
all: ci ## Run the full CI parity suite
