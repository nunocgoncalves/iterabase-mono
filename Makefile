SHELL := /usr/bin/env bash
.SHELLFLAGS := -euo pipefail -c

GO_MODULES := control-plane inference-gateway forge forge/test/e2e
GO_MODULE_FILES := $(foreach module,$(GO_MODULES),$(module)/go.mod $(module)/go.sum)
WORKSPACE_FILES := go.work go.work.sum $(GO_MODULE_FILES)
CONTAINER_TOOL ?= docker

.PHONY: workspace-sync workspace-check workspace-list fmt-check vet build test lint codegen-check charts-check release-check release-security-audit source-authority-audit docker-build check install-hooks pre-commit clean

workspace-sync:
	go work sync

workspace-check:
	@before="$$(shasum $(WORKSPACE_FILES))"; \
	go work sync; \
	after="$$(shasum $(WORKSPACE_FILES))"; \
	if [[ "$$before" != "$$after" ]]; then \
		echo "go work sync changed module metadata; commit the synchronized files"; \
		exit 1; \
	fi
	@$(MAKE) --no-print-directory workspace-list

workspace-list:
	@for module in $(GO_MODULES); do \
		echo ":: independent go list $$module"; \
		(cd "$$module" && GOWORK=off go list ./... >/dev/null); \
	done

fmt-check:
	@for module in control-plane inference-gateway forge; do \
		echo ":: fmt-check $$module"; \
		$(MAKE) -C "$$module" fmt-check; \
	done

vet:
	@for module in $(GO_MODULES); do \
		echo ":: go vet $$module"; \
		(cd "$$module" && go vet ./...); \
	done

build:
	$(MAKE) -C control-plane ui-deps build
	$(MAKE) -C inference-gateway build
	$(MAKE) -C forge build

# Full local component tests. Docker is required by control-plane and
# inference-gateway integration tests; infrastructure E2E scenarios remain
# explicit Forge targets and are not run here.
test:
	$(MAKE) -C control-plane ui-deps ui-test harness-deps harness-test tool-runner-deps tool-runner-test test
	$(MAKE) -C inference-gateway test
	$(MAKE) -C forge test test-e2e-unit

lint:
	$(MAKE) -C control-plane ui-deps ui-lint harness-deps harness-lint tool-runner-deps tool-runner-lint lint
	$(MAKE) -C inference-gateway lint
	$(MAKE) -C forge lint
	cd forge/test/e2e && golangci-lint run ./...

codegen-check:
	$(MAKE) -C control-plane proto-check

charts-check:
	$(MAKE) -C charts check

release-check:
	python3 .github/scripts/test_release.py
	python3 .github/scripts/release.py validate

release-security-audit:
	.github/scripts/audit_release_security.sh

# Explicit, authenticated operational audit. This is intentionally outside the
# hermetic `check` matrix because it verifies live GitHub and optional registry state.
source-authority-audit:
	.github/scripts/audit_source_authority.sh "$${SOURCE_AUTHORITY_STATE:-pre-archive}"

# Preserve the existing runtime image names while using component-scoped
# monorepo build contexts.
docker-build:
	$(CONTAINER_TOOL) build -t control-plane:latest control-plane
	$(CONTAINER_TOOL) build -t control-plane-harness:latest -f control-plane/harness/Dockerfile control-plane/harness
	$(CONTAINER_TOOL) build -t control-plane-harness-isolation:latest -f control-plane/harness/isolation/Dockerfile control-plane/harness
	$(CONTAINER_TOOL) build -t control-plane-tool-runner:latest -f control-plane/tool-runner/Dockerfile control-plane/tool-runner
	$(CONTAINER_TOOL) build -t inference-gateway:latest inference-gateway

check: workspace-check fmt-check vet build test lint codegen-check charts-check release-check docker-build

install-hooks:
	git config core.hooksPath .githooks

pre-commit: workspace-check fmt-check vet build lint release-check

clean:
	$(MAKE) -C control-plane clean
	$(MAKE) -C inference-gateway clean
	$(MAKE) -C forge clean
	$(MAKE) -C charts clean
