#!/usr/bin/env bash
set -euo pipefail

repo_root=$(git rev-parse --show-toplevel)
manifest=${TOOLS_MANIFEST:-"$repo_root/.github/inputs/remote-content.json"}
download_dir=${TOOLS_DOWNLOAD_DIR:-"$HOME/.cache/iterabase/tools"}
install_dir=${TOOLS_INSTALL_DIR:-"$RUNNER_TEMP/iterabase-tools/bin"}

case "$(uname -s)-$(uname -m)" in
  Linux-x86_64) platform=linux-amd64 ;;
  Linux-aarch64|Linux-arm64) platform=linux-arm64 ;;
  *) echo "unsupported CI tool platform: $(uname -s)-$(uname -m)" >&2; exit 1 ;;
esac

mkdir -p "$download_dir" "$install_dir"

while IFS=$'\t' read -r tool version entry_platform checksum url; do
  artifact="$download_dir/${tool}-${version}-${platform}-${url##*/}"
  if [[ -f "$artifact" ]] && printf '%s  %s\n' "$checksum" "$artifact" | sha256sum --check --status; then
    source=cache
  else
    source=download
    rm -f "$artifact"
    python3 "$repo_root/.github/scripts/remote_content.py" validate >/dev/null
    python3 - "$repo_root" "$url" "$checksum" "$artifact" <<'PY'
import importlib.util
from pathlib import Path
import sys
spec = importlib.util.spec_from_file_location("remote_content", Path(sys.argv[1]) / ".github/scripts/remote_content.py")
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)
module.verified_download(sys.argv[2], sys.argv[3], Path(sys.argv[4]))
PY
  fi
  printf '%s  %s\n' "$checksum" "$artifact" | sha256sum --check --status || {
    rm -f "$artifact"
    echo "checksum verification failed for $tool $version ($platform)" >&2
    exit 1
  }
  echo "tool=$tool version=$version platform=$platform source=$source sha256=$checksum"

  case "$tool" in
    helm)
      tar -xzf "$artifact" -C "$install_dir" --strip-components=1 "$platform/helm"
      ;;
    kubeconform)
      tar -xzf "$artifact" -C "$install_dir" kubeconform
      ;;
    kind|kubectl)
      install -m 0755 "$artifact" "$install_dir/$tool"
      ;;
    *)
      echo "no installer registered for $tool" >&2
      exit 1
      ;;
  esac
done < <(jq -r --arg platform "$platform" '.tools[] | select(.platform == $platform) | [.name,.version,.platform,.sha256,.url] | @tsv' "$manifest")

for tool in helm kind kubectl kubeconform; do
  test -x "$install_dir/$tool" || { echo "$tool was not installed" >&2; exit 1; }
done

if [[ -n "${GITHUB_PATH:-}" ]]; then
  echo "$install_dir" >> "$GITHUB_PATH"
else
  echo "add to PATH: $install_dir"
fi
