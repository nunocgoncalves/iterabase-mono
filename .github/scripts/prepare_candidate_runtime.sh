#!/usr/bin/env bash
set -euo pipefail

plan=${1:?usage: prepare_candidate_runtime.sh PLAN CANDIDATE_DIR ENV_OUTPUT}
candidate_dir=${2:?usage: prepare_candidate_runtime.sh PLAN CANDIDATE_DIR ENV_OUTPUT}
env_output=${3:?usage: prepare_candidate_runtime.sh PLAN CANDIDATE_DIR ENV_OUTPUT}
source_sha=$(jq -r '.source_sha' "$plan")

set_env() { printf '%s\n' "$1" >> "$env_output"; }
baseline_chart_version() {
  jq -r --arg chart "$1" '.baseline_dependencies.charts[] | select(.chart == $chart) | .version' "$plan"
}
baseline_image_field() {
  jq -r --arg name "$1" --arg field "$2" '.baseline_dependencies.images[] | select(.name == $name) | .[$field]' "$plan"
}

set_env "ITERABASE_CHART_VERSION=$(baseline_chart_version iterabase-platform)"
set_env "CONTROL_PLANE_CHART_VERSION=$(baseline_chart_version control-plane)"
for specification in \
  'control-plane CONTROL_PLANE' \
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
  fi
done

for metadata in "$candidate_dir"/images/candidate-*.json; do
  [[ -e "$metadata" ]] || continue
  [[ $(jq -r '.artifact_type' "$metadata") == image ]] || continue
  name=$(jq -r '.name' "$metadata")
  repository=$(jq -r '.repository' "$metadata")
  digest=$(jq -r '.digest' "$metadata")
  immutable="$source_sha@$digest"
  case "$name" in
    control-plane)
      set_env "CONTROL_PLANE_IMAGE_REPO=$repository"
      set_env "CONTROL_PLANE_IMAGE_TAG=$immutable"
      set_env "CONTROL_PLANE_IMAGE_DIGEST=$digest"
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

mkdir -p candidate-local
extract_chart() {
  local chart=$1
  local archive metadata checksums
  archive=$(find "$candidate_dir/charts" -name "$chart-*.tgz" -print -quit 2>/dev/null || true)
  [[ -n "$archive" ]] || return 0
  metadata="$candidate_dir/charts/candidate-chart-$chart.json"
  checksums="$candidate_dir/charts/checksums-$chart.txt"
  [[ $(jq -r '.schema_version' "$metadata") == 2 ]]
  [[ $(jq -r '.source_sha' "$metadata") == "$source_sha" ]]
  (cd "$candidate_dir/charts" && sha256sum --check "$(basename "$checksums")")
  tar -xzf "$archive" -C candidate-local
}

extract_chart control-plane
extract_chart inference-gateway
extract_chart iterabase-platform

if [[ -d candidate-local/control-plane ]]; then
  set_env "CONTROL_PLANE_LOCAL_CHART=$PWD/candidate-local/control-plane"
  set_env "CONTROL_PLANE_CHART_VERSION="
fi

if [[ -d candidate-local/iterabase-platform ]]; then
  companion_archive=$(find "$candidate_dir/charts" -name 'cert-manager-substrate-*.tgz' -print -quit)
  [[ -n "$companion_archive" ]]
  tar -xzf "$companion_archive" -C candidate-local
  set_env "ITERABASE_LOCAL_CHART=$PWD/candidate-local/iterabase-platform"
  set_env "ITERABASE_PLATFORM_LOCAL_CHART=$PWD/candidate-local/iterabase-platform"
  set_env "ITERABASE_CHART_VERSION="
elif [[ -d candidate-local/inference-gateway ]]; then
  # Compose the selected gateway chart into the reviewed published platform
  # baseline; do not resolve any bumped-but-unpublished repository version.
  platform_version=$(baseline_chart_version iterabase-platform)
  substrate_version=$(baseline_chart_version cert-manager-substrate)
  [[ -n "$platform_version" && "$platform_version" != null ]]
  [[ -n "$substrate_version" && "$substrate_version" != null ]]
  mkdir -p candidate-local/platform-baseline
  helm pull oci://ghcr.io/nunocgoncalves/iterabase-charts/iterabase-platform \
    --version "$platform_version" --untar --untardir candidate-local/platform-baseline
  helm pull oci://ghcr.io/nunocgoncalves/iterabase-charts/cert-manager-substrate \
    --version "$substrate_version" --untar --untardir candidate-local/platform-baseline
  mkdir -p candidate-local/platform-baseline/iterabase-platform/charts
  rm -f candidate-local/platform-baseline/iterabase-platform/charts/inference-gateway-*.tgz
  cp "$candidate_dir"/charts/inference-gateway-*.tgz \
    candidate-local/platform-baseline/iterabase-platform/charts/
  set_env "ITERABASE_LOCAL_CHART=$PWD/candidate-local/platform-baseline/iterabase-platform"
  set_env "ITERABASE_PLATFORM_LOCAL_CHART=$PWD/candidate-local/platform-baseline/iterabase-platform"
  set_env "ITERABASE_CHART_VERSION="
fi
