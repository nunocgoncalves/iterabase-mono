#!/usr/bin/env bash
set -euo pipefail

candidate=${1:?usage: check_promotion_destinations.sh CANDIDATE REPOSITORY_OWNER REPOSITORY MANIFESTS}
repository_owner=${2:?usage: check_promotion_destinations.sh CANDIDATE REPOSITORY_OWNER REPOSITORY MANIFESTS}
github_repository=${3:-${GITHUB_REPOSITORY:-}}
manifests=${4:-}
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
[[ -d "$manifests" ]] || { echo "complete Release manifests are required for preflight" >&2; exit 1; }
python3 "$(git rev-parse --show-toplevel)/.github/scripts/release.py" verify-release-manifests \
  --candidate "$candidate" --directory "$manifests" >/dev/null

# Published Releases are verification-only and must already contain exactly the
# manifest-complete member set. Unpublished drafts may be replaced atomically by
# the publication step; no existing draft member is trusted.
while IFS= read -r manifest; do
  tag=$(jq -r '.tag' "$manifest")
  set +e
  release_json=$($gh_bin release view "$tag" --repo "$github_repository" --json tagName,isDraft,assets 2>&1)
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
  [[ $(jq -r '.isDraft' <<<"$release_json") == false ]] || continue

  mapfile -t expected_names < <(jq -r '.assets[].name' "$manifest"; basename "$manifest")
  mapfile -t actual_names < <(jq -r '.assets[].name' <<<"$release_json" | sort)
  mapfile -t expected_sorted < <(printf '%s\n' "${expected_names[@]}" | sort)
  [[ "${actual_names[*]}" == "${expected_sorted[*]}" ]] || {
    echo "published GitHub Release $tag is not manifest-complete" >&2
    exit 1
  }
  for name in "${expected_names[@]}"; do
    expected_path="$manifests/$(basename "$manifest")"
    if [[ "$name" != "$(basename "$manifest")" ]]; then
      relative=$(jq -r --arg name "$name" '.assets[] | select(.name == $name) | .path' "$manifest")
      expected_path="$candidate/$relative"
    fi
    destination=$(mktemp -d)
    $gh_bin release download "$tag" --repo "$github_repository" --pattern "$name" --dir "$destination"
    cmp "$expected_path" "$destination/$name" || {
      echo "published GitHub Release $tag asset $name conflicts with the complete manifest" >&2
      exit 1
    }
  done
done < <(find "$manifests" -maxdepth 1 -name 'release-manifest-*.json' -type f | sort)
