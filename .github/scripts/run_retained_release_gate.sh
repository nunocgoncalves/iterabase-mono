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
readonly UPLOAD_ENDPOINT="https://uploads.github.com/repos/$EXPECTED_REPOSITORY/releases/$RELEASE_ID/assets?name=forbidden.txt"
readonly DELETE_ASSET_ENDPOINT="repos/$EXPECTED_REPOSITORY/releases/assets/$PROBE_ASSET_ID"
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
mkdir -p "$work/expected" "$work/initial-assets"
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

capture_state() {
  local stem=$1
  local directory="$work/$stem-assets"
  rm -rf "$directory"
  mkdir -p "$directory"
  fetch_release "$work/$stem-release"
  verify_remote_tag
  gh release download "$GATE_TAG" --repo "$repository" \
    --pattern probe.txt --pattern release-manifest.json --dir "$directory"
  verify_downloaded_assets "$directory"
  verify_attestations "$work/$stem" "$directory"
  python3 "$VALIDATOR" state \
    --release "$work/$stem-release.json" \
    --attestation "$work/$stem-release-attestation.json" \
    --probe-attestation "$work/$stem-probe.txt-attestation.json" \
    --manifest-attestation "$work/$stem-release-manifest.json-attestation.json" \
    --directory "$directory" \
    --tag-object "$REMOTE_TAG_OBJECT" \
    --tag-target "$REMOTE_TAG_TARGET" > "$work/$stem-state.json"
}

capture_and_compare() {
  local operation=$1 process_status=$2 stem=$3 protocol_valid=$4
  local state_error="$work/$stem-state.err"
  if ! capture_state "$stem" 2> "$state_error"; then
    rm -f "$state_error"
    echo "retained release gate: probe state mismatch: operation=$operation status=$process_status protocol_valid=$protocol_valid state=invalid result=fresh-state-validation-failed" >&2
    return 1
  fi
  rm -f "$state_error"
  python3 "$VALIDATOR" compare-state \
    --before "$work/baseline-state.json" \
    --after "$work/$stem-state.json" \
    --operation "$operation" \
    --status "$process_status" \
    --protocol-valid "$protocol_valid" > "$work/$stem-comparison.tmp.json"
  local state_sha
  state_sha=$(sha256sum "$work/$stem-state.json" | awk '{print $1}')
  jq --arg state_sha256 "$state_sha" \
    '. + {state_sha256:$state_sha256}' \
    "$work/$stem-comparison.tmp.json" > "$work/$stem-comparison.json"
  rm -f "$work/$stem-comparison.tmp.json"
}

# Establish immutable identity before the sole guarded presentation repair.
fetch_release "$work/initial-release" true
verify_remote_tag
gh release download "$GATE_TAG" --repo "$repository" \
  --pattern probe.txt --pattern release-manifest.json --dir "$work/initial-assets"
verify_downloaded_assets "$work/initial-assets"
verify_attestations "$work/initial" "$work/initial-assets"

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

# The exact baseline includes fresh release and per-asset attestations. Every
# probe is provisional until a fresh complete snapshot matches this baseline.
capture_state baseline
baseline_sha=$(sha256sum "$work/baseline-state.json" | awk '{print $1}')

printf 'forbidden late member\n' > "$work/forbidden.txt"
upload_status=0
gh api --include --silent --method POST \
  -H 'Content-Type: application/octet-stream' \
  --input "$work/forbidden.txt" \
  "$UPLOAD_ENDPOINT" \
  > "$work/upload-http.out" 2> /dev/null || upload_status=$?
upload_protocol_valid=true
python3 "$VALIDATOR" require-http-result --status "$upload_status" \
  --output "$work/upload-http.out" --operation 'asset upload' \
  --endpoint "$UPLOAD_ENDPOINT" \
  > "$work/upload-protocol.json" 2> "$work/upload-protocol.err" || \
  upload_protocol_valid=false
rm -f "$work/upload-http.out"
capture_and_compare 'asset upload' "$upload_status" after-upload "$upload_protocol_valid"
if [[ "$upload_protocol_valid" != true ]]; then
  cat "$work/upload-protocol.err" >&2
  echo "retained release gate: probe state result: operation=asset-upload status=$upload_status protocol_valid=false state=unchanged" >&2
  exit 1
fi
rm -f "$work/upload-protocol.err"

delete_asset_status=0
gh api --include --silent --method DELETE \
  "$DELETE_ASSET_ENDPOINT" \
  > "$work/delete-asset-http.out" 2> /dev/null || delete_asset_status=$?
delete_asset_protocol_valid=true
python3 "$VALIDATOR" require-http-result --status "$delete_asset_status" \
  --output "$work/delete-asset-http.out" --operation 'asset deletion' \
  --endpoint "$DELETE_ASSET_ENDPOINT" \
  > "$work/delete-asset-protocol.json" 2> "$work/delete-asset-protocol.err" || \
  delete_asset_protocol_valid=false
