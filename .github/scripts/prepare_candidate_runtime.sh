#!/usr/bin/env bash
set -euo pipefail

plan=${1:?usage: prepare_candidate_runtime.sh PLAN CANDIDATE_DIR ENV_OUTPUT}
candidate_dir=${2:?usage: prepare_candidate_runtime.sh PLAN CANDIDATE_DIR ENV_OUTPUT}
env_output=${3:?usage: prepare_candidate_runtime.sh PLAN CANDIDATE_DIR ENV_OUTPUT}
source_sha=$(jq -r '.source_sha' "$plan")
helm_bin=${HELM_BIN:-helm}

set_env() { printf '%s\n' "$1" >> "$env_output"; }
set_env "ITERABASE_E2E_SOURCE_SHA=$source_sha"
baseline_chart_field() {
  jq -r --arg chart "$1" --arg field "$2" \
    '.baseline_dependencies.charts[] | select(.chart == $chart) | .[$field]' "$plan"
}
baseline_image_field() {
  jq -r --arg name "$1" --arg field "$2" \
    '.baseline_dependencies.images[] | select(.name == $name) | .[$field]' "$plan"
}
selected_chart_version() {
  jq -r --arg chart "$1" '.chart_matrix[] | select(.chart == $chart) | .version' "$plan"
}
chart_version() {
  local chart=$1 version
  version=$(selected_chart_version "$chart")
  if [[ -z "$version" ]]; then
    version=$(baseline_chart_field "$chart" version)
  fi
  printf '%s' "$version"
}

for specification in \
  'control-plane CONTROL_PLANE' \
  'control-plane-harness HARNESS' \
  'inference-gateway INFERENCE_GATEWAY' \
  'control-plane-tool-runner TOOL_RUNNER'; do
  read -r name prefix <<<"$specification"
  repository=$(baseline_image_field "$name" repository)
  version=$(baseline_image_field "$name" version)
  digest=$(baseline_image_field "$name" digest)
  if [[ -n "$repository" && "$repository" != null ]]; then
    [[ "$digest" =~ ^sha256:[0-9a-f]{64}$ ]]
    set_env "${prefix}_IMAGE_REPO=$repository"
    set_env "${prefix}_IMAGE_TAG=$version@$digest"
    set_env "${prefix}_IMAGE_DIGEST=$digest"
  fi
done

for metadata in "$candidate_dir"/images/candidate-*.json; do
  [[ -e "$metadata" ]] || continue
  [[ $(jq -r '.artifact_type' "$metadata") == image ]] || continue
  name=$(jq -r '.name' "$metadata")
  repository=$(jq -r '.repository' "$metadata")
  digest=$(jq -r '.digest' "$metadata")
  [[ $(jq -r '.source_sha' "$metadata") == "$source_sha" ]]
  [[ "$digest" =~ ^sha256:[0-9a-f]{64}$ ]]
  immutable="$source_sha@$digest"
  case "$name" in
    control-plane)
      set_env "CONTROL_PLANE_IMAGE_REPO=$repository"
      set_env "CONTROL_PLANE_IMAGE_TAG=$immutable"
      set_env "CONTROL_PLANE_IMAGE_DIGEST=$digest"
      ;;
    control-plane-harness)
      set_env "HARNESS_IMAGE_REPO=$repository"
      set_env "HARNESS_IMAGE_TAG=$immutable"
      set_env "HARNESS_IMAGE_DIGEST=$digest"
      ;;
    inference-gateway)
      set_env "INFERENCE_GATEWAY_IMAGE_REPO=$repository"
      set_env "INFERENCE_GATEWAY_IMAGE_TAG=$immutable"
      set_env "INFERENCE_GATEWAY_IMAGE_DIGEST=$digest"
      ;;
    control-plane-tool-runner)
      set_env "TOOL_RUNNER_IMAGE_REPO=$repository"
      set_env "TOOL_RUNNER_IMAGE_TAG=$immutable"
      set_env "TOOL_RUNNER_IMAGE_DIGEST=$digest"
      ;;
  esac
done

