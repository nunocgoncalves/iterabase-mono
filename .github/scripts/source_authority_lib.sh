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
  local workflow_id workflow_path workflow_state

  while IFS=$'\t' read -r workflow_id workflow_path workflow_state; do
    [[ -n $workflow_id ]] || continue
    if [[ $workflow_state == active ]] && is_repository_workflow_path "$workflow_path"; then
      gh workflow disable "$workflow_id" --repo "$repository"
      printf 'disabled repository workflow %s in %s\n' "$workflow_path" "$repository"
    elif [[ $workflow_state == active ]]; then
      printf 'leaving GitHub-managed workflow entry %s; repository Actions will be disabled\n' "$workflow_path"
    fi
  done < <(list_legacy_workflows "$repository")
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
  local workflow_id workflow_path workflow_state
  local count=0

  while IFS=$'\t' read -r workflow_id workflow_path workflow_state; do
    [[ -n $workflow_id ]] || continue
    if [[ $workflow_state == active ]] && is_repository_workflow_path "$workflow_path"; then
      count=$((count + 1))
    fi
  done < <(list_legacy_workflows "$repository")

  printf '%s\n' "$count"
}
