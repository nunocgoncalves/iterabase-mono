#!/usr/bin/env bash
set -euo pipefail

repository="${1:-nunocgoncalves/iterabase-mono}"
expected_reviewer="${RELEASE_REVIEWER:-nunocgoncalves}"

fail() {
  printf 'release security audit failed: %s\n' "$*" >&2
  exit 1
}

keys="$(gh api "repos/$repository/keys")"
write_key_count="$(jq '[.[] | select(.read_only == false)] | length' <<<"$keys")"
[[ "$write_key_count" == 1 ]] || fail "expected exactly one write deploy key, found $write_key_count"
write_key_title="$(jq -r '.[] | select(.read_only == false) | .title' <<<"$keys")"
[[ "$write_key_title" == 'iterabase protected release tags (validated)' ]] || fail "unexpected write deploy key: $write_key_title"

environment="$(gh api "repos/$repository/environments/release")"
jq -e --arg reviewer "$expected_reviewer" '
  .name == "release" and
  .deployment_branch_policy.protected_branches == false and
  .deployment_branch_policy.custom_branch_policies == true and
  any(.protection_rules[];
    .type == "required_reviewers" and
    .prevent_self_review == false and
    any(.reviewers[]; .type == "User" and .reviewer.login == $reviewer))
' <<<"$environment" >/dev/null || fail "release environment protection does not match the approved contract"

branches="$(gh api "repos/$repository/environments/release/deployment-branch-policies")"
jq -e '.branch_policies | length == 1 and .[0].name == "master" and .[0].type == "branch"' \
  <<<"$branches" >/dev/null || fail "release environment must allow only the master branch"

secrets="$(gh api "repos/$repository/environments/release/secrets")"
jq -e 'any(.secrets[]; .name == "RELEASE_TAG_SSH_KEY")' <<<"$secrets" >/dev/null || \
  fail "release environment is missing RELEASE_TAG_SSH_KEY"

rulesets="$(gh api "repos/$repository/rulesets" --paginate)"
ruleset_id="$(jq -r '.[] | select(.name == "protected release tags" and .target == "tag" and .enforcement == "active") | .id' <<<"$rulesets")"
[[ -n "$ruleset_id" ]] || fail "active protected release tags ruleset not found"
ruleset="$(gh api "repos/$repository/rulesets/$ruleset_id")"
jq -e '
  ([.bypass_actors[] | select(.actor_type == "DeployKey" and .bypass_mode == "always")] | length) == 1 and
  ([.rules[].type] | sort) == (["creation", "deletion", "non_fast_forward", "update"] | sort) and
  ([.conditions.ref_name.include[]] | sort) == ([
    "refs/tags/control-plane-v*",
    "refs/tags/inference-gateway-v*",
    "refs/tags/forge-v*",
    "refs/tags/control-plane-*",
    "refs/tags/inference-gateway-*",
    "refs/tags/iterabase-platform-*",
    "refs/tags/dry-run/**"
  ] | sort)
' <<<"$ruleset" >/dev/null || fail "release tag ruleset does not match the approved contract"

permissions="$(gh api "repos/$repository/actions/permissions/workflow")"
jq -e '.default_workflow_permissions == "read" and .can_approve_pull_request_reviews == false' \
  <<<"$permissions" >/dev/null || fail "default workflow token permissions are not read-only"

printf 'release security audit passed for %s\n' "$repository"
printf 'write deploy key: %s\n' "$write_key_title"
printf 'release ruleset id: %s\n' "$ruleset_id"