rm -rf candidate-local
mkdir -p candidate-local/baseline-packages candidate-local/baselines candidate-local/selected candidate-local/transition-packages

# Pull the chart-owned supported predecessor pair and verify its owner-pinned
# archive checksums before exposing the exact packages to lifecycle scenarios.
while IFS=$'\t' read -r name chart repository version checksum; do
  [[ -n "$name" ]] || continue
  "$helm_bin" pull "$repository" --version "$version" --destination candidate-local/transition-packages
  archive="candidate-local/transition-packages/$chart-$version.tgz"
  [[ -f "$archive" ]]
  [[ "$checksum" =~ ^[0-9a-f]{64}$ ]]
  printf '%s  %s\n' "$checksum" "$archive" | sha256sum --check -
  case "$name" in
    supported-platform-predecessor)
      set_env "ITERABASE_E2E_PREDECESSOR_PLATFORM_ARCHIVE=$PWD/$archive"
      ;;
    supported-substrate-predecessor)
      set_env "ITERABASE_E2E_PREDECESSOR_SUBSTRATE_ARCHIVE=$PWD/$archive"
      ;;
    metallb-platform-predecessor)
      set_env "ITERABASE_E2E_METALLB_PREDECESSOR_PLATFORM_ARCHIVE=$PWD/$archive"
      ;;
    metallb-substrate-predecessor)
      set_env "ITERABASE_E2E_METALLB_PREDECESSOR_SUBSTRATE_ARCHIVE=$PWD/$archive"
      ;;
    *)
      echo "unknown chart transition baseline $name" >&2
      exit 1
      ;;
  esac
done < <(jq -r '(.transition_baselines.charts // [])[] | [.name,.chart,.repository,.version,.sha256] | @tsv' "$plan")

# Pull every published chart by its reviewed version, verify the plan-recorded
# archive checksum, and only then make its bytes available to a test fixture.
while IFS=$'\t' read -r chart repository version checksum; do
  [[ -n "$chart" ]] || continue
  "$helm_bin" pull "$repository" --version "$version" --destination candidate-local/baseline-packages
  archive="candidate-local/baseline-packages/$chart-$version.tgz"
  [[ -f "$archive" ]]
  [[ "$checksum" =~ ^[0-9a-f]{64}$ ]]
  printf '%s  %s\n' "$checksum" "$archive" | sha256sum --check -
  tar -xzf "$archive" -C candidate-local/baselines
done < <(
  jq -r '.baseline_dependencies.charts[] | [.chart,.repository,.version,.sha256] | @tsv' "$plan"
)

# Candidate chart checksums cover the exact selected archives and any companion.
while IFS= read -r chart; do
  [[ -n "$chart" ]] || continue
  metadata="$candidate_dir/charts/candidate-chart-$chart.json"
  checksums="$candidate_dir/charts/checksums-$chart.txt"
  [[ -f "$metadata" && -f "$checksums" ]]
  [[ $(jq -r '.schema_version' "$metadata") == 2 ]]
  [[ $(jq -r '.artifact_type' "$metadata") == chart ]]
  [[ $(jq -r '.chart' "$metadata") == "$chart" ]]
  [[ $(jq -r '.source_sha' "$metadata") == "$source_sha" ]]
  (cd "$candidate_dir/charts" && sha256sum --check "$(basename "$checksums")")
done < <(jq -r '.chart_matrix[].chart' "$plan")

candidate_archive() {
  local chart=$1 version
  version=$(selected_chart_version "$chart")
  [[ -n "$version" ]] || return 1
  printf '%s/%s-%s.tgz' "$candidate_dir/charts" "$chart" "$version"
}
extract_selected_chart() {
  local chart=$1 archive
  archive=$(candidate_archive "$chart")
  [[ -f "$archive" ]]
  tar -xzf "$archive" -C candidate-local/selected
}

for chart in control-plane inference-gateway iterabase-platform; do
  if [[ -n $(selected_chart_version "$chart") ]]; then
    extract_selected_chart "$chart"
  fi
done

