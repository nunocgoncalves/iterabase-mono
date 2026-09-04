#!/usr/bin/env bash
set -euo pipefail

run_id=${1:?usage: verify_candidate_run.sh RUN_ID VERIFIED_PLAN REPOSITORY}
plan=${2:?usage: verify_candidate_run.sh RUN_ID VERIFIED_PLAN REPOSITORY}
repository=${3:-${GITHUB_REPOSITORY:-}}
[[ "$run_id" =~ ^[1-9][0-9]*$ ]] || { echo "candidate run ID must be a positive integer" >&2; exit 2; }
[[ -f "$plan" && -n "$repository" ]] || { echo "verified candidate plan and repository are required" >&2; exit 2; }

run=$(gh api "repos/$repository/actions/runs/$run_id")
plan_repository=$(jq -r '.candidate_repository' "$plan")
plan_workflow=$(jq -r '.candidate_workflow' "$plan")
plan_event=$(jq -r '.candidate_event' "$plan")
plan_control_sha=$(jq -r '.candidate_control_sha' "$plan")
plan_attempt=$(jq -r '.run_attempt' "$plan")
plan_run_id=$(jq -r '.run_id' "$plan")

jq -e \
  --arg repository "$repository" \
  --arg workflow "$plan_workflow" \
  --arg event "$plan_event" \
  --arg control_sha "$plan_control_sha" \
  --arg run_id "$plan_run_id" \
  --arg run_attempt "$plan_attempt" '
    .name == "Release candidate" and
    .repository.full_name == $repository and
    .head_repository.full_name == $repository and
    .path == $workflow and
    .event == $event and
    .head_sha == $control_sha and
    (.id | tostring) == $run_id and
    (.run_attempt | tostring) == $run_attempt and
    .head_branch == "master" and
    .conclusion == "success"
  ' <<<"$run" >/dev/null || {
    echo "live candidate run repository/workflow/event/control SHA/run attempt is inconsistent" >&2
    exit 1
  }
[[ "$plan_repository" == "$repository" ]] || {
  echo "candidate plan repository does not match the promotion repository" >&2
  exit 1
}
