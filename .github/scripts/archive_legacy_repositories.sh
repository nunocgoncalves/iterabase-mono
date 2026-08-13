#!/usr/bin/env bash
set -euo pipefail

apply=false
reaper_run_id=
while (($# > 0)); do
  case "$1" in
    --apply)
      apply=true
      shift
      ;;
    --reaper-run-id)
      [[ $# -ge 2 ]] || { echo "--reaper-run-id requires a value" >&2; exit 2; }
      reaper_run_id=$2
      shift 2
      ;;
    *)
      echo "usage: $0 --reaper-run-id RUN_ID [--apply]" >&2
      exit 2
      ;;
  esac
done
[[ "$reaper_run_id" =~ ^[0-9]+$ ]] || {
  echo "usage: $0 --reaper-run-id RUN_ID [--apply]" >&2
  exit 2
}

for command in gh jq; do
  command -v "$command" >/dev/null || {
    echo "legacy archive failed: $command is required" >&2
    exit 1
  }
done

owner=nunocgoncalves
monorepo="$owner/iterabase-mono"
pointer="https://github.com/$monorepo"
legacy_repositories=(control-plane inference-gateway forge iterabase-charts)
legacy_heads=(
  c63eea9d21c367a3e5fd91431bedc853fb15a16b
  cf093df2cdca30e916cb340d3e5dc1ab29c49989
  56afae7b21f97a1c40c81705954756ef16f46674
  0d97d50962afcd03aa474f096a8948f0e1dcd8b5
)

fail() {
  printf 'legacy archive failed: %s\n' "$*" >&2
  exit 1
}

master_sha=$(gh api "repos/$monorepo/commits/master" --jq '.sha')
checks=$(gh api "repos/$monorepo/commits/$master_sha/check-runs?per_page=100")
for context in 'CI / required' 'E2E / required'; do
  jq -e --arg context "$context" '
    any(.check_runs[]; .name == $context and .status == "completed" and .conclusion == "success")
  ' <<<"$checks" >/dev/null || fail "$context did not pass on current master $master_sha"
done

reaper_run=$(gh api "repos/$monorepo/actions/runs/$reaper_run_id")
jq -e --arg master_sha "$master_sha" '
  .path == ".github/workflows/reaper.yml" and
  .event == "workflow_dispatch" and
  .head_branch == "master" and
  .head_sha == $master_sha and
  .status == "completed" and
  .conclusion == "success"
' <<<"$reaper_run" >/dev/null \
  || fail "run $reaper_run_id is not a successful manual root reaper run for current master $master_sha"

CHECK_ARTIFACTS=true "$(dirname "$0")/audit_source_authority.sh" transition

printf 'cutover preflight passed for master %s\n' "$master_sha"
printf 'legacy repositories to archive at imported heads:\n'
for index in "${!legacy_repositories[@]}"; do
  printf '  %s/%s %s\n' "$owner" "${legacy_repositories[$index]}" "${legacy_heads[$index]}"
done

if [[ "$apply" != true ]]; then
  echo 'dry run only; repeat with --apply to disable legacy workflows, add pointers, and archive'
  exit 0
fi

for repository in "${legacy_repositories[@]}"; do
  full_name="$owner/$repository"
  archived=$(gh api "repos/$full_name" --jq '.archived')
  if [[ "$archived" == true ]]; then
    printf '%s is already archived; verifying final state\n' "$full_name"
    continue
  fi

  description="Historical source frozen at the monorepo import head. Active development: $pointer"
  gh api --method PATCH "repos/$full_name" \
    -f description="$description" \
    -f homepage="$pointer" >/dev/null

  while IFS=$'\t' read -r workflow_id workflow_state; do
    [[ -n "$workflow_id" ]] || continue
    if [[ "$workflow_state" == active ]]; then
      gh workflow disable "$workflow_id" --repo "$full_name"
    fi
  done < <(gh api "repos/$full_name/actions/workflows?per_page=100" \
    --jq '.workflows[] | [.id,.state] | @tsv')

  gh api --method PATCH "repos/$full_name" -F archived=true >/dev/null
  printf 'archived %s\n' "$full_name"
done

"$(dirname "$0")/audit_source_authority.sh" archived
printf 'one-way source-authority cutover completed at monorepo master %s\n' "$master_sha"
