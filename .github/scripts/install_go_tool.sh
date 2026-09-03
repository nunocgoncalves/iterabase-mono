#!/usr/bin/env bash
set -euo pipefail

name=${1:?usage: install_go_tool.sh goimports|golangci-lint}
repo_root=$(git rev-parse --show-toplevel)
case "$name" in
  goimports) package=golang.org/x/tools/cmd/goimports ;;
  golangci-lint) package=github.com/golangci/golangci-lint/v2/cmd/golangci-lint ;;
  *) echo "unreviewed Go tool: $name" >&2; exit 2 ;;
esac
(
  cd "$repo_root/.github/tools"
  GOWORK=off go mod verify
  GOWORK=off go install -mod=readonly "$package"
)
