#!/usr/bin/env bash
set -euo pipefail

candidate=${1:?usage: check_promotion_destinations.sh CANDIDATE REPOSITORY_OWNER}
repository_owner=${2:?usage: check_promotion_destinations.sh CANDIDATE REPOSITORY_OWNER}
docker_bin=${DOCKER_BIN:-docker}
helm_bin=${HELM_BIN:-helm}

plan="$candidate/candidate-plan.json"

for metadata in "$candidate"/assets/images/candidate-*.json; do
  [[ -e "$metadata" ]] || continue
  [[ $(jq -r '.artifact_type' "$metadata") == image ]] || continue
  repository=$(jq -r '.repository' "$metadata")
  version=$(jq -r '.version' "$metadata")
  expected=$(jq -r '.digest' "$metadata")
  set +e
  output=$($docker_bin buildx imagetools inspect "$repository:$version" --format '{{json .Manifest.Digest}}' 2>&1)
  status=$?
  set -e
  if [[ $status -eq 0 ]]; then
    actual=$(tr -d '"' <<<"$output")
    [[ "$actual" == "$expected" ]] || {
      echo "$repository:$version conflicts with tested digest $expected ($actual exists)" >&2
      exit 1
    }
  elif [[ $status -eq 1 ]] && grep -Eqi '(^|: )not found$|manifest unknown|name unknown' <<<"$output"; then
    :
  else
    echo "could not preflight $repository:$version:" >&2
    printf '%s\n' "$output" >&2
    exit 1
  fi
done

while IFS=$'\t' read -r chart version; do
  package="$candidate/assets/charts/$chart-$version.tgz"
  [[ -f "$package" ]] || { echo "missing tested chart package $package" >&2; exit 1; }
  repository="oci://ghcr.io/$repository_owner/iterabase-charts/$chart"
  destination=$(mktemp -d)
  set +e
  output=$($helm_bin pull "$repository" --version "$version" --destination "$destination" 2>&1)
  status=$?
  set -e
  if [[ $status -eq 0 ]]; then
    cmp "$package" "$destination/$(basename "$package")" || {
      echo "$repository:$version conflicts with the tested archive" >&2
      exit 1
    }
  elif [[ $status -eq 1 ]] && grep -Eqi '(^|: )not found$|manifest unknown|name unknown' <<<"$output"; then
    :
  else
    echo "could not preflight $repository:$version:" >&2
    printf '%s\n' "$output" >&2
    exit 1
  fi
done < <(
  jq -r '.chart_matrix[] | .version as $version | ([.chart] + .companions)[] | [.,$version] | @tsv' "$plan"
)
