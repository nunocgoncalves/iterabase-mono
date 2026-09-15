#!/usr/bin/env bash
set -euo pipefail

candidate=${1:?usage: publish_github_releases.sh CANDIDATE REPOSITORY}
repository=${2:-${GITHUB_REPOSITORY:-}}
[[ -n "$repository" ]] || { echo "GitHub repository is required" >&2; exit 2; }

repo_root=$(git rev-parse --show-toplevel)
plan="$candidate/candidate-plan.json"
python3 "$repo_root/.github/scripts/release.py" verify-candidate --directory "$candidate" >/dev/null
parent_id=$(jq -er '.baseline_anchor_release_id' "$plan")
[[ "$parent_id" =~ ^[1-9][0-9]*$ ]] || { echo "candidate has no exact parent anchor Release ID" >&2; exit 1; }

latest_id() {
  gh api "repos/$repository/releases/latest" --jq '.id'
}

# Promotion checks this before every semantic mutation in the workflow and again
# here before creating Release drafts. A retry after designation may observe only
# the exact intended anchor, which is verified below rather than overwritten.
initial_latest=$(latest_id)
if [[ "$initial_latest" != "$parent_id" ]]; then
  anchor_target=$(jq -r '.targets[-1]' "$plan")
  anchor_tag=$(jq -er --arg target "$anchor_target" '.releases[] | select(.target == $target) | .production_tag' "$plan")
  set +e
  anchor_release=$(gh api "repos/$repository/releases/tags/$anchor_tag" 2>/dev/null)
  status=$?
  set -e
  [[ $status -eq 0 && $(jq -r '.id' <<<"$anchor_release") == "$initial_latest" && $(jq -r '.immutable' <<<"$anchor_release") == true ]] || {
    echo "candidate parent anchor $parent_id is stale (Latest is conflicting Release ID $initial_latest)" >&2
    exit 1
  }
fi

release_ids=$(mktemp)
printf '{}\n' > "$release_ids"
while IFS=$'\t' read -r target tag source_sha; do
  set +e
  release_json=$(gh api "repos/$repository/releases/tags/$tag" 2>/dev/null)
  status=$?
  set -e
  if [[ $status -eq 0 ]]; then
    jq -e --arg tag "$tag" --arg source "$source_sha" '
      .tag_name == $tag and .target_commitish == $source and
      ((.draft == true) or (.draft == false and .prerelease == false and .immutable == true))
    ' <<<"$release_json" >/dev/null || {
      echo "existing Release $tag conflicts with the candidate cohort" >&2
      exit 1
    }
  else
    notes=$(mktemp)
    printf 'Staging complete immutable cohort for %s from %s.\n' "$target" "$source_sha" > "$notes"
    gh release create "$tag" --repo "$repository" --verify-tag --draft --latest=false \
      --target "$source_sha" --title "$tag" --notes-file "$notes"
    release_json=$(gh api "repos/$repository/releases/tags/$tag")
  fi
  release_id=$(jq -er '.id' <<<"$release_json")
  jq --arg target "$target" --argjson id "$release_id" '. + {($target):$id}' "$release_ids" > "$release_ids.next"
  mv "$release_ids.next" "$release_ids"
done < <(jq -r '.releases[] | [.target,.production_tag,$source] | @tsv' --arg source "$(jq -r '.source_sha' "$plan")" "$plan")

tag_objects=$(mktemp)
printf '{}\n' > "$tag_objects"
while IFS=$'\t' read -r target tag source_sha; do
  ref=$(gh api "repos/$repository/git/ref/tags/$tag")
  object_sha=$(jq -er 'select(.object.type == "tag") | .object.sha' <<<"$ref")
  tag_json=$(gh api "repos/$repository/git/tags/$object_sha")
  target_sha=$(jq -er 'select(.object.type == "commit") | .object.sha' <<<"$tag_json")
  [[ "$target_sha" == "$source_sha" ]] || { echo "protected tag $tag does not target exact candidate source" >&2; exit 1; }
  jq --arg target "$target" --arg sha "$object_sha" --arg target_sha "$target_sha" \
    '. + {($target):{sha:$sha,target_sha:$target_sha}}' "$tag_objects" > "$tag_objects.next"
  mv "$tag_objects.next" "$tag_objects"
