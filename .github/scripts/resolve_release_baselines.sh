#!/usr/bin/env bash
set -euo pipefail

plan=${1:?usage: resolve_release_baselines.sh PLAN}
docker_bin=${DOCKER_BIN:-docker}
helm_bin=${HELM_BIN:-helm}
script_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
baseline_root=$(mktemp -d)
trap 'rm -rf "$baseline_root"' EXIT

# Resolve charts first because image versions may come from the exact published
# chart fixture that validation will consume.
resolved_charts='[]'
while IFS=$'\t' read -r chart repository version; do
  [[ -n "$chart" ]] || continue
  $helm_bin pull "$repository" --version "$version" --destination "$baseline_root"
  archive="$baseline_root/$chart-$version.tgz"
  [[ -f "$archive" ]] || { echo "published baseline archive missing: $archive" >&2; exit 1; }
  checksum=$(sha256sum "$archive" | awk '{print $1}')
  tar -xzf "$archive" -C "$baseline_root"
  resolved_charts=$(jq -c \
    --arg chart "$chart" --arg repository "$repository" --arg version "$version" --arg sha256 "$checksum" \
    '. + [{chart:$chart,repository:$repository,version:$version,sha256:$sha256}]' \
    <<<"$resolved_charts")
done < <(jq -r '.baseline_dependencies.charts[] | [.chart,.repository,.version] | @tsv' "$plan")

resolved_images='[]'
while IFS=$'\x1f' read -r name target repository version source_chart values_path value_key; do
  [[ -n "$name" ]] || continue
  if [[ -z "$version" ]]; then
    [[ -n "$source_chart" && -n "$values_path" && -n "$value_key" ]] || {
      echo "published baseline $name has no version authority" >&2
      exit 1
    }
    values="$baseline_root/$source_chart/$values_path"
    [[ -f "$values" ]] || { echo "published baseline values missing: $values" >&2; exit 1; }
    version=$(python3 "$script_dir/release.py" image-version --values "$values" --key "$value_key")
  fi
  digest=$($docker_bin buildx imagetools inspect "$repository:$version" --format '{{json .Manifest.Digest}}' | tr -d '"')
  [[ "$digest" =~ ^sha256:[0-9a-f]{64}$ ]] || {
    echo "published baseline $repository:$version has invalid digest $digest" >&2
    exit 1
  }
  resolved_images=$(jq -c \
    --arg name "$name" --arg target "$target" --arg repository "$repository" \
    --arg version "$version" --arg digest "$digest" \
    '. + [{name:$name,target:$target,repository:$repository,version:$version,digest:$digest,immutable_reference:($repository+":"+$version+"@"+$digest)}]' \
    <<<"$resolved_images")
done < <(
  jq -r '.baseline_dependencies.images[] |
    [.name,.target,.repository,(.version // ""),(.version_from.chart // ""),
     (.version_from.values_path // ""),(.version_from.value_key // "")] | join("\u001f")' "$plan"
)

temporary=$(mktemp)
jq --argjson images "$resolved_images" --argjson charts "$resolved_charts" \
  '.baseline_dependencies.images = $images | .baseline_dependencies.charts = $charts' \
  "$plan" > "$temporary"
mv "$temporary" "$plan"
