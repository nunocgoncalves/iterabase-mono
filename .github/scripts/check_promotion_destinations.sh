#!/usr/bin/env bash
set -euo pipefail

candidate=${1:?usage: check_promotion_destinations.sh CANDIDATE REPOSITORY_OWNER REPOSITORY}
repository_owner=${2:?usage: check_promotion_destinations.sh CANDIDATE REPOSITORY_OWNER REPOSITORY}
repository=${3:-${GITHUB_REPOSITORY:-}}
docker_bin=${DOCKER_BIN:-docker}
helm_bin=${HELM_BIN:-helm}
gh_bin=${GH_BIN:-gh}

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

release_assets() {
  local target=$1 version=$2 artifact_types=$3 name chart
  printf '%s\n' "$candidate/candidate-plan.json" "$candidate/candidate-evidence.json"
  if jq -e 'index("image")' <<<"$artifact_types" >/dev/null; then
    while IFS= read -r name; do
      [[ -n "$name" ]] || continue
      printf '%s\n' \
        "$candidate/assets/images/candidate-$name.json" \
        "$candidate/assets/images/candidate-$name.spdx.json"
    done < <(jq -r --arg target "$target" '.image_matrix[] | select(.target == $target) | .name' "$plan")
  fi
  if jq -e 'index("chart")' <<<"$artifact_types" >/dev/null; then
    chart=$(jq -r --arg target "$target" '.chart_matrix[] | select(.target == $target) | .chart' "$plan")
    printf '%s\n' \
      "$candidate/assets/charts/candidate-chart-$chart.json" \
      "$candidate/assets/charts/candidate-chart-$chart.spdx.json" \
      "$candidate/assets/charts/checksums-$chart.txt"
    while IFS= read -r name; do
      printf '%s\n' "$candidate/assets/charts/$name-$version.tgz"
    done < <(
      jq -r --arg target "$target" \
        '.chart_matrix[] | select(.target == $target) | [.chart] + .companions | .[]' "$plan"
    )
  fi
  if jq -e 'index("forge")' <<<"$artifact_types" >/dev/null; then
    find "$candidate/assets/forge" -type f | sort
  fi
}

# An existing Release is resumable only when every already-present asset is one
# of this target's candidate files and has identical bytes. Perform all of these
# reads before the first image/chart/tag/Release mutation.
[[ -n "$repository" ]] || { echo "GitHub repository is required for Release preflight" >&2; exit 1; }
while IFS=$'\t' read -r target version tag artifact_types; do
  expected_assets=()
  while IFS= read -r asset; do expected_assets+=("$asset"); done \
    < <(release_assets "$target" "$version" "$artifact_types")
  for asset in "${expected_assets[@]}"; do
    [[ -f "$asset" ]] || { echo "missing candidate Release asset $asset" >&2; exit 1; }
  done

  set +e
  release_json=$($gh_bin release view "$tag" --repo "$repository" --json tagName,assets 2>&1)
  status=$?
  set -e
  if [[ $status -ne 0 ]]; then
    if [[ $status -eq 1 ]] && grep -Eqi '^release not found([:.]|$)' <<<"$release_json"; then
      continue
    fi
    echo "could not preflight GitHub Release $tag:" >&2
    printf '%s\n' "$release_json" >&2
    exit 1
  fi
  [[ $(jq -r '.tagName' <<<"$release_json") == "$tag" ]] || {
    echo "GitHub Release $tag resolved to a conflicting tag" >&2
    exit 1
  }

  while IFS= read -r existing_name; do
    [[ -n "$existing_name" ]] || continue
    expected_path=
    for asset in "${expected_assets[@]}"; do
      if [[ $(basename "$asset") == "$existing_name" ]]; then
        expected_path=$asset
        break
      fi
    done
    [[ -n "$expected_path" ]] || {
      echo "GitHub Release $tag has unexpected conflicting asset $existing_name" >&2
      exit 1
    }
    destination=$(mktemp -d)
    $gh_bin release download "$tag" --repo "$repository" \
      --pattern "$existing_name" --dir "$destination"
    cmp "$expected_path" "$destination/$existing_name" || {
      echo "GitHub Release $tag asset $existing_name conflicts with the candidate" >&2
      exit 1
    }
  done < <(jq -r '.assets[].name' <<<"$release_json")
done < <(
  jq -r '.releases[] | [.target,.version,.production_tag,(.artifact_types | tojson)] | @tsv' "$plan"
)
