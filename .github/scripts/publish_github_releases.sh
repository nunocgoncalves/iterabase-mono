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
  local tag=$1 manifest=$2 expected_dir=$3 release_json actual_dir name expected_sha actual_sha
  release_json=$(gh release view "$tag" --repo "$repository" --json tagName,isDraft,assets)
  [[ $(jq -r '.tagName' <<<"$release_json") == "$tag" ]] || { echo "release tag mismatch: $tag" >&2; return 1; }
  mapfile -t expected_names < <(jq -r '.assets[].name' "$manifest"; basename "$manifest")
  mapfile -t actual_names < <(jq -r '.assets[].name' <<<"$release_json" | sort)
  mapfile -t expected_sorted < <(printf '%s\n' "${expected_names[@]}" | sort)
  [[ "${actual_names[*]}" == "${expected_sorted[*]}" ]] || { echo "release $tag has missing, extra, or duplicate assets" >&2; return 1; }
  actual_dir=$(mktemp -d)
  for name in "${expected_names[@]}"; do
    gh release download "$tag" --repo "$repository" --pattern "$name" --dir "$actual_dir"
    cmp "$expected_dir/$name" "$actual_dir/$name" || { echo "release $tag asset $name conflicts with its draft manifest" >&2; return 1; }
    actual_sha=$(sha256sum "$actual_dir/$name" | awk '{print $1}')
    if [[ "$name" == "$(basename "$manifest")" ]]; then
      expected_sha=$(sha256sum "$manifest" | awk '{print $1}')
    else
      expected_sha=$(jq -r --arg name "$name" '.assets[] | select(.name == $name) | .sha256' "$manifest")
    fi
    [[ "$actual_sha" == "$expected_sha" ]] || { echo "release $tag asset digest mismatch: $name" >&2; return 1; }
  done
}

while IFS= read -r manifest; do
  target=$(jq -r '.target' "$manifest")
  version=$(jq -r '.version' "$manifest")
  tag=$(jq -r '.tag' "$manifest")
  source_sha=$(jq -r '.source_sha' "$manifest")
  candidate_run_id=$(jq -r '.candidate_run_id' "$manifest")
  stage=$(mktemp -d)
  while IFS=$'\t' read -r name path; do
    [[ -f "$candidate/$path" ]] || { echo "missing release member $path" >&2; exit 1; }
    cp "$candidate/$path" "$stage/$name"
  done < <(jq -r '.assets[] | [.name,.path] | @tsv' "$manifest")
  cp "$manifest" "$stage/$(basename "$manifest")"
  notes=$(mktemp)
  cat >"$notes" <<EOF
Release of **$target $version** from \`$source_sha\` as part of candidate run \`$candidate_run_id\`.

Built and validated once, staged as a complete draft, and published without rebuilding. The attached release manifest binds every exact member.
EOF

  if gh release view "$tag" --repo "$repository" >/dev/null 2>&1; then
    release_json=$(gh release view "$tag" --repo "$repository" --json isDraft)
    if [[ $(jq -r '.isDraft' <<<"$release_json") == false ]]; then
      verify_release "$tag" "$manifest" "$stage"
      echo "published release $tag already matches exactly; verification-only"
      continue
    fi
    # An unpublished draft has no artifact authority and may be replaced in full
    # on retry. The protected tag is intentionally retained.
    gh release delete "$tag" --repo "$repository" --yes
  fi

  mapfile -t staged_assets < <(find "$stage" -maxdepth 1 -type f | sort)
  gh release create "$tag" "${staged_assets[@]}" --repo "$repository" \
    --verify-tag --draft --title "$tag" --notes-file "$notes"
  [[ $(gh release view "$tag" --repo "$repository" --json isDraft --jq '.isDraft') == true ]]
  verify_release "$tag" "$manifest" "$stage"
  gh release edit "$tag" --repo "$repository" --draft=false
  [[ $(gh release view "$tag" --repo "$repository" --json isDraft --jq '.isDraft') == false ]]
  verify_release "$tag" "$manifest" "$stage"
done < <(find "$manifests" -maxdepth 1 -name 'release-manifest-*.json' -type f | sort)
