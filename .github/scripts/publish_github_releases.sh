#!/usr/bin/env bash
set -euo pipefail

candidate=${1:?usage: publish_github_releases.sh CANDIDATE MANIFESTS REPOSITORY}
manifests=${2:?usage: publish_github_releases.sh CANDIDATE MANIFESTS REPOSITORY}
repository=${3:-${GITHUB_REPOSITORY:-}}
[[ -n "$repository" ]] || { echo "GitHub repository is required" >&2; exit 2; }

repo_root=$(git rev-parse --show-toplevel)
python3 "$repo_root/.github/scripts/release.py" verify-release-manifests \
  --candidate "$candidate" --directory "$manifests" >/dev/null

verify_release() {
  local tag=$1 manifest=$2 expected_dir=$3 expected_draft=$4
  local release_json actual_dir name expected_sha actual_sha expected_size actual_size
  release_json=$(gh release view "$tag" --repo "$repository" --json tagName,targetCommitish,name,body,isDraft,isPrerelease,assets)
  jq -e \
    --slurpfile expected "$manifest" \
    --argjson draft "$expected_draft" '
      .tagName == $expected[0].tag and
      .targetCommitish == $expected[0].release_metadata.target_commitish and
      .name == $expected[0].release_metadata.title and
      .body == $expected[0].release_metadata.notes and
      .isDraft == $draft and
      .isPrerelease == $expected[0].release_metadata.prerelease
    ' <<<"$release_json" >/dev/null || {
      echo "release $tag metadata does not match its governed manifest" >&2
      return 1
    }
  if [[ "$expected_draft" == false ]]; then
    [[ $(gh api "repos/$repository/releases/tags/$tag" --jq '.immutable') == true ]] || {
      echo "published release $tag is not immutable" >&2
      return 1
    }
  fi
  mapfile -t expected_names < <(jq -r '.assets[].name' "$manifest"; basename "$manifest")
  mapfile -t actual_names < <(jq -r '.assets[].name' <<<"$release_json" | sort)
  mapfile -t expected_sorted < <(printf '%s\n' "${expected_names[@]}" | sort)
  [[ "${actual_names[*]}" == "${expected_sorted[*]}" ]] || { echo "release $tag has missing, extra, or duplicate assets" >&2; return 1; }
  actual_dir=$(mktemp -d)
  for name in "${expected_names[@]}"; do
    gh release download "$tag" --repo "$repository" --pattern "$name" --dir "$actual_dir"
    cmp "$expected_dir/$name" "$actual_dir/$name" || { echo "release $tag asset $name conflicts with its draft manifest" >&2; return 1; }
    actual_sha=$(sha256sum "$actual_dir/$name" | awk '{print $1}')
    actual_size=$(jq -r --arg name "$name" '.assets[] | select(.name == $name) | .size' <<<"$release_json")
    if [[ "$name" == "$(basename "$manifest")" ]]; then
      expected_sha=$(sha256sum "$manifest" | awk '{print $1}')
      expected_size=$(wc -c < "$manifest" | tr -d ' ')
    else
      expected_sha=$(jq -r --arg name "$name" '.assets[] | select(.name == $name) | .sha256' "$manifest")
      expected_size=$(jq -r --arg name "$name" '.assets[] | select(.name == $name) | .size' "$manifest")
    fi
    [[ "$actual_sha" == "$expected_sha" && "$actual_size" == "$expected_size" ]] || {
      echo "release $tag asset size or digest mismatch: $name" >&2
      return 1
    }
  done
}

while IFS= read -r manifest; do
  tag=$(jq -r '.tag' "$manifest")
  stage=$(mktemp -d)
  while IFS=$'\t' read -r name path; do
    [[ -f "$candidate/$path" ]] || { echo "missing release member $path" >&2; exit 1; }
    cp "$candidate/$path" "$stage/$name"
  done < <(jq -r '.assets[] | [.name,.path] | @tsv' "$manifest")
  cp "$manifest" "$stage/$(basename "$manifest")"
  notes=$(mktemp)
  # Preserve the governed body byte-for-byte; jq -r would append a newline that
  # is not present in the manifest and make a fresh draft fail verification.
  jq -j '.release_metadata.notes' "$manifest" > "$notes"

  if gh release view "$tag" --repo "$repository" >/dev/null 2>&1; then
    release_json=$(gh release view "$tag" --repo "$repository" --json isDraft)
    if [[ $(jq -r '.isDraft' <<<"$release_json") == false ]]; then
      verify_release "$tag" "$manifest" "$stage" false
      echo "published release $tag already matches exactly; verification-only"
      continue
    fi
    # An unpublished draft has no artifact authority and may be replaced in full
    # on retry. The protected tag is intentionally retained.
    gh release delete "$tag" --repo "$repository" --yes
  fi

  mapfile -t staged_assets < <(find "$stage" -maxdepth 1 -type f | sort)
  gh release create "$tag" "${staged_assets[@]}" --repo "$repository" \
    --verify-tag --draft --target "$(jq -r '.release_metadata.target_commitish' "$manifest")" \
    --title "$(jq -r '.release_metadata.title' "$manifest")" --notes-file "$notes"
  verify_release "$tag" "$manifest" "$stage" true
  gh release edit "$tag" --repo "$repository" --draft=false
  verify_release "$tag" "$manifest" "$stage" false
done < <(find "$manifests" -maxdepth 1 -name 'release-manifest-*.json' -type f | sort)
