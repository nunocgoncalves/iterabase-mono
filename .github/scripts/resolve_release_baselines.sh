#!/usr/bin/env bash
set -euo pipefail

plan=${1:?usage: resolve_release_baselines.sh PLAN}
docker_bin=${DOCKER_BIN:-docker}
helm_bin=${HELM_BIN:-helm}

resolved_images='[]'
while IFS=$'\t' read -r name target repository version; do
  [[ -n "$name" ]] || continue
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
done < <(jq -r '.baseline_dependencies.images[] | [.name,.target,.repository,.version] | @tsv' "$plan")

resolved_charts='[]'
while IFS=$'\t' read -r chart repository version; do
  [[ -n "$chart" ]] || continue
  directory=$(mktemp -d)
  $helm_bin pull "$repository" --version "$version" --destination "$directory"
  archive="$directory/$chart-$version.tgz"
  [[ -f "$archive" ]] || { echo "published baseline archive missing: $archive" >&2; exit 1; }
  checksum=$(sha256sum "$archive" | awk '{print $1}')
  resolved_charts=$(jq -c \
    --arg chart "$chart" --arg repository "$repository" --arg version "$version" --arg sha256 "$checksum" \
    '. + [{chart:$chart,repository:$repository,version:$version,sha256:$sha256}]' \
    <<<"$resolved_charts")
done < <(jq -r '.baseline_dependencies.charts[] | [.chart,.repository,.version] | @tsv' "$plan")

temporary=$(mktemp)
jq --argjson images "$resolved_images" --argjson charts "$resolved_charts" \
  '.baseline_dependencies.images = $images | .baseline_dependencies.charts = $charts' \
  "$plan" > "$temporary"
mv "$temporary" "$plan"
