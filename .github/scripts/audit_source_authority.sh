#!/usr/bin/env bash
set -euo pipefail

script_directory=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=.github/scripts/source_authority_lib.sh
source "$script_directory/source_authority_lib.sh"

state=${1:-pre-archive}
case "$state" in
  pre-archive|transition|archived) ;;
  *)
    echo "usage: $0 [pre-archive|transition|archived]" >&2
    exit 2
    ;;
esac

for command in gh jq; do
  command -v "$command" >/dev/null || {
    echo "source authority audit failed: $command is required" >&2
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
legacy_release_tags=(v0.0.25 v0.2.5 v0.8.1 iterabase-platform-0.3.9)

fail() {
  printf 'source authority audit failed: %s\n' "$*" >&2
  exit 1
}

monorepo_json=$(gh api "repos/$monorepo")
jq -e '
  .archived == false and
  .visibility == "public" and
  .default_branch == "master"
' <<<"$monorepo_json" >/dev/null || fail "$monorepo is not the active public master repository"

rulesets=$(gh api "repos/$monorepo/rulesets" --paginate)
master_ruleset_id=$(jq -r '.[] | select(.name == "master" and .target == "branch" and .enforcement == "active") | .id' <<<"$rulesets")
[[ -n "$master_ruleset_id" ]] || fail "active master ruleset not found"
master_ruleset=$(gh api "repos/$monorepo/rulesets/$master_ruleset_id")
jq -e '
  (.conditions.ref_name.include | index("~DEFAULT_BRANCH")) != null and
  any(.rules[]; .type == "deletion") and
  any(.rules[]; .type == "non_fast_forward") and
  any(.rules[];
    .type == "pull_request" and
    (.parameters.allowed_merge_methods == ["rebase"])) and
  any(.rules[];
    .type == "required_status_checks" and
    .parameters.strict_required_status_checks_policy == true and
    ([.parameters.required_status_checks[].context] | sort) == ["CI / required", "E2E / required"])
' <<<"$master_ruleset" >/dev/null || fail "master ruleset does not enforce the approved PR and required-check contract"

"$(dirname "$0")/audit_release_security.sh" "$monorepo" >/dev/null

for index in "${!legacy_repositories[@]}"; do
  repository=${legacy_repositories[$index]}
  expected_head=${legacy_heads[$index]}
  release_tag=${legacy_release_tags[$index]}
  full_name="$owner/$repository"

  repository_json=$(gh api "repos/$full_name")
  archived=$(jq -r '.archived' <<<"$repository_json")
  case "$state" in
    pre-archive)
      [[ "$archived" == false ]] || fail "$full_name was archived before the cutover"
      ;;
    transition)
      [[ "$archived" == true || "$archived" == false ]] || fail "$full_name returned an invalid archive state"
      ;;
    archived)
      [[ "$archived" == true ]] || fail "$full_name is not archived"
      ;;
  esac

  jq -e '.visibility == "public" and .default_branch == "master"' <<<"$repository_json" >/dev/null \
    || fail "$full_name is not a public master repository"

  actual_head=$(gh api "repos/$full_name/branches/master" --jq '.commit.sha')
  [[ "$actual_head" == "$expected_head" ]] \
    || fail "$full_name master is $actual_head, expected imported head $expected_head"

  protected=$(gh api "repos/$full_name/branches/master" --jq '.protected')
  [[ "$protected" == true || "$archived" == true ]] \
    || fail "$full_name master is writable before archive"

  open_pull_count=$(gh api "repos/$full_name/pulls?state=open&per_page=1" --jq 'length')
  [[ "$open_pull_count" == 0 ]] || fail "$full_name has an open pull request"

  gh api "repos/$monorepo/git/commits/$expected_head" >/dev/null
  comparison=$(gh api "repos/$monorepo/compare/$expected_head...master" --jq '.status')
  [[ "$comparison" == ahead || "$comparison" == identical ]] \
    || fail "$expected_head is not contained in $monorepo master"

  gh api "repos/$full_name/commits/$expected_head" >/dev/null
  gh api "repos/$full_name/pulls/1" >/dev/null
  # GraphQL variable names are literal; gh supplies their values via -F.
  # shellcheck disable=SC2016
  issue_count=$(gh api graphql \
    -f query='query($owner:String!,$name:String!){repository(owner:$owner,name:$name){issues{totalCount}}}' \
    -F owner="$owner" -F name="$repository" --jq '.data.repository.issues.totalCount')
  [[ "$issue_count" == 0 ]] || fail "$full_name issue history changed from the recorded zero-issue cutover state"
  gh api "repos/$full_name/releases/tags/$release_tag" >/dev/null
  gh api "repos/$full_name/git/ref/tags/$release_tag" >/dev/null

  if [[ "$archived" == true ]]; then
    jq -e --arg pointer "$pointer" '
      (.description | contains($pointer)) and .homepage == $pointer
    ' <<<"$repository_json" >/dev/null || fail "$full_name has no canonical monorepo pointer"

    actions_enabled=$(repository_actions_enabled "$full_name")
    [[ "$actions_enabled" == false ]] || fail "$full_name still has repository Actions enabled"

    active_workflows=$(active_repository_workflow_count "$full_name")
    [[ "$active_workflows" == 0 ]] \
      || fail "$full_name still has $active_workflows active repository-authored workflows"

    secret_count=$(gh api "repos/$full_name/actions/secrets?per_page=1" --jq '.total_count')
    [[ "$secret_count" == 0 ]] || fail "$full_name still has $secret_count Actions secrets"
  fi

done

if [[ ${CHECK_ARTIFACTS:-false} == true ]]; then
  for command in docker helm; do
    command -v "$command" >/dev/null || fail "$command is required when CHECK_ARTIFACTS=true"
  done
  docker buildx imagetools inspect ghcr.io/nunocgoncalves/control-plane:0.0.25 >/dev/null
  docker buildx imagetools inspect ghcr.io/nunocgoncalves/control-plane-harness:0.0.25 >/dev/null
  docker buildx imagetools inspect ghcr.io/nunocgoncalves/control-plane-tool-runner:0.0.25 >/dev/null
  docker buildx imagetools inspect ghcr.io/nunocgoncalves/inference-gateway:0.2.5 >/dev/null
  artifact_directory=$(mktemp -d)
  trap 'rm -rf "$artifact_directory"' EXIT
  helm pull oci://ghcr.io/nunocgoncalves/iterabase-charts/iterabase-platform \
    --version 0.3.9 --destination "$artifact_directory" >/dev/null
  [[ -s "$artifact_directory/iterabase-platform-0.3.9.tgz" ]] \
    || fail "historical platform chart is unavailable"
fi

printf 'source authority audit passed for %s (%s)\n' "$monorepo" "$state"
printf 'required checks: CI / required, E2E / required\n'
printf 'legacy heads, ancestry, PRs, tags, and releases remain accessible\n'
if [[ "$state" == archived ]]; then
  printf 'legacy repository Actions and authored workflows are disabled; Actions secrets are absent\n'
fi
if [[ ${CHECK_ARTIFACTS:-false} == true ]]; then
  printf 'historical GHCR images and chart artifact remain accessible\n'
fi
