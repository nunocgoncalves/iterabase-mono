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
  gh api "repos/$repository/releases/$RELEASE_ID" > "$prefix-by-id.json" || return 1
  gh api "repos/$repository/releases/tags/$GATE_TAG" > "$prefix-by-tag.json" || return 1
  python3 "$VALIDATOR" "${validation_args[@]}" \
    --release "$prefix-by-id.json" || return 1
  python3 "$VALIDATOR" "${validation_args[@]}" \
    --release "$prefix-by-tag.json" || return 1
  cp "$prefix-by-id.json" "$prefix.json" || return 1
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
  python3 "$VALIDATOR" validate-assets --directory "$directory" || return 1
  cmp "$work/expected/probe.txt" "$directory/probe.txt" || return 1
  cmp "$work/expected/release-manifest.json" \
    "$directory/release-manifest.json" || return 1
}

verify_attestations() {
  local prefix=$1 directory=$2 name
  gh release verify "$GATE_TAG" --repo "$repository" --format json \
    > "$prefix-release-attestation.json" || return 1
  python3 "$VALIDATOR" validate-attestation \
    --attestation "$prefix-release-attestation.json" || return 1
  for name in probe.txt release-manifest.json; do
    gh release verify-asset "$GATE_TAG" "$directory/$name" \
      --repo "$repository" --format json \
      > "$prefix-$name-attestation.json" || return 1
    python3 "$VALIDATOR" validate-attestation \
      --attestation "$prefix-$name-attestation.json" || return 1
  done
}

capture_state() {
  local stem=$1
  local directory="$work/$stem-assets"
  rm -rf "$directory" || return 1
  mkdir -p "$directory" || return 1
  fetch_release "$work/$stem-release" || return 1
  verify_remote_tag || return 1
  gh release download "$GATE_TAG" --repo "$repository" \
    --pattern probe.txt --pattern release-manifest.json --dir "$directory" || return 1
  verify_downloaded_assets "$directory" || return 1
  verify_attestations "$work/$stem" "$directory" || return 1
  python3 "$VALIDATOR" state \
    --release "$work/$stem-release.json" \
    --attestation "$work/$stem-release-attestation.json" \
    --probe-attestation "$work/$stem-probe.txt-attestation.json" \
    --manifest-attestation "$work/$stem-release-manifest.json-attestation.json" \
    --directory "$directory" \
    --tag-object "$REMOTE_TAG_OBJECT" \
    --tag-target "$REMOTE_TAG_TARGET" > "$work/$stem-state.json"
}

run_probe() {
  local operation=$1 stem=$2 spec target output status=0
  local protocol_valid=true state=invalid protocol_summary=invalid state_sha
  spec=$(python3 "$VALIDATOR" probe-spec --operation "$operation")
  target=$(jq -er '.target' <<< "$spec")
  output="$work/$stem-protocol.out"

  case "$operation" in
    asset-upload)
      gh api --include --silent --method POST \
        -H 'Content-Type: application/octet-stream' \
        --input "$work/forbidden.txt" "$target" > "$output" 2> /dev/null || status=$?
      ;;
    asset-deletion)
      gh api --include --silent --method DELETE "$target" \
        > "$output" 2> /dev/null || status=$?
      ;;
    tag-update)
      git push --porcelain --force origin "$target" \
        > "$output" 2> /dev/null || status=$?
      ;;
    tag-deletion)
      git push --porcelain origin "$target" \
        > "$output" 2> /dev/null || status=$?
      ;;
  esac

  python3 "$VALIDATOR" require-probe-result \
    --operation "$operation" --status "$status" --output "$output" \
    > "$work/$stem-protocol.json" 2> "$work/$stem-protocol.err" || \
    protocol_valid=false
  rm -f "$output"
  if capture_state "$stem" 2> "$work/$stem-state.err"; then
    state=changed
    if python3 "$VALIDATOR" compare-state \
      --before "$work/baseline-state.json" --after "$work/$stem-state.json" \
      >> "$work/$stem-state.err" 2>&1; then
      state=unchanged
    fi
  fi
  rm -f "$work/$stem-state.err"

  if [[ "$protocol_valid" == true ]]; then
    protocol_summary=$(jq -r 'if .protocol == "http" then "http-\(.http_status)" else "git-porcelain-\(.flag)-\(.classification)-refspec-matching" end' "$work/$stem-protocol.json")
  else
    cat "$work/$stem-protocol.err" >&2
  fi
  rm -f "$work/$stem-protocol.err"
  if [[ "$protocol_valid" != true || "$state" != unchanged ]]; then
    echo "retained release gate: probe mismatch: operation=$operation status=$status protocol=$protocol_summary state=$state" >&2
    return 1
  fi

  state_sha=$(sha256sum "$work/$stem-state.json" | awk '{print $1}')
  jq --arg state_sha256 "$state_sha" \
    '. + {state:"unchanged",state_sha256:$state_sha256}' \
    "$work/$stem-protocol.json" > "$work/$stem-result.json"
  rm -f "$work/$stem-protocol.json"
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
run_probe asset-upload after-upload
run_probe asset-deletion after-asset-deletion

mutation_sha=$(git rev-list --max-parents=0 "$control_sha" | tail -1)
[[ "$mutation_sha" =~ ^[0-9a-f]{40}$ && "$mutation_sha" != "$EXPECTED_SOURCE" ]] || {
  echo "could not select a safe, different tag-mutation target" >&2
  exit 1
}
git tag -f "$GATE_TAG" "$mutation_sha"
run_probe tag-update after-tag-update
run_probe tag-deletion after-tag-deletion

mkdir -p "$(dirname "$evidence")"
jq -n -cS \
  --arg repository "$repository" \
  --arg control_sha "$control_sha" \
  --arg run_id "$run_id" \
  --arg baseline_state_sha256 "$baseline_sha" \
  --argjson title_restored "$title_restored" \
  --slurpfile state "$work/after-tag-deletion-state.json" \
  --slurpfile upload "$work/after-upload-result.json" \
  --slurpfile delete_asset "$work/after-asset-deletion-result.json" \
  --slurpfile update_tag "$work/after-tag-update-result.json" \
  --slurpfile delete_tag "$work/after-tag-deletion-result.json" \
  '{schema_version:4,repository:$repository,control_sha:$control_sha,run_id:$run_id,immutable_authority:($state[0].immutable_authority + {release_attestation_verified:true,per_asset_attestations_verified:true,baseline_state_sha256:$baseline_state_sha256,all_probe_states_unchanged:true,probe_results:{asset_upload:$upload[0],asset_deletion:$delete_asset[0],tag_update:$update_tag[0],tag_deletion:$delete_tag[0]}}),governed_presentation:($state[0].governed_presentation + {title_restored:$title_restored})}' \
  > "$evidence"
