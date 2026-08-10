#!/usr/bin/env bash
set -euo pipefail

# Restore the standard repository policy after the merge-preserving HOR-472 PR lands.

readonly repository="${GITHUB_REPOSITORY:-nunocgoncalves/iterabase-mono}"
ruleset_id="$(
  gh api "repos/${repository}/rulesets" \
    --jq '.[] | select(.name == "master" and .target == "branch") | .id'
)"
readonly ruleset_id

if [[ -z "${ruleset_id}" || "${ruleset_id}" == *$'\n'* ]]; then
  echo "error: expected exactly one master branch ruleset" >&2
  exit 1
fi

gh api --method PATCH "repos/${repository}" \
  -F allow_merge_commit=false \
  -F allow_rebase_merge=true \
  -F allow_squash_merge=false >/dev/null

payload="$(mktemp)"
trap 'rm -f "${payload}"' EXIT
cat >"${payload}" <<'JSON'
{
  "name": "master",
  "target": "branch",
  "enforcement": "active",
  "conditions": {
    "ref_name": {
      "include": ["~DEFAULT_BRANCH"],
      "exclude": []
    }
  },
  "rules": [
    {"type": "deletion"},
    {"type": "non_fast_forward"},
    {
      "type": "pull_request",
      "parameters": {
        "allowed_merge_methods": ["rebase"],
        "dismiss_stale_reviews_on_push": false,
        "require_code_owner_review": false,
        "require_last_push_approval": false,
        "required_approving_review_count": 0,
        "required_review_thread_resolution": false,
        "required_reviewers": []
      }
    }
  ],
  "bypass_actors": []
}
JSON

gh api --method PUT "repos/${repository}/rulesets/${ruleset_id}" --input "${payload}" >/dev/null
printf 'restored rebase-only pull-request protection for %s\n' "${repository}"
