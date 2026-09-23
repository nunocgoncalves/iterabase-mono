#!/usr/bin/env bash
set -euo pipefail

repository=${GITHUB_REPOSITORY:?GITHUB_REPOSITORY is required}
actor=${GITHUB_ACTOR:?GITHUB_ACTOR is required}
head_repository=${HEAD_REPOSITORY:?HEAD_REPOSITORY is required}
approved_writer=${APPROVED_FIXTURE_WRITER:-nunocgoncalves}

fail() {
  printf 'fixture trust gate failed: %s\n' "$*" >&2
  exit 1
}

[[ "$actor" == "$approved_writer" ]] || fail "actor is not the approved fixture writer"
[[ "$head_repository" == "$repository" ]] || fail "fork or alternate repository source cannot receive fixture authority"

collaborators=$(gh api --paginate "repos/$repository/collaborators?affiliation=all&per_page=100")
writers=$(jq -cs '[.[][] | select(.permissions.admin == true or .permissions.maintain == true or .permissions.push == true) | .login] | unique | sort' <<<"$collaborators")
[[ "$writers" == "[\"$approved_writer\"]" ]] || fail "write-capable collaborator set differs from the founder-only authority"

printf 'fixture trust gate passed for %s at actor %s\n' "$repository" "$actor"
