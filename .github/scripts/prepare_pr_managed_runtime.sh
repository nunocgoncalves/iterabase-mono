#!/usr/bin/env bash
set -euo pipefail

candidate_dir=${1:?usage: prepare_pr_managed_runtime.sh CANDIDATE_DIR ENV_OUTPUT [charts-only|with-source-images]}
env_output=${2:?usage: prepare_pr_managed_runtime.sh CANDIDATE_DIR ENV_OUTPUT [charts-only|with-source-images]}
runtime_mode=${3:-charts-only}
[[ "$runtime_mode" == "charts-only" || "$runtime_mode" == "with-source-images" ]] || {
  echo "runtime mode must be charts-only or with-source-images, got $runtime_mode" >&2
  exit 1
}
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

runtime_artifacts=("$platform_archive" "$certificate_archive" "$storage_archive")
if [[ "$runtime_mode" == "with-source-images" ]]; then
  source_sha="$(git -C "$root" rev-parse HEAD)"
  source_date="$(git -C "$root" show -s --format=%cI HEAD)"
  control_plane_repository=localhost/iterabase/control-plane
  tool_runner_repository=localhost/iterabase/control-plane-tool-runner
  harness_repository=localhost/iterabase/control-plane-harness
  control_plane_image="$control_plane_repository:$source_sha"
  tool_runner_image="$tool_runner_repository:$source_sha"
  harness_image="$harness_repository:$source_sha"
  source_image_archive="$candidate_dir/exact-source-images-$source_sha.tar"

  docker build --platform linux/amd64 \
    --build-arg VERSION="pr-$source_sha" \
    --build-arg COMMIT="$source_sha" \
    --build-arg DATE="$source_date" \
    --tag "$control_plane_image" \
    "$root/control-plane"
  docker build --platform linux/amd64 \
    --build-arg VERSION="$source_sha" \
    --tag "$tool_runner_image" \
    --file "$root/control-plane/tool-runner/Dockerfile" \
    "$root/control-plane/tool-runner"
  docker build --platform linux/amd64 \
    --build-arg VERSION="$source_sha" \
    --tag "$harness_image" \
    --file "$root/control-plane/harness/Dockerfile" \
    "$root/control-plane/harness"
  docker save --output "$source_image_archive" "$control_plane_image" "$tool_runner_image" "$harness_image"
  runtime_artifacts+=("$source_image_archive")
fi

python3 - "$candidate_dir/checksums.txt" "${runtime_artifacts[@]}" <<'PY'
import hashlib
import pathlib
import sys

output = pathlib.Path(sys.argv[1])
lines = []
for value in sys.argv[2:]:
    path = pathlib.Path(value)
    digest = hashlib.sha256()
    with path.open("rb") as source:
        for chunk in iter(lambda: source.read(1024 * 1024), b""):
            digest.update(chunk)
    lines.append(f"{digest.hexdigest()}  {path.name}\n")
output.write_text("".join(lines), encoding="utf-8")
PY

{
  printf 'ITERABASE_CHART_VERSION=%s\n' "$platform_version"
  printf 'FORGE_E2E_PLATFORM_CHART_ARCHIVE=%s\n' "$platform_archive"
  printf 'FORGE_E2E_SUBSTRATE_CHART_ARCHIVE=%s\n' "$certificate_archive"
  printf 'FORGE_E2E_RWX_STORAGE_CHART_ARCHIVE=%s\n' "$storage_archive"
  if [[ "$runtime_mode" == "with-source-images" ]]; then
    printf 'FORGE_E2E_SOURCE_IMAGE_ARCHIVE=%s\n' "$source_image_archive"
    printf 'CONTROL_PLANE_IMAGE_REPO=%s\n' "$control_plane_repository"
    printf 'CONTROL_PLANE_IMAGE_TAG=%s\n' "$source_sha"
    printf 'TOOL_RUNNER_IMAGE_REPO=%s\n' "$tool_runner_repository"
    printf 'TOOL_RUNNER_IMAGE_TAG=%s\n' "$source_sha"
    printf 'HARNESS_IMAGE_REPO=%s\n' "$harness_repository"
    printf 'HARNESS_IMAGE_TAG=%s\n' "$source_sha"
  fi
} >> "$env_output"
printf 'exact source managed runtime ready: version=%s source=%s mode=%s\n' "$platform_version" "$(git -C "$root" rev-parse HEAD)" "$runtime_mode"
