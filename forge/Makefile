.PHONY: build test test-unit test-e2e test-e2e-overlay test-e2e-secrets test-e2e-flux test-e2e-cert-issuers test-e2e-controlplane test-e2e-gpu test-e2e-gpu-broken-policy test-e2e-inference test-e2e-inference-gpu test-e2e-internal-tls test-e2e-tool-runner test-e2e-unit lint fmt fmt-check install-hooks clean

# Load .env if present (e.g. DIGITALOCEAN_TOKEN for e2e). .env is gitignored.
-include .env
export

BINARY := bin/forge
GO := go
GOBUILDFLAGS := -trimpath

# Every suite execution records an explicit fixture mode. CI may override these
# with candidate-plan or pinned-published inputs; there is no floating latest.
ITERABASE_E2E_FIXTURE_MODE ?= source
ITERABASE_E2E_SOURCE_SHA ?= $(shell git rev-parse HEAD)
ITERABASE_E2E_SOURCE_DIRTY ?= $(if $(shell git status --porcelain),true,false)
ITERABASE_E2E_PUBLISHED_FIXTURE ?= $(CURDIR)/test/e2e/published-fixture.json
ITERABASE_E2E_SOURCE_INPUTS ?= $(ITERABASE_E2E_PUBLISHED_FIXTURE)
export ITERABASE_E2E_FIXTURE_MODE ITERABASE_E2E_SOURCE_SHA ITERABASE_E2E_SOURCE_DIRTY ITERABASE_E2E_PUBLISHED_FIXTURE ITERABASE_E2E_CANDIDATE_PLAN

VERSION := $(shell version="$$(git describe --tags --match 'forge-v*' --always --dirty 2>/dev/null)" && printf '%s\n' "$$version" | sed 's/^forge-//' || echo dev)
COMMIT  := $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE    := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -X github.com/nunocgoncalves/iterabase-mono/forge/internal/version.version=$(VERSION) \
           -X github.com/nunocgoncalves/iterabase-mono/forge/internal/version.commit=$(COMMIT) \
           -X github.com/nunocgoncalves/iterabase-mono/forge/internal/version.date=$(DATE)

build:
	$(GO) build $(GOBUILDFLAGS) -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/forge

test: test-unit

test-unit:
	$(GO) test -race -count=1 ./...

# The E2E module has one top-level TestE2E runner. -run selects an isolated
# scenario; -v is required so capacity skips and named stages remain visible.
test-e2e:
	cd test/e2e && ITERABASE_E2E_SOURCE_INPUTS="$(ITERABASE_E2E_SOURCE_INPUTS)" go test -v -race -count=1 -timeout 60m -run '^TestE2E$$/^digitalocean-cpu$$' .

# Compatibility aliases: overlay, secret-sync, and Flux now compose on the one
# CPU fixture instead of provisioning three additional droplets.
test-e2e-overlay test-e2e-secrets test-e2e-flux: test-e2e

test-e2e-gpu:
	cd test/e2e && ITERABASE_E2E_SOURCE_INPUTS="$(ITERABASE_E2E_SOURCE_INPUTS)" go test -v -race -count=1 -timeout 100m -run '^TestE2E$$/^digitalocean-gpu$$' .

# Intentional HOR-411 red proof. Build the same source with only the Forge
# deleteEmptyDir value mutated to false, require real capacity, and prove the
# normal green fixture catches the operator's local-storage deletion failure.
test-e2e-gpu-broken-policy:
	@output=$$(mktemp); trap 'rm -f "$$output"' EXIT; \
	set +e; (cd test/e2e && FORGE_E2E_BREAK_DELETE_EMPTYDIR=true FORGE_E2E_REQUIRE_CAPACITY=true \
		ITERABASE_E2E_SOURCE_INPUTS="$(ITERABASE_E2E_SOURCE_INPUTS)" \
		go test -v -race -count=1 -timeout 100m -run '^TestE2E$$/^digitalocean-gpu$$' .) >"$$output" 2>&1; status=$$?; set -e; \
	cat "$$output"; \
	if [ "$$status" -eq 0 ]; then echo "broken deleteEmptyDir policy unexpectedly passed" >&2; exit 1; fi; \
	grep -Fq 'building intentional HOR-411 mutation with deleteEmptyDir=false' "$$output"; \
	grep -Fq 'baseline GPU workload ready:' "$$output"; \
	grep -Fq 'deleteEmptyDir: false' "$$output"; \
	grep -Fq 'pod-deletion-required' "$$output"; \
	grep -Fq 'upgrade-failed' "$$output"

# Compatibility alias: real inference now follows GPU smoke on the same VM.
test-e2e-inference-gpu: test-e2e-gpu

test-e2e-cert-issuers:
	cd test/e2e && go test -v -race -count=1 -timeout 15m -run '^TestE2E$$/^kind-cert-issuers$$' .

test-e2e-controlplane:
	cd test/e2e && go test -v -race -count=1 -timeout 15m -run '^TestE2E$$/^kind-controlplane-identity$$' .

test-e2e-internal-tls:
	cd test/e2e && go test -v -race -count=1 -timeout 20m -run '^TestE2E$$/^kind-internal-tls$$' .

test-e2e-tool-runner:
	cd test/e2e && go test -v -race -count=1 -timeout 30m -run '^TestE2E$$/^kind-tool-runner-contract$$' .

test-e2e-inference:
	cd test/e2e && go test -v -race -count=1 -timeout 15m -run '^TestE2E$$/^kind-inference-contract$$' .

# Run the hermetic shared-runner scenario, package-level pure tests excluding
# infrastructure scenarios, and pure Kind/chart helper tests.
test-e2e-unit:
	cd test/e2e && go test -race -count=1 -run '^TestE2E$$/^hermetic-example$$' .
	cd test/e2e && go test -race -count=1 -skip '^TestE2E$$' .
	cd test/e2e && go test -race -count=1 ./internal/...

lint:
	golangci-lint run ./...

fmt:
	$(GO) fmt ./...

fmt-check:
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

install-hooks:
	git config core.hooksPath .githooks

clean:
	rm -rf bin/
	$(GO) clean -testcache