# Direct control-plane scenarios consume the selected chart when present and
# otherwise the checksum-verified published chart directory.
if [[ -d candidate-local/selected/control-plane ]]; then
  set_env "CONTROL_PLANE_LOCAL_CHART=$PWD/candidate-local/selected/control-plane"
  set_env "CONTROL_PLANE_CHART_VERSION=$(chart_version control-plane)"
elif [[ -d candidate-local/baselines/control-plane ]]; then
  set_env "CONTROL_PLANE_LOCAL_CHART=$PWD/candidate-local/baselines/control-plane"
  set_env "CONTROL_PLANE_CHART_VERSION=$(chart_version control-plane)"
fi

# Build one platform fixture from the selected outer chart (if any) or its
# verified published baseline, then replace selected nested chart members with
# their exact retained archives.
mkdir -p candidate-local/runtime
if [[ -d candidate-local/selected/iterabase-platform ]]; then
  cp -R candidate-local/selected/iterabase-platform candidate-local/runtime/
  companion_version=$(selected_chart_version iterabase-platform)
  companion="$candidate_dir/charts/cert-manager-substrate-$companion_version.tgz"
  storage_companion="$candidate_dir/charts/rwx-storage-substrate-$companion_version.tgz"
  [[ -f "$companion" ]]
  [[ -f "$storage_companion" ]]
  tar -xzf "$companion" -C candidate-local/runtime
  tar -xzf "$storage_companion" -C candidate-local/runtime
elif [[ -d candidate-local/baselines/iterabase-platform ]]; then
  cp -R candidate-local/baselines/iterabase-platform candidate-local/runtime/
  [[ -d candidate-local/baselines/cert-manager-substrate ]]
  cp -R candidate-local/baselines/cert-manager-substrate candidate-local/runtime/
fi

platform_dir=candidate-local/runtime/iterabase-platform
if [[ -d "$platform_dir" ]]; then
  mkdir -p "$platform_dir/charts"
  for chart in control-plane inference-gateway; do
    if archive=$(candidate_archive "$chart" 2>/dev/null); then
      [[ -f "$archive" ]]
      rm -rf "$platform_dir/charts/$chart" "$platform_dir/charts/$chart-"*.tgz
      cp "$archive" "$platform_dir/charts/"
    fi
  done
  platform_version=$(chart_version iterabase-platform)
  set_env "ITERABASE_LOCAL_CHART=$PWD/$platform_dir"
  set_env "ITERABASE_PLATFORM_LOCAL_CHART=$PWD/$platform_dir"
  set_env "ITERABASE_CHART_VERSION=$platform_version"

  substrate_dir=candidate-local/runtime/cert-manager-substrate
  storage_substrate_dir=candidate-local/runtime/rwx-storage-substrate
  [[ -d "$substrate_dir" ]]
  tar -czf candidate-local/runtime-iterabase-platform.tgz \
    -C candidate-local/runtime iterabase-platform
  tar -czf candidate-local/runtime-cert-manager-substrate.tgz \
    -C candidate-local/runtime cert-manager-substrate
  set_env "FORGE_E2E_PLATFORM_CHART_ARCHIVE=$PWD/candidate-local/runtime-iterabase-platform.tgz"
  set_env "FORGE_E2E_SUBSTRATE_CHART_ARCHIVE=$PWD/candidate-local/runtime-cert-manager-substrate.tgz"
  if [[ -d "$storage_substrate_dir" ]]; then
    tar -czf candidate-local/runtime-rwx-storage-substrate.tgz \
      -C candidate-local/runtime rwx-storage-substrate
    set_env "FORGE_E2E_RWX_STORAGE_CHART_ARCHIVE=$PWD/candidate-local/runtime-rwx-storage-substrate.tgz"
  fi
elif [[ -d candidate-local/baselines/cert-manager-substrate ]]; then
  # The tool-runner scenario asks for the substrate as a sibling of the
  # platform path even though it does not install the platform chart itself.
  mkdir -p candidate-local/baselines/platform-placeholder
  set_env "ITERABASE_PLATFORM_LOCAL_CHART=$PWD/candidate-local/baselines/platform-placeholder"
  set_env "ITERABASE_CHART_VERSION=$(chart_version cert-manager-substrate)"
fi