done < <(jq -r '.source_sha as $source | .releases[] | [.target,.production_tag,$source] | @tsv' "$plan")

chart_digests=$(mktemp)
printf '{}\n' > "$chart_digests"
while IFS=$'\t' read -r name reference; do
  digest=$(docker buildx imagetools inspect "${reference#oci://}" --format '{{json .Manifest.Digest}}' | tr -d '"')
  [[ "$digest" =~ ^sha256:[0-9a-f]{64}$ ]] || { echo "published chart $name has no exact OCI digest" >&2; exit 1; }
  jq --arg name "$name" --arg digest "$digest" '. + {($name):$digest}' "$chart_digests" > "$chart_digests.next"
  mv "$chart_digests.next" "$chart_digests"
done < <(
  jq -r '
    .chart_matrix[] | .version as $version |
    ([{name:(if .chart == "iterabase-platform" then "iterabase-platform-chart" else (.chart + "-chart") end),chart:.chart}]
      + [.companion_recipes[] | {name:.artifact,chart:.chart}])[] |
    [.name,("oci://ghcr.io/nunocgoncalves/iterabase-charts/" + .chart + ":" + $version)] | @tsv
  ' "$plan"
)

python3 "$repo_root/.github/scripts/release.py" final-snapshot \
  --candidate "$candidate" --release-ids "$release_ids" --chart-digests "$chart_digests" \
  --tag-objects "$tag_objects" --output "$candidate/baseline-snapshot.json" >/dev/null
manifests=$(mktemp -d)
python3 "$repo_root/.github/scripts/release.py" release-manifests \
  --candidate "$candidate" --output "$manifests" >/dev/null
python3 "$repo_root/.github/scripts/release.py" verify-release-manifests \
  --candidate "$candidate" --directory "$manifests" >/dev/null

verify_release() {
  local release_id=$1 manifest=$2 expected_dir=$3 expected_draft=$4
  local release_json name asset_id downloaded actual_sha expected_sha expected_size
  release_json=$(gh api "repos/$repository/releases/$release_id")
  jq -e --slurpfile expected "$manifest" --argjson draft "$expected_draft" '
    .id == $expected[0].release_id and
    .tag_name == $expected[0].tag and
    .target_commitish == $expected[0].release_metadata.target_commitish and
    .name == $expected[0].release_metadata.title and
    .body == $expected[0].release_metadata.notes and
    .draft == $draft and .prerelease == false and
    ($draft or .immutable == true)
  ' <<<"$release_json" >/dev/null || {
    echo "Release ID $release_id metadata does not match its schema-v3 manifest" >&2
    return 1
  }
  mapfile -t expected_names < <(jq -r '.assets[].name' "$manifest"; basename "$manifest")
  mapfile -t actual_names < <(jq -r '.assets[].name' <<<"$release_json" | sort)
  mapfile -t expected_sorted < <(printf '%s\n' "${expected_names[@]}" | sort)
  [[ "${actual_names[*]}" == "${expected_sorted[*]}" ]] || {
    echo "Release ID $release_id has missing, extra, or duplicate assets" >&2
    return 1
  }
  for name in "${expected_names[@]}"; do
    asset_id=$(jq -er --arg name "$name" '.assets[] | select(.name == $name) | .id' <<<"$release_json")
    downloaded=$(mktemp)
    gh api -H 'Accept: application/octet-stream' "repos/$repository/releases/assets/$asset_id" > "$downloaded"
    if [[ "$name" == "$(basename "$manifest")" ]]; then
      expected_sha=$(sha256sum "$manifest" | awk '{print $1}')
      expected_size=$(wc -c < "$manifest" | tr -d ' ')
    else
      expected_sha=$(jq -er --arg name "$name" '.assets[] | select(.name == $name) | .sha256' "$manifest")
      expected_size=$(jq -er --arg name "$name" '.assets[] | select(.name == $name) | .size' "$manifest")
    fi
    actual_sha=$(sha256sum "$downloaded" | awk '{print $1}')
    [[ "$actual_sha" == "$expected_sha" && $(wc -c < "$downloaded" | tr -d ' ') == "$expected_size" ]] || {
      echo "Release ID $release_id asset $name size or digest drifted" >&2
      return 1
    }
  done
}

