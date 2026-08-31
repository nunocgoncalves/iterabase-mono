#!/usr/bin/env bash
set -euo pipefail

candidate_dir=${1:?usage: prepare_pr_workspace_runtime.sh CANDIDATE_DIR ENV_OUTPUT}
env_output=${2:?usage: prepare_pr_workspace_runtime.sh CANDIDATE_DIR ENV_OUTPUT}
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
[[ -n "$platform_version" && "$certificate_version" == "$platform_version" ]] || {
  echo "workspace runtime chart versions are not aligned: platform=$platform_version certificate=$certificate_version" >&2
  exit 1
}

for chart in iterabase-platform cert-manager-substrate; do
  helm package "$root/charts/charts/$chart" --destination "$candidate_dir" >/dev/null
done
platform_archive="$candidate_dir/iterabase-platform-$platform_version.tgz"
certificate_archive="$candidate_dir/cert-manager-substrate-$platform_version.tgz"
for archive in "$platform_archive" "$certificate_archive"; do
  [[ -f "$archive" ]]
  [[ "$(archive_chart_version "$archive")" == "$platform_version" ]]
done

source_sha="$(git -C "$root" rev-parse HEAD)"
image_tag="${source_sha:0:12}"
declare -A image_contexts=(
  [control-plane]="control-plane|control-plane/Dockerfile"
  [control-plane-harness]="control-plane/harness|control-plane/harness/Dockerfile"
  [control-plane-tool-runner]="control-plane/tool-runner|control-plane/tool-runner/Dockerfile"
  [control-plane-runtime-fixture]="control-plane/test/e2e/fixtures/runtime|control-plane/test/e2e/fixtures/runtime/Dockerfile"
  [inference-gateway]="inference-gateway|inference-gateway/Dockerfile"
)
for image in control-plane control-plane-harness control-plane-tool-runner control-plane-runtime-fixture inference-gateway; do
  IFS='|' read -r context dockerfile <<<"${image_contexts[$image]}"
  docker build -t "iterabase-e2e/$image:$image_tag" -f "$root/$dockerfile" "$root/$context"
  docker save -o "$candidate_dir/$image.tar" "iterabase-e2e/$image:$image_tag"
done

python3 - "$candidate_dir/checksums.txt" "$platform_archive" "$certificate_archive" "$candidate_dir/control-plane.tar" "$candidate_dir/control-plane-harness.tar" "$candidate_dir/control-plane-tool-runner.tar" "$candidate_dir/control-plane-runtime-fixture.tar" "$candidate_dir/inference-gateway.tar" <<'PY'
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
  printf 'CONTROL_PLANE_IMAGE_REPO=iterabase-e2e/control-plane\n'
  printf 'CONTROL_PLANE_IMAGE_TAG=%s\n' "$image_tag"
  printf 'HARNESS_IMAGE_REPO=iterabase-e2e/control-plane-harness\n'
  printf 'HARNESS_IMAGE_TAG=%s\n' "$image_tag"
  printf 'TOOL_RUNNER_IMAGE_REPO=iterabase-e2e/control-plane-tool-runner\n'
  printf 'TOOL_RUNNER_IMAGE_TAG=%s\n' "$image_tag"
  printf 'FORGE_E2E_RUNTIME_IMAGE_REPO=iterabase-e2e/control-plane-runtime-fixture\n'
  printf 'FORGE_E2E_RUNTIME_IMAGE_TAG=%s\n' "$image_tag"
  printf 'INFERENCE_GATEWAY_IMAGE_REPO=iterabase-e2e/inference-gateway\n'
  printf 'INFERENCE_GATEWAY_IMAGE_TAG=%s\n' "$image_tag"
  printf 'FORGE_E2E_CONTROL_PLANE_IMAGE_ARCHIVE=%s\n' "$candidate_dir/control-plane.tar"
  printf 'FORGE_E2E_HARNESS_IMAGE_ARCHIVE=%s\n' "$candidate_dir/control-plane-harness.tar"
  printf 'FORGE_E2E_TOOL_RUNNER_IMAGE_ARCHIVE=%s\n' "$candidate_dir/control-plane-tool-runner.tar"
  printf 'FORGE_E2E_RUNTIME_IMAGE_ARCHIVE=%s\n' "$candidate_dir/control-plane-runtime-fixture.tar"
  printf 'FORGE_E2E_INFERENCE_IMAGE_ARCHIVE=%s\n' "$candidate_dir/inference-gateway.tar"
} >> "$env_output"
printf 'exact source dedicated-workspace runtime ready: version=%s source=%s\n' "$platform_version" "$source_sha"
