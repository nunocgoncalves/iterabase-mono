#!/usr/bin/env bash
set -euo pipefail

repository=${1:?usage: run_retained_release_gate.sh REPOSITORY CONTROL_SHA RUN_ID [EVIDENCE]}
control_sha=${2:?usage: run_retained_release_gate.sh REPOSITORY CONTROL_SHA RUN_ID [EVIDENCE]}
run_id=${3:?usage: run_retained_release_gate.sh REPOSITORY CONTROL_SHA RUN_ID [EVIDENCE]}
evidence=${4:-immutable-release-gate-evidence.json}

readonly EXPECTED_REPOSITORY=nunocgoncalves/iterabase-mono
readonly RELEASE_ID=382723775
readonly GATE_TAG=dry-run/immutable-release-gate-v1
readonly EXPECTED_SOURCE=42604a60764816a66d147a89d8d0772c9e0d2491
readonly EXPECTED_TAG_OBJECT=9f529662036d70348379c6c71a13c9242c7155a5
readonly PROBE_ASSET_ID=544335752
readonly VALIDATOR=.github/scripts/retained_release.py

[[ "$repository" == "$EXPECTED_REPOSITORY" ]] || {
  echo "retained release gate is bound to $EXPECTED_REPOSITORY" >&2
  exit 1
}
[[ "$control_sha" =~ ^[0-9a-f]{40}$ ]] || { echo "control SHA must be full" >&2; exit 1; }
[[ "$run_id" =~ ^[1-9][0-9]*$ ]] || { echo "run ID must be numeric" >&2; exit 1; }
[[ -f "$VALIDATOR" ]] || { echo "retained release validator is missing" >&2; exit 1; }

work=${RUNNER_TEMP:?RUNNER_TEMP is required}/retained-release-gate
rm -rf "$work"
mkdir -p "$work/expected" "$work/pre-assets" "$work/post-assets"
printf 'iterabase immutable release gate v1\n' > "$work/expected/probe.txt"
printf '%s\n' '{"assets":[{"name":"probe.txt","sha256":"450a38bb5ae772469148be208a9c794dd5da78bc0a142d026fa3fbc2def354c6","size":36}],"purpose":"non-semantic immutable-release validation","run_id":"33874696044","schema_version":1,"source_sha":"42604a60764816a66d147a89d8d0772c9e0d2491","tag":"dry-run/immutable-release-gate-v1"}' > "$work/expected/release-manifest.json"
python3 "$VALIDATOR" validate-assets --directory "$work/expected"

fetch_release() {
  local prefix=$1 allow_repair=${2:-false}
  local -a validation_args=(validate-release)
  [[ "$allow_repair" == true ]] && validation_args+=(--allow-repair-title)
  gh api "repos/$repository/releases/$RELEASE_ID" > "$prefix-by-id.json"
  gh api "repos/$repository/releases/tags/$GATE_TAG" > "$prefix-by-tag.json"
  python3 "$VALIDATOR" "${validation_args[@]}" --release "$prefix-by-id.json"
  python3 "$VALIDATOR" "${validation_args[@]}" --release "$prefix-by-tag.json"
  cp "$prefix-by-id.json" "$prefix.json"
}

