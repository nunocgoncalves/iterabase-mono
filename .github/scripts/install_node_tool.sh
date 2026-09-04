#!/usr/bin/env bash
set -euo pipefail

name=${1:?usage: install_node_tool.sh TOOL}
repo_root=$(git rev-parse --show-toplevel)
case "$name" in
  protoc-gen-es) tool_dir="$repo_root/.github/tools/protobuf"; variable=PROTOC_GEN_ES ;;
  *) echo "unreviewed Node tool: $name" >&2; exit 2 ;;
esac

npm ci --ignore-scripts --prefix "$tool_dir"
tool="$tool_dir/node_modules/.bin/$name"
test -x "$tool"
if [[ -n ${GITHUB_ENV:-} ]]; then
  printf '%s=%s\n' "$variable" "$tool" >> "$GITHUB_ENV"
  echo 'ITERABASE_CI_EXACT_TOOLS=true' >> "$GITHUB_ENV"
fi
if [[ -n ${GITHUB_PATH:-} ]]; then
  dirname "$tool" >> "$GITHUB_PATH"
fi
