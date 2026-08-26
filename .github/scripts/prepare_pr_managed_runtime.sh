#!/usr/bin/env bash
set -euo pipefail

candidate_dir=${1:?usage: prepare_pr_managed_runtime.sh CANDIDATE_DIR ENV_OUTPUT}
env_output=${2:?usage: prepare_pr_managed_runtime.sh CANDIDATE_DIR ENV_OUTPUT}
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
candidate_dir="$(python3 - "$candidate_dir" <<'PY'
import os
import sys
print(os.path.abspath(sys.argv[1]))
PY
)"

rm -rf "$candidate_dir"
mkdir -p "$candidate_dir"
"$root/.github/scripts/add_helm_repositories.sh"
make -C "$root/charts" build-deps

source_chart_version() {
  awk '/^version:/ { print $2; exit }' "$1/Chart.yaml"
}
archive_chart_version() {
  helm show chart "$1" | awk '/^version:/ { print $2; exit }'
}
platform_version="$(source_chart_version "$root/charts/charts/iterabase-platform")"
certificate_version="$(source_chart_version "$root/charts/charts/cert-manager-substrate")"
storage_version="$(source_chart_version "$root/charts/charts/rwx-storage-substrate")"
[[ -n "$platform_version" && "$certificate_version" == "$platform_version" && "$storage_version" == "$platform_version" ]] || {
  echo "managed runtime chart versions are not aligned: platform=$platform_version certificate=$certificate_version storage=$storage_version" >&2
  exit 1
}

for chart in iterabase-platform cert-manager-substrate rwx-storage-substrate; do
  helm package "$root/charts/charts/$chart" --destination "$candidate_dir" >/dev/null
done
platform_archive="$candidate_dir/iterabase-platform-$platform_version.tgz"
certificate_archive="$candidate_dir/cert-manager-substrate-$platform_version.tgz"
storage_archive="$candidate_dir/rwx-storage-substrate-$platform_version.tgz"
for archive in "$platform_archive" "$certificate_archive" "$storage_archive"; do
  [[ -f "$archive" ]]
  [[ "$(archive_chart_version "$archive")" == "$platform_version" ]]
done

python3 - "$candidate_dir/checksums.txt" "$platform_archive" "$certificate_archive" "$storage_archive" <<'PY'
import hashlib
import pathlib
import sys

output = pathlib.Path(sys.argv[1])
lines = []
for value in sys.argv[2:]:
    path = pathlib.Path(value)
    digest = hashlib.sha256(path.read_bytes()).hexdigest()
    lines.append(f"{digest}  {path.name}\n")
output.write_text("".join(lines), encoding="utf-8")
PY

{
  printf 'ITERABASE_CHART_VERSION=%s\n' "$platform_version"
  printf 'FORGE_E2E_PLATFORM_CHART_ARCHIVE=%s\n' "$platform_archive"
  printf 'FORGE_E2E_SUBSTRATE_CHART_ARCHIVE=%s\n' "$certificate_archive"
  printf 'FORGE_E2E_RWX_STORAGE_CHART_ARCHIVE=%s\n' "$storage_archive"
} >> "$env_output"
printf 'exact source managed runtime ready: version=%s source=%s\n' "$platform_version" "$(git -C "$root" rev-parse HEAD)"
