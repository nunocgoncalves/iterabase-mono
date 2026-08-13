#!/usr/bin/env bash
set -euo pipefail

candidate_dir=${1:?candidate artifact directory is required}

prepare_candidate() {
  local candidate_chart=$1
  local metadata="$candidate_dir/candidate-chart-$candidate_chart.json"
  local checksums="$candidate_dir/checksums-$candidate_chart.txt"

  [[ -f "$metadata" ]] || { echo "missing candidate metadata: $metadata" >&2; exit 1; }
  [[ -f "$checksums" ]] || { echo "missing candidate checksums: $checksums" >&2; exit 1; }
  [[ $(jq -r '.schema_version' "$metadata") == 2 ]]
  [[ $(jq -r '.artifact_type' "$metadata") == chart ]]
  [[ $(jq -r '.chart' "$metadata") == "$candidate_chart" ]]
  local version archive
  version=$(jq -r '.version' "$metadata")

  (
    cd "$candidate_dir"
    sha256sum --check "$(basename "$checksums")"
  )

  archive="$candidate_dir/$candidate_chart-$version.tgz"
  [[ -f "$archive" ]] || { echo "missing exact candidate archive: $archive" >&2; exit 1; }
  [[ $(helm show chart "$archive" | awk '/^name:/ {print $2; exit}') == "$candidate_chart" ]]
  [[ $(helm show chart "$archive" | awk '/^version:/ {print $2; exit}') == "$version" ]]

  case "$candidate_chart" in
    control-plane|inference-gateway)
      rm -f "charts/iterabase-platform/charts/$candidate_chart-"*.tgz
      cp "$archive" charts/iterabase-platform/charts/
      cmp "$archive" "charts/iterabase-platform/charts/$(basename "$archive")"
      ;;
    iterabase-platform)
      local companion staging
      companion="$candidate_dir/cert-manager-substrate-$version.tgz"
      [[ -f "$companion" ]] || { echo "missing exact companion archive: $companion" >&2; exit 1; }
      staging=$(mktemp -d)
      tar -xzf "$archive" -C "$staging"
      tar -xzf "$companion" -C "$staging"
      rm -rf charts/iterabase-platform charts/cert-manager-substrate
      mv "$staging/iterabase-platform" charts/iterabase-platform
      mv "$staging/cert-manager-substrate" charts/cert-manager-substrate
      ;;
    *)
      echo "unsupported runtime candidate chart: $candidate_chart" >&2
      exit 1
      ;;
  esac

  echo "runtime fixture uses exact $candidate_chart $version candidate archive"
}

# Apply nested chart candidates before the umbrella candidate. If the umbrella
# is selected it already contains the exact dependency archives from its build;
# applying it last preserves that coherent package.
for chart in control-plane inference-gateway iterabase-platform; do
  if [[ -f "$candidate_dir/candidate-chart-$chart.json" ]]; then
    prepare_candidate "$chart"
  fi
done
