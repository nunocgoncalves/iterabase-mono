#!/usr/bin/env bash
set -euo pipefail

# Reproduce the HOR-472 history import from the pinned public source heads.
# Run this from the clean ticket branch of an initialized iterabase-mono clone.

readonly source_owner="${SOURCE_OWNER:-nunocgoncalves}"
integration_branch="$(git branch --show-current)"
readonly integration_branch
readonly relocation_staging_dir=".hor-472-relocation"

if [[ -z "${integration_branch}" ]]; then
  echo "error: run from a named integration branch" >&2
  exit 1
fi

if [[ -n "$(git status --porcelain)" ]]; then
  echo "error: the worktree must be clean before importing histories" >&2
  exit 1
fi

components=(control-plane inference-gateway forge charts)
repositories=(control-plane inference-gateway forge iterabase-charts)
source_heads=(
  c63eea9d21c367a3e5fd91431bedc853fb15a16b
  cf093df2cdca30e916cb340d3e5dc1ab29c49989
  56afae7b21f97a1c40c81705954756ef16f46674
  0d97d50962afcd03aa474f096a8948f0e1dcd8b5
)

for index in "${!components[@]}"; do
  component="${components[$index]}"
  repository="${repositories[$index]}"
  expected_head="${source_heads[$index]}"
  remote="import-${repository}"
  relocation_branch="import/HOR-472-${component}"
  remote_url="https://github.com/${source_owner}/${repository}.git"

  if git show-ref --verify --quiet "refs/heads/${relocation_branch}"; then
    echo "error: relocation branch already exists: ${relocation_branch}" >&2
    exit 1
  fi

  if git remote get-url "${remote}" >/dev/null 2>&1; then
    git remote set-url "${remote}" "${remote_url}"
  else
    git remote add "${remote}" "${remote_url}"
  fi

  git fetch --no-tags "${remote}" master
  actual_head="$(git rev-parse FETCH_HEAD)"
  if [[ "${actual_head}" != "${expected_head}" ]]; then
    echo "error: ${repository}/master moved: expected ${expected_head}, got ${actual_head}" >&2
    exit 1
  fi

  git switch --create "${relocation_branch}" "${actual_head}"
  mkdir "${relocation_staging_dir}"
  while IFS= read -r -d '' entry; do
    git mv -- "${entry}" "${relocation_staging_dir}/"
  done < <(git ls-tree -z --name-only HEAD)
  mv "${relocation_staging_dir}" "${component}"
  git add -A
  git commit -m "HOR-472: relocate ${repository} under ${component}/"
  relocation_sha="$(git rev-parse HEAD)"

  git switch "${integration_branch}"
  git merge --no-ff --allow-unrelated-histories "${relocation_branch}" \
    -m "HOR-472: merge relocated ${repository} history"
  merge_sha="$(git rev-parse HEAD)"

  printf '%s\t%s\t%s\t%s\n' \
    "${repository}" "${expected_head}" "${relocation_sha}" "${merge_sha}"
done
