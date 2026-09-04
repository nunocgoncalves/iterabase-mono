#!/usr/bin/env bash
set -euo pipefail

name=${1:?usage: install_go_tool.sh TOOL}
repo_root=$(git rev-parse --show-toplevel)
case "$name" in
  goimports) module_dir=.github/tools; package=golang.org/x/tools/cmd/goimports; variable=GOIMPORTS ;;
  golangci-lint) module_dir=.github/tools; package=github.com/golangci/golangci-lint/v2/cmd/golangci-lint; variable=GOLANGCI_LINT ;;
  buf) module_dir=.github/tools/control-plane; package=github.com/bufbuild/buf/cmd/buf; variable=BUF ;;
  protoc-gen-go) module_dir=.github/tools/control-plane; package=google.golang.org/protobuf/cmd/protoc-gen-go; variable=PROTOC_GEN_GO ;;
  protoc-gen-connect-go) module_dir=.github/tools/control-plane; package=connectrpc.com/connect/cmd/protoc-gen-connect-go; variable=PROTOC_GEN_CONNECT_GO ;;
  controller-gen) module_dir=.github/tools/control-plane; package=sigs.k8s.io/controller-tools/cmd/controller-gen; variable=CONTROLLER_GEN ;;
  setup-envtest) module_dir=.github/tools/control-plane; package=sigs.k8s.io/controller-runtime/tools/setup-envtest; variable=ENVTEST ;;
  kustomize) module_dir=.github/tools/control-plane; package=sigs.k8s.io/kustomize/kustomize/v5; variable=KUSTOMIZE ;;
  *) echo "unreviewed Go tool: $name" >&2; exit 2 ;;
esac
install_dir=$(GOWORK=off go env GOPATH)/bin
(
  cd "$repo_root/$module_dir"
  GOWORK=off go mod verify
  GOWORK=off GOBIN="$install_dir" go install -mod=readonly "$package"
)
test -x "$install_dir/$name"
if [[ -n ${GITHUB_ENV:-} ]]; then
  printf '%s=%s\n' "$variable" "$install_dir/$name" >> "$GITHUB_ENV"
  echo 'ITERABASE_CI_EXACT_TOOLS=true' >> "$GITHUB_ENV"
fi
