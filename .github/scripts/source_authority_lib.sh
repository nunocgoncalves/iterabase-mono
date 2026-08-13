#!/usr/bin/env bash

is_repository_workflow_path() {
  [[ $1 == .github/workflows/* ]]
}

list_legacy_workflows() {
  local repository=$1
  gh api --paginate "repos/$repository/actions/workflows?per_page=100" \
    --jq '.workflows[] | [.id,.path,.state] | @tsv'
}

disable_repository_workflows() {
  local repository=$1
  local workflow_listing workflow_id workflow_path workflow_state

  if ! workflow_listing=$(list_legacy_workflows "$repository"); then
    printf 'failed to list workflows in %s\n' "$repository" >&2
    return 1
  fi

  while IFS=$'\t' read -r workflow_id workflow_path workflow_state; do
    [[ -n $workflow_id ]] || continue
    if [[ $workflow_state == active ]] && is_repository_workflow_path "$workflow_path"; then
      gh workflow disable "$workflow_id" --repo "$repository"
      printf 'disabled repository workflow %s in %s\n' "$workflow_path" "$repository"
    elif [[ $workflow_state == active ]]; then
      printf 'leaving GitHub-managed workflow entry %s; managed writers are disabled separately\n' "$workflow_path"
    fi
  done <<<"$workflow_listing"
}

dependabot_version_updates_configured() {
  local repository=$1
  gh api "repos/$repository/contents/.github?ref=master" \
    --jq 'any(.[]; .type == "file" and .name == "dependabot.yml")'
}

disable_dependabot_security_updates() {
  local repository=$1
  gh api --method DELETE "repos/$repository/automated-security-fixes" >/dev/null
}

dependabot_security_updates_enabled() {
  local repository=$1
  gh api "repos/$repository/automated-security-fixes" --jq '.enabled'
}

disable_repository_actions() {
  local repository=$1
  gh api --method PUT "repos/$repository/actions/permissions" -F enabled=false >/dev/null
}

repository_actions_enabled() {
  local repository=$1
  gh api "repos/$repository/actions/permissions" --jq '.enabled'
}

active_repository_workflow_count() {
  local repository=$1
  local workflow_listing workflow_id workflow_path workflow_state
  local count=0

  if ! workflow_listing=$(list_legacy_workflows "$repository"); then
    printf 'failed to list workflows in %s\n' "$repository" >&2
    return 1
  fi

  while IFS=$'\t' read -r workflow_id workflow_path workflow_state; do
    [[ -n $workflow_id ]] || continue
    if [[ $workflow_state == active ]] && is_repository_workflow_path "$workflow_path"; then
      count=$((count + 1))
    fi
  done <<<"$workflow_listing"

  printf '%s\n' "$count"
}
