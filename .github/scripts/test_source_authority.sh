#!/usr/bin/env bash
set -euo pipefail

script_directory=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=.github/scripts/source_authority_lib.sh
source "$script_directory/source_authority_lib.sh"

temporary_directory=$(mktemp -d)
trap 'rm -rf "$temporary_directory"' EXIT
call_log="$temporary_directory/calls"
authored_workflow_state="$temporary_directory/authored-state"
actions_enabled_state="$temporary_directory/actions-enabled"
printf 'active\n' >"$authored_workflow_state"
printf 'true\n' >"$actions_enabled_state"

gh() {
  printf '%q ' "$@" >>"$call_log"
  printf '\n' >>"$call_log"

  if [[ $1 == api && $2 == --paginate ]]; then
    printf '101\t.github/workflows/ci.yml\t%s\n' "$(<"$authored_workflow_state")"
    printf '102\tdynamic/dependabot/dependabot-updates\tactive\n'
    printf '103\tdynamic/dependabot/update-graph\tactive\n'
    return
  fi
  if [[ $1 == workflow && $2 == disable && $3 == 101 && $4 == --repo ]]; then
    printf 'disabled_manually\n' >"$authored_workflow_state"
    return
  fi
  if [[ $1 == workflow && $2 == disable ]]; then
    printf 'unexpected attempt to disable GitHub-managed workflow %s\n' "$3" >&2
    return 1
  fi
  if [[ $1 == api && $2 == --method && $3 == PUT && $4 == repos/example/legacy/actions/permissions ]]; then
    printf 'false\n' >"$actions_enabled_state"
    return
  fi
  if [[ $1 == api && $2 == repos/example/legacy/actions/permissions ]]; then
    cat "$actions_enabled_state"
    return
  fi

  printf 'unexpected gh invocation: %s\n' "$*" >&2
  return 1
}

disable_repository_workflows example/legacy
disable_repository_actions example/legacy

[[ $(repository_actions_enabled example/legacy) == false ]]
[[ $(active_repository_workflow_count example/legacy) == 0 ]]
grep -q '^workflow disable 101 --repo example/legacy ' "$call_log"
if grep -Eq '^workflow disable (102|103) ' "$call_log"; then
  echo 'GitHub-managed workflow was passed to gh workflow disable' >&2
  exit 1
fi
grep -q '^api --method PUT repos/example/legacy/actions/permissions -F enabled=false ' "$call_log"

echo 'source-authority workflow boundary tests passed'