verify_remote_tag() {
  local -a direct_matches peeled_matches
  local direct_ref direct_name peeled_ref peeled_name
  mapfile -t direct_matches < <(git ls-remote --tags origin "refs/tags/$GATE_TAG")
  mapfile -t peeled_matches < <(git ls-remote --tags origin "refs/tags/$GATE_TAG^{}")
  [[ ${#direct_matches[@]} -eq 1 && ${#peeled_matches[@]} -eq 1 ]] || {
    echo "retained tag must resolve to exactly one annotated object and target" >&2
    return 1
  }
  IFS=$'\t' read -r direct_ref direct_name <<< "${direct_matches[0]}"
  IFS=$'\t' read -r peeled_ref peeled_name <<< "${peeled_matches[0]}"
  [[ "$direct_name" == "refs/tags/$GATE_TAG" && "$peeled_name" == "refs/tags/$GATE_TAG^{}" ]] || {
    echo "retained tag refs are malformed" >&2
    return 1
  }
  [[ "$direct_ref" == "$EXPECTED_TAG_OBJECT" && "$peeled_ref" == "$EXPECTED_SOURCE" ]] || {
    echo "retained tag object or target does not match authority" >&2
    return 1
  }
  REMOTE_TAG_OBJECT=$direct_ref
  REMOTE_TAG_TARGET=$peeled_ref
}

verify_downloaded_assets() {
  local directory=$1
  python3 "$VALIDATOR" validate-assets --directory "$directory"
  cmp "$work/expected/probe.txt" "$directory/probe.txt"
  cmp "$work/expected/release-manifest.json" "$directory/release-manifest.json"
}

verify_attestations() {
  local prefix=$1 directory=$2 name
  gh release verify "$GATE_TAG" --repo "$repository" --format json > "$prefix-release-attestation.json"
  python3 "$VALIDATOR" validate-attestation --attestation "$prefix-release-attestation.json"
  for name in probe.txt release-manifest.json; do
    gh release verify-asset "$GATE_TAG" "$directory/$name" \
      --repo "$repository" --format json > "$prefix-$name-attestation.json"
    python3 "$VALIDATOR" validate-attestation --attestation "$prefix-$name-attestation.json"
  done
}

fetch_release "$work/initial-release" true
verify_remote_tag
gh release download "$GATE_TAG" --repo "$repository" \
  --pattern probe.txt --pattern release-manifest.json --dir "$work/pre-assets"
verify_downloaded_assets "$work/pre-assets"
verify_attestations "$work/pre" "$work/pre-assets"

# GitHub's immutable contract does not lock presentation metadata. Permit only
# the one observed failed-probe value, and repair it only after all immutable
# release, tag, byte, and attestation authority above has been established.
fetch_release "$work/repair-release" true
title=$(jq -r '.name' "$work/repair-release.json")
title_restored=false
if [[ "$title" == forbidden ]]; then
  gh release edit "$GATE_TAG" --repo "$repository" --title "$GATE_TAG"
  title_restored=true
fi

fetch_release "$work/before-release"
verify_remote_tag
python3 "$VALIDATOR" state \
  --release "$work/before-release.json" \
  --attestation "$work/pre-release-attestation.json" \
  --directory "$work/pre-assets" \
  --tag-object "$REMOTE_TAG_OBJECT" \
  --tag-target "$REMOTE_TAG_TARGET" > "$work/before-state.json"
before_sha=$(sha256sum "$work/before-state.json" | awk '{print $1}')

printf 'forbidden late member\n' > "$work/forbidden.txt"
upload_status=0
gh release upload "$GATE_TAG" "$work/forbidden.txt" --repo "$repository" \
  > "$work/upload.out" 2>&1 || upload_status=$?
python3 "$VALIDATOR" require-denial --status "$upload_status" \
  --output "$work/upload.out" --operation 'asset upload'

delete_asset_status=0
gh api --method DELETE "repos/$repository/releases/assets/$PROBE_ASSET_ID" \
  > "$work/delete-asset.out" 2>&1 || delete_asset_status=$?
python3 "$VALIDATOR" require-denial --status "$delete_asset_status" \
  --output "$work/delete-asset.out" --operation 'asset deletion'

mutation_sha=$(git rev-list --max-parents=0 "$control_sha" | tail -1)
[[ "$mutation_sha" =~ ^[0-9a-f]{40}$ && "$mutation_sha" != "$EXPECTED_SOURCE" ]] || {
  echo "could not select a safe, different tag-mutation target" >&2
  exit 1
}
git tag -f "$GATE_TAG" "$mutation_sha"
update_tag_status=0
git push --force origin "refs/tags/$GATE_TAG" \
  > "$work/update-tag.out" 2>&1 || update_tag_status=$?
python3 "$VALIDATOR" require-denial --status "$update_tag_status" \
  --output "$work/update-tag.out" --operation 'release tag update'

delete_tag_status=0
git push --delete origin "refs/tags/$GATE_TAG" \
  > "$work/delete-tag.out" 2>&1 || delete_tag_status=$?
python3 "$VALIDATOR" require-denial --status "$delete_tag_status" \
  --output "$work/delete-tag.out" --operation 'release tag deletion'

fetch_release "$work/post-release"
verify_remote_tag
gh release download "$GATE_TAG" --repo "$repository" \
  --pattern probe.txt --pattern release-manifest.json --dir "$work/post-assets"
verify_downloaded_assets "$work/post-assets"
verify_attestations "$work/post" "$work/post-assets"
python3 "$VALIDATOR" state \
  --release "$work/post-release.json" \
  --attestation "$work/post-release-attestation.json" \
  --directory "$work/post-assets" \
  --tag-object "$REMOTE_TAG_OBJECT" \
  --tag-target "$REMOTE_TAG_TARGET" > "$work/post-state.json"
python3 "$VALIDATOR" compare-state \
  --before "$work/before-state.json" --after "$work/post-state.json"
after_sha=$(sha256sum "$work/post-state.json" | awk '{print $1}')

mkdir -p "$(dirname "$evidence")"
jq -n -cS \
  --arg repository "$repository" \
  --arg control_sha "$control_sha" \
  --arg run_id "$run_id" \
  --arg before_state_sha256 "$before_sha" \
  --arg after_state_sha256 "$after_sha" \
  --argjson title_restored "$title_restored" \
  --argjson upload_status "$upload_status" \
  --argjson delete_asset_status "$delete_asset_status" \
  --argjson update_tag_status "$update_tag_status" \
  --argjson delete_tag_status "$delete_tag_status" \
  --slurpfile state "$work/post-state.json" \
  '{schema_version:3,repository:$repository,control_sha:$control_sha,run_id:$run_id,immutable_authority:($state[0].immutable_authority + {release_attestation_verified:true,per_asset_attestations_verified:true,post_state_unchanged:($before_state_sha256 == $after_state_sha256),before_state_sha256:$before_state_sha256,after_state_sha256:$after_state_sha256,safe_denials:{classification:"immutable-release",asset_upload:$upload_status,asset_delete:$delete_asset_status,tag_update:$update_tag_status,tag_delete:$delete_tag_status}}),governed_presentation:($state[0].governed_presentation + {title_restored:$title_restored})}' \
  > "$evidence"
