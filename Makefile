# SPDX-License-Identifier: Apache-2.0
# Every target prints a one-line PASS/FAIL summary.
SHELL := /bin/bash
.SHELLFLAGS := -o pipefail -c
.DEFAULT_GOAL := help

PROJECT      := triage
# Release version shown in the acceptance report and the benchmark results.
RELEASE      ?= v0.1.0
MODULE       := github.com/thnhdatle/triage
VERSION      ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT       ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILD_DATE   ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS      := -s -w -X $(MODULE)/internal/platform/buildinfo.Version=$(VERSION) \
                -X $(MODULE)/internal/platform/buildinfo.Commit=$(COMMIT) \
                -X $(MODULE)/internal/platform/buildinfo.Date=$(BUILD_DATE)
GOFILES      := $(shell git ls-files '*.go' 2>/dev/null)
SHELLFILES   := $(shell git ls-files 'scripts/*.sh' '.githooks/*' 2>/dev/null)
CACHE_DIR    ?= $(HOME)/.cache/rayld/$(PROJECT)
FUZZTIME     ?= 30s
ONLY         ?=
export CACHE_DIR

# summary runs a command and prints "PASS <name>" or "FAIL <name>" (exit status preserved).
define summary
@if $(1); then echo "PASS $(2)"; else echo "FAIL $(2)"; exit 1; fi
endef

.PHONY: help
help:
	@grep -E '^[a-z-]+:.*##' $(MAKEFILE_LIST) | sed 's/:.*## /\t/' | sort

.PHONY: bootstrap
bootstrap: ## check tools, download modules, set up hooks, fetch pinned test artifacts
	$(call summary,scripts/bootstrap.sh,bootstrap)

.PHONY: generate
generate: ## code generation (none: no proto, no sqlc)
	@echo "PASS generate (nothing to generate)"

.PHONY: fmt
fmt: ## format all code
	@go tool gofumpt -w $(GOFILES) && go tool goimports -local $(MODULE) -w $(GOFILES) && \
		if [ -f research/pyproject.toml ]; then (cd research && uv run ruff format .); fi && echo "PASS fmt"

.PHONY: lint
lint: ## golangci-lint, ruff+mypy, hadolint, shellcheck, gitleaks, kubeconform
	$(call summary,scripts/lint.sh,lint)

.PHONY: test
test: ## unit tests with -race (goleak in TestMain of goroutine-starting packages)
	$(call summary,go test -race -count=1 ./...,test)

.PHONY: test-integration
test-integration: ## integration tests (Testcontainers), -count=1
	$(call summary,go test -race -count=1 -tags integration ./...,test-integration)

.PHONY: coverage
coverage: ## coverage with thresholds (80% overall, 90% for domain and app)
	$(call summary,scripts/coverage.sh,coverage)

.PHONY: mutation
mutation: ## gremlins on the core routing and privacy packages, score >= 60%
	$(call summary,scripts/mutation.sh,mutation)

.PHONY: acceptance
acceptance: ## acceptance suite, writes acceptance/report.md; ONLY=TRG-01,TRG-04 for a subset; CI=1 for CI thresholds
	@go run ./acceptance/cmd/runner --version "$(RELEASE)" --only "$(ONLY)"

.PHONY: bench
bench: ## benchmarks, writes bench/RESULTS.md
	$(call summary,env RELEASE=$(RELEASE) scripts/bench.sh,bench)

.PHONY: fuzz
fuzz: ## each fuzz target for FUZZTIME
	$(call summary,scripts/fuzz.sh $(FUZZTIME),fuzz)

.PHONY: examples
examples: ## run every example and check expected output
	$(call summary,scripts/examples.sh,examples)

.PHONY: quickstart-test
quickstart-test: ## clone to a temp dir and run the README quickstart verbatim
	$(call summary,scripts/quickstart-test.sh,quickstart-test)

.PHONY: security
security: ## govulncheck, go-licenses, trivy on local images
	$(call summary,scripts/security.sh,security)

.PHONY: build
build: ## binaries into ./bin
	$(call summary,CGO_ENABLED=1 go build -trimpath -ldflags '$(LDFLAGS)' -o bin/ ./cmd/...,build)

.PHONY: models
models: build ## pull and derive all pinned checkpoints (FP32 and INT8)
	$(call summary,./bin/triage models pull all --int8,models)

.PHONY: docker
docker: ## local images thnhdatle/{triage,fakellm,triage-models}:dev for the host arch
	$(call summary,scripts/docker.sh,docker)

.PHONY: up
up: ## compose stack up
	docker compose -f deploy/docker-compose.yml up -d --wait

.PHONY: down
down: ## compose stack down -v
	docker compose -f deploy/docker-compose.yml down -v --remove-orphans

.PHONY: ci
ci: ## lint test test-integration coverage examples security
	@$(MAKE) lint test test-integration coverage examples security && echo "PASS ci"

.PHONY: release-check
release-check: ## pre-release checks (run before tagging)
	$(call summary,scripts/release-check.sh,release-check)

.PHONY: release-dry
release-dry: ## goreleaser snapshot
	$(call summary,scripts/release-dry.sh,release-dry)
