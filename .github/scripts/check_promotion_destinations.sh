#!/usr/bin/env bash
set -euo pipefail

candidate=${1:?usage: check_promotion_destinations.sh CANDIDATE REPOSITORY_OWNER REPOSITORY}
repository_owner=${2:?usage: check_promotion_destinations.sh CANDIDATE REPOSITORY_OWNER REPOSITORY}
github_repository=${3:-${GITHUB_REPOSITORY:-}}
docker_bin=${DOCKER_BIN:-docker}
helm_bin=${HELM_BIN:-helm}
gh_bin=${GH_BIN:-gh}
plan="$candidate/candidate-plan.json"
repo_root=$(git rev-parse --show-toplevel)

[[ -n "$github_repository" ]] || { echo "GitHub repository is required for Release preflight" >&2; exit 1; }

release_ids=$(mktemp)
published_targets=$(mktemp)
chart_digests=$(mktemp)
tag_objects=$(mktemp)
printf '{}\n' > "$release_ids"
printf '[]\n' > "$published_targets"
printf '{}\n' > "$chart_digests"
printf '{}\n' > "$tag_objects"

# Resolve every exact semantic Release destination before any mutation. Drafts
# remain replaceable in full. If any immutable member already exists, the
# complete selected Release-ID vector must exist so its expected final snapshot
# and schema-v3 manifests can be reconstructed and verified byte-for-byte.
while IFS=$'\t' read -r target tag source; do
  set +e
  release_json=$($gh_bin api "repos/$github_repository/releases/tags/$tag" 2>&1)
  status=$?
  set -e
  if [[ $status -ne 0 ]]; then
    if [[ $status -eq 1 ]] && grep -Eqi '(^|[^0-9])404([^0-9]|$)|not found' <<<"$release_json"; then
      continue
    fi
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
  release_id=$(jq -er '.id | select(type == "number" and . > 0)' <<<"$release_json")
  jq --arg target "$target" --argjson id "$release_id" \
    '. + {($target):$id}' "$release_ids" > "$release_ids.next"
  mv "$release_ids.next" "$release_ids"
  if [[ $(jq -r '.draft' <<<"$release_json") == false ]]; then
    jq --arg target "$target" '. + [$target]' "$published_targets" > "$published_targets.next"
    mv "$published_targets.next" "$published_targets"
  fi
done < <(jq -r '.source_sha as $source | .releases[] | [.target,.production_tag,$source] | @tsv' "$plan")

published_count=$(jq 'length' "$published_targets")
if [[ $published_count -gt 0 ]]; then
  [[ $(jq 'length' "$release_ids") == $(jq '.targets | length' "$plan") ]] || {
    echo "published retry cohort has a missing or ambiguous selected Release destination" >&2
    exit 1
  }
fi

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
    [[ $published_count -eq 0 ]] || {
      echo "published retry cohort is missing semantic image $image_repository:$version" >&2
      exit 1
    }
  else
    echo "could not preflight $image_repository:$version:" >&2
    printf '%s\n' "$output" >&2
    exit 1
  fi
done

while IFS=$'\t' read -r artifact chart version; do
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
    if [[ $published_count -gt 0 ]]; then
      digest=$($docker_bin buildx imagetools inspect "${chart_repository#oci://}:$version" --format '{{json .Manifest.Digest}}' | tr -d '"')
      [[ "$digest" =~ ^sha256:[0-9a-f]{64}$ ]] || {
        echo "$chart_repository:$version has no exact OCI digest" >&2
        exit 1
      }
      jq --arg artifact "$artifact" --arg digest "$digest" \
        '. + {($artifact):$digest}' "$chart_digests" > "$chart_digests.next"
      mv "$chart_digests.next" "$chart_digests"
    fi
  elif [[ $status -eq 1 ]] && grep -Eqi '(^|: )not found$|manifest unknown|name unknown' <<<"$output"; then
    [[ $published_count -eq 0 ]] || {
      echo "published retry cohort is missing semantic chart $chart_repository:$version" >&2
      exit 1
    }
  else
    echo "could not preflight $chart_repository:$version:" >&2
    printf '%s\n' "$output" >&2
    exit 1
  fi
done < <(
  jq -r '
    .chart_matrix[] | .version as $version |
    ([{artifact:(if .chart == "iterabase-platform" then "iterabase-platform-chart" else (.chart + "-chart") end),chart:.chart}]
      + [.companion_recipes[] | {artifact:.artifact,chart:.chart}])[] |
    [.artifact,.chart,$version] | @tsv
  ' "$plan"
)

if [[ $published_count -gt 0 ]]; then
  while IFS=$'\t' read -r target tag source_sha; do
    ref=$($gh_bin api "repos/$github_repository/git/ref/tags/$tag")
    object_sha=$(jq -er 'select(.object.type == "tag") | .object.sha' <<<"$ref")
    tag_json=$($gh_bin api "repos/$github_repository/git/tags/$object_sha")
    target_sha=$(jq -er 'select(.object.type == "commit") | .object.sha' <<<"$tag_json")
    [[ "$target_sha" == "$source_sha" ]] || {
      echo "protected tag $tag does not target exact candidate source" >&2
      exit 1
    }
    jq --arg target "$target" --arg sha "$object_sha" --arg target_sha "$target_sha" \
      '. + {($target):{sha:$sha,target_sha:$target_sha}}' "$tag_objects" > "$tag_objects.next"
    mv "$tag_objects.next" "$tag_objects"
  done < <(jq -r '.source_sha as $source | .releases[] | [.target,.production_tag,$source] | @tsv' "$plan")

  python3 "$repo_root/.github/scripts/release.py" final-snapshot \
    --candidate "$candidate" --release-ids "$release_ids" \
    --chart-digests "$chart_digests" --tag-objects "$tag_objects" \
    --output "$candidate/baseline-snapshot.json" >/dev/null
  manifests=$(mktemp -d)
  python3 "$repo_root/.github/scripts/release.py" release-manifests \
    --candidate "$candidate" --output "$manifests" >/dev/null
  python3 "$repo_root/.github/scripts/release.py" verify-release-manifests \
    --candidate "$candidate" --directory "$manifests" >/dev/null
  python3 "$repo_root/.github/scripts/release_baseline.py" verify-published-members \
    --contract "$repo_root/release/targets.json" \
    --snapshot "$candidate/baseline-snapshot.json" \
    --targets "$published_targets" --manifests "$manifests"
fi
