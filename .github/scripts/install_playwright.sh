#!/usr/bin/env bash
set -euo pipefail

version=${1:?usage: install_playwright.sh VERSION}
repo_root=$(git rev-parse --show-toplevel)
manifest=${TOOLS_MANIFEST:-"$repo_root/.github/inputs/remote-content.json"}
download_dir=${TOOLS_DOWNLOAD_DIR:-"$HOME/.cache/iterabase/playwright"}
browsers_path=${PLAYWRIGHT_BROWSERS_PATH:-"$RUNNER_TEMP/iterabase-playwright"}

case "$(uname -s)-$(uname -m)" in
  Linux-x86_64) platform=linux-amd64 ;;
  *) echo "unsupported reviewed Playwright platform: $(uname -s)-$(uname -m)" >&2; exit 1 ;;
esac

python3 "$repo_root/.github/scripts/remote_content.py" validate >/dev/null
count=$(jq --arg version "$version" --arg platform "$platform" \
  '[.playwright_archives[] | select(.version == $version and .platform == $platform)] | length' "$manifest")
[[ "$count" == 3 ]] || { echo "Playwright archive set is missing or ambiguous: $version $platform" >&2; exit 1; }
mkdir -p "$download_dir" "$browsers_path"

while IFS=$'\t' read -r name revision checksum url directory executable installed_checksum; do
  artifact="$download_dir/$checksum-${url##*/}"
  if [[ -f "$artifact" ]] && printf '%s  %s\n' "$checksum" "$artifact" | sha256sum --check --status; then
    source=cache
  else
    source=download
    rm -f "$artifact"
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
    echo "checksum verification failed for Playwright $name $revision" >&2
    exit 1
  }
  destination="$browsers_path/$directory"
  rm -rf "$destination"
  mkdir -p "$destination"
  unzip -q "$artifact" -d "$destination"
  printf '%s  %s\n' "$installed_checksum" "$destination/$executable" | sha256sum --check --status || {
    rm -rf "$destination"
    echo "extracted executable verification failed for Playwright $name $revision" >&2
    exit 1
  }
  touch "$destination/INSTALLATION_COMPLETE"
  echo "playwright=$name revision=$revision platform=$platform source=$source sha256=$checksum"
done < <(jq -r --arg version "$version" --arg platform "$platform" \
  '.playwright_archives[] | select(.version == $version and .platform == $platform) | [.name,.revision,.sha256,.url,.directory,.executable,.installed_sha256] | @tsv' "$manifest")

if [[ -n "${GITHUB_ENV:-}" ]]; then
  echo "PLAYWRIGHT_BROWSERS_PATH=$browsers_path" >> "$GITHUB_ENV"
fi