# Drafts are replaceable in full. Their database IDs remain stable so every
# selected Release can carry identical final snapshot bytes that pin all peers.
while IFS= read -r manifest; do
  target=$(jq -r '.target' "$manifest")
  release_id=$(jq -er --arg target "$target" '.[$target]' "$release_ids")
  release_json=$(gh api "repos/$repository/releases/$release_id")
  stage=$(mktemp -d)
  while IFS=$'\t' read -r name path; do
    [[ -f "$candidate/$path" ]] || { echo "missing release member $path" >&2; exit 1; }
    cp "$candidate/$path" "$stage/$name"
  done < <(jq -r '.assets[] | [.name,.path] | @tsv' "$manifest")
  cp "$manifest" "$stage/$(basename "$manifest")"
  if [[ $(jq -r '.draft' <<<"$release_json") == false ]]; then
    verify_release "$release_id" "$manifest" "$stage" false
    continue
  fi
  while IFS= read -r asset_id; do
    gh api --method DELETE "repos/$repository/releases/assets/$asset_id"
  done < <(jq -r '.assets[].id' <<<"$release_json")
  notes=$(mktemp)
  jq -j '.release_metadata.notes' "$manifest" > "$notes"
  gh api --method PATCH "repos/$repository/releases/$release_id" \
    -f "name=$(jq -r '.release_metadata.title' "$manifest")" \
    -f "body=$(cat "$notes")" -F prerelease=false -f make_latest=false >/dev/null
  mapfile -t staged_assets < <(find "$stage" -maxdepth 1 -type f | sort)
  gh release upload "$(jq -r '.tag' "$manifest")" "${staged_assets[@]}" --repo "$repository"
  verify_release "$release_id" "$manifest" "$stage" true

done < <(find "$manifests" -maxdepth 1 -name 'release-manifest-*.json' -type f | sort)

# Publish every complete member explicitly non-Latest. Existing immutable
# members are verification-only; no late upload or metadata rewrite is allowed.
while IFS= read -r manifest; do
  target=$(jq -r '.target' "$manifest")
  release_id=$(jq -er --arg target "$target" '.[$target]' "$release_ids")
  release_json=$(gh api "repos/$repository/releases/$release_id")
  if [[ $(jq -r '.draft' <<<"$release_json") == true ]]; then
    gh api --method PATCH "repos/$repository/releases/$release_id" \
      -F draft=false -F prerelease=false -f make_latest=false >/dev/null
  fi
  for attempt in {1..12}; do
    if verify_release "$release_id" "$manifest" /dev/null false; then break; fi
    [[ $attempt -lt 12 ]] || exit 1
    sleep 5
  done
done < <(find "$manifests" -maxdepth 1 -name 'release-manifest-*.json' -type f | sort)

anchor_target=$(jq -r '.snapshot.anchor_target' "$candidate/baseline-snapshot.json")
anchor_id=$(jq -er --arg target "$anchor_target" '.[$target]' "$release_ids")
current_latest=$(latest_id)
if [[ "$current_latest" == "$parent_id" ]]; then
  # This is the only normal-promotion Latest mutation.
  gh api --method PATCH "repos/$repository/releases/$anchor_id" -f make_latest=true >/dev/null
elif [[ "$current_latest" != "$anchor_id" ]]; then
  echo "Latest changed to conflicting Release ID $current_latest immediately before handoff" >&2
  exit 1
fi
[[ $(latest_id) == "$anchor_id" ]] || { echo "Latest handoff did not select deterministic anchor $anchor_id" >&2; exit 1; }

# Re-resolve the exact new anchor and verify every cohort Release, manifest,
# asset, attestation, image, chart, companion, Forge variant, and fixture.
python3 "$repo_root/.github/scripts/release_baseline.py" resolve \
  --contract "$repo_root/release/targets.json" --expected-anchor-id "$anchor_id" \
  --output "$RUNNER_TEMP/resolved-promoted-baseline.json"
cmp "$candidate/baseline-snapshot.json" "$RUNNER_TEMP/resolved-promoted-baseline.json"