rm -f "$work/delete-asset-http.out"
capture_and_compare 'asset deletion' "$delete_asset_status" after-asset-deletion "$delete_asset_protocol_valid"
if [[ "$delete_asset_protocol_valid" != true ]]; then
  cat "$work/delete-asset-protocol.err" >&2
  echo "retained release gate: probe state result: operation=asset-deletion status=$delete_asset_status protocol_valid=false state=unchanged" >&2
  exit 1
fi
rm -f "$work/delete-asset-protocol.err"

mutation_sha=$(git rev-list --max-parents=0 "$control_sha" | tail -1)
[[ "$mutation_sha" =~ ^[0-9a-f]{40}$ && "$mutation_sha" != "$EXPECTED_SOURCE" ]] || {
  echo "could not select a safe, different tag-mutation target" >&2
  exit 1
}
git tag -f "$GATE_TAG" "$mutation_sha"
update_tag_status=0
git push --porcelain --force origin \
  "refs/tags/$GATE_TAG:refs/tags/$GATE_TAG" \
  > "$work/update-tag-porcelain.out" 2> /dev/null || update_tag_status=$?
update_tag_protocol_valid=true
python3 "$VALIDATOR" require-git-result --status "$update_tag_status" \
  --output "$work/update-tag-porcelain.out" --operation 'release tag update' \
  > "$work/update-tag-protocol.json" 2> "$work/update-tag-protocol.err" || \
  update_tag_protocol_valid=false
rm -f "$work/update-tag-porcelain.out"
capture_and_compare 'release tag update' "$update_tag_status" after-tag-update "$update_tag_protocol_valid"
if [[ "$update_tag_protocol_valid" != true ]]; then
  cat "$work/update-tag-protocol.err" >&2
  echo "retained release gate: probe state result: operation=release-tag-update status=$update_tag_status protocol_valid=false state=unchanged" >&2
  exit 1
fi
rm -f "$work/update-tag-protocol.err"

delete_tag_status=0
git push --porcelain origin ":refs/tags/$GATE_TAG" \
  > "$work/delete-tag-porcelain.out" 2> /dev/null || delete_tag_status=$?
delete_tag_protocol_valid=true
python3 "$VALIDATOR" require-git-result --status "$delete_tag_status" \
  --output "$work/delete-tag-porcelain.out" --operation 'release tag deletion' \
  > "$work/delete-tag-protocol.json" 2> "$work/delete-tag-protocol.err" || \
  delete_tag_protocol_valid=false
rm -f "$work/delete-tag-porcelain.out"
capture_and_compare 'release tag deletion' "$delete_tag_status" after-tag-deletion "$delete_tag_protocol_valid"
if [[ "$delete_tag_protocol_valid" != true ]]; then
  cat "$work/delete-tag-protocol.err" >&2
  echo "retained release gate: probe state result: operation=release-tag-deletion status=$delete_tag_status protocol_valid=false state=unchanged" >&2
  exit 1
fi
rm -f "$work/delete-tag-protocol.err"

mkdir -p "$(dirname "$evidence")"
jq -n -cS \
  --arg repository "$repository" \
  --arg control_sha "$control_sha" \
  --arg run_id "$run_id" \
  --arg baseline_state_sha256 "$baseline_sha" \
  --argjson title_restored "$title_restored" \
  --slurpfile state "$work/after-tag-deletion-state.json" \
  --slurpfile upload_protocol "$work/upload-protocol.json" \
  --slurpfile upload_state "$work/after-upload-comparison.json" \
  --slurpfile delete_asset_protocol "$work/delete-asset-protocol.json" \
  --slurpfile delete_asset_state "$work/after-asset-deletion-comparison.json" \
  --slurpfile update_tag_protocol "$work/update-tag-protocol.json" \
  --slurpfile update_tag_state "$work/after-tag-update-comparison.json" \
  --slurpfile delete_tag_protocol "$work/delete-tag-protocol.json" \
  --slurpfile delete_tag_state "$work/after-tag-deletion-comparison.json" \
  '{schema_version:4,repository:$repository,control_sha:$control_sha,run_id:$run_id,immutable_authority:($state[0].immutable_authority + {release_attestation_verified:true,per_asset_attestations_verified:true,baseline_state_sha256:$baseline_state_sha256,all_probe_states_unchanged:true,probe_results:{asset_upload:{protocol:$upload_protocol[0],state:$upload_state[0]},asset_deletion:{protocol:$delete_asset_protocol[0],state:$delete_asset_state[0]},tag_update:{protocol:$update_tag_protocol[0],state:$update_tag_state[0]},tag_deletion:{protocol:$delete_tag_protocol[0],state:$delete_tag_state[0]}}}),governed_presentation:($state[0].governed_presentation + {title_restored:$title_restored})}' \
  > "$evidence"
