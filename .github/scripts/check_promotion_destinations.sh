#!/usr/bin/env bash
set -euo pipefail

candidate=${1:?usage: check_promotion_destinations.sh CANDIDATE REPOSITORY_OWNER REPOSITORY}
repository_owner=${2:?usage: check_promotion_destinations.sh CANDIDATE REPOSITORY_OWNER REPOSITORY}
github_repository=${3:-${GITHUB_REPOSITORY:-}}
docker_bin=${DOCKER_BIN:-docker}
helm_bin=${HELM_BIN:-helm}
gh_bin=${GH_BIN:-gh}
plan="$candidate/candidate-plan.json"

for metadata in "$candidate"/assets/images/candidate-*.json; do
  [[ -e "$metadata" ]] || continue
  [[ $(jq -r '.artifact_type' "$metadata") == image ]] || continue
  image_repository=$(jq -r '.repository' "$metadata")
  version=$(jq -r '.version' "$metadata")
  expected=$(jq -r '.digest' "$metadata")
  set +e
  output=$($docker_bin buildx imagetools inspect "$image_repository:$version" --format '{{json .Manifest.Digest}}' 2>&1)
  status=$?
  set -e
  if [[ $status -eq 0 ]]; then
    actual=$(tr -d '"' <<<"$output")
    [[ "$actual" == "$expected" ]] || {
      echo "$image_repository:$version conflicts with tested digest $expected ($actual exists)" >&2
      exit 1
    }
  elif [[ $status -eq 1 ]] && grep -Eqi '(^|: )not found$|manifest unknown|name unknown' <<<"$output"; then
    :
  else
    echo "could not preflight $image_repository:$version:" >&2
    printf '%s\n' "$output" >&2
    exit 1
  fi
done

while IFS=$'\t' read -r chart version; do
  package="$candidate/assets/charts/$chart-$version.tgz"
  [[ -f "$package" ]] || { echo "missing tested chart package $package" >&2; exit 1; }
  chart_repository="oci://ghcr.io/$repository_owner/iterabase-charts/$chart"
  destination=$(mktemp -d)
  set +e
  output=$($helm_bin pull "$chart_repository" --version "$version" --destination "$destination" 2>&1)
  status=$?
  set -e
  if [[ $status -eq 0 ]]; then
    cmp "$package" "$destination/$(basename "$package")" || {
      echo "$chart_repository:$version conflicts with the tested archive" >&2
      exit 1
    }
  elif [[ $status -eq 1 ]] && grep -Eqi '(^|: )not found$|manifest unknown|name unknown' <<<"$output"; then
    :
  else
    echo "could not preflight $chart_repository:$version:" >&2
    printf '%s\n' "$output" >&2
    exit 1
  fi
done < <(
  jq -r '.chart_matrix[] | .version as $version | ([.chart] + .companions)[] | [.,$version] | @tsv' "$plan"
)

[[ -n "$github_repository" ]] || { echo "GitHub repository is required for Release preflight" >&2; exit 1; }
# Exact tags are the only lookup key. A draft is replaceable in full; a published
# immutable Release is accepted only as a retry candidate and is verified against
# the generated schema-v3 manifest by publish_github_releases.sh before mutation.
while IFS=$'\t' read -r target tag source; do
  set +e
  release_json=$($gh_bin api "repos/$github_repository/releases/tags/$tag" 2>&1)
  status=$?
  set -e
  if [[ $status -ne 0 ]]; then
    [[ $status -eq 1 ]] && grep -Eqi '(^|[^0-9])404([^0-9]|$)|not found' <<<"$release_json" && continue
    echo "could not preflight GitHub Release $tag:" >&2
    printf '%s\n' "$release_json" >&2
    exit 1
  fi
  jq -e --arg tag "$tag" --arg source "$source" '
    .tag_name == $tag and .target_commitish == $source and
    ((.draft == true) or (.draft == false and .prerelease == false and .immutable == true))
  ' <<<"$release_json" >/dev/null || {
    echo "GitHub Release $tag conflicts with the selected candidate target $target" >&2
    exit 1
  }
done < <(jq -r '.source_sha as $source | .releases[] | [.target,.production_tag,$source] | @tsv' "$plan")
