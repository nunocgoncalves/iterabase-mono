#!/usr/bin/env bash
set -euo pipefail

readonly target="${1:-HEAD}"
repository_root="$(git rev-parse --show-toplevel)"
readonly repository_root
readonly manifest="${repository_root}/docs/history-import.tsv"

if [[ ! -f "${manifest}" ]]; then
  echo "error: missing ${manifest}" >&2
  exit 1
fi

source_heads=()
expected_top_level=(README.md charts control-plane docs forge inference-gateway scripts)

while IFS=$'\t' read -r component repository branch source_head relocation_sha merge_sha sample_sha follow_sha blame_sha representative_file; do
  if [[ "${component}" == "component" || -z "${component}" ]]; then
    continue
  fi

  source_heads+=("${source_head}")

  git cat-file -e "${source_head}^{commit}"
  git cat-file -e "${sample_sha}^{commit}"
  git merge-base --is-ancestor "${source_head}" "${target}"
  git merge-base --is-ancestor "${sample_sha}" "${source_head}"

  if [[ "$(git rev-parse "${relocation_sha}^")" != "${source_head}" ]]; then
    echo "error: ${component} relocation is not directly based on its pinned source head" >&2
    exit 1
  fi

  if [[ "$(git rev-list --parents -n 1 "${merge_sha}" | wc -w | tr -d ' ')" != "3" ]]; then
    echo "error: ${component} import is not a two-parent merge" >&2
    exit 1
  fi

  if [[ "$(git rev-parse "${merge_sha}^2")" != "${relocation_sha}" ]]; then
    echo "error: ${component} relocation is not the import merge's second parent" >&2
    exit 1
  fi

  source_tree="$(git rev-parse "${source_head}^{tree}")"
  relocated_tree="$(git rev-parse "${relocation_sha}:${component}")"
  integrated_tree="$(git rev-parse "${target}:${component}")"
  if [[ "${source_tree}" != "${relocated_tree}" ]] || [[ "${source_tree}" != "${integrated_tree}" ]]; then
    echo "error: ${component} tree differs from ${repository}/${branch} at ${source_head}" >&2
    exit 1
  fi

  if ! git log --follow --format='%H' "${target}" -- "${component}/${representative_file}" \
      | grep -Fqx "${follow_sha}"; then
    echo "error: git log --follow did not cross the ${component} relocation" >&2
    exit 1
  fi

  if ! git blame --line-porcelain "${target}" -- "${component}/${representative_file}" \
      | grep -Eq "^${blame_sha} "; then
    echo "error: blame did not retain pre-relocation attribution for ${component}" >&2
    exit 1
  fi

  printf 'verified %-18s head=%s tree=%s sample=%s\n' \
    "${component}" "${source_head}" "${source_tree}" "${sample_sha}"
done < "${manifest}"

actual_top_level="$(git ls-tree --name-only "${target}" | sort)"
sorted_expected_top_level="$(printf '%s\n' "${expected_top_level[@]}" | sort)"
if [[ "${actual_top_level}" != "${sorted_expected_top_level}" ]]; then
  echo "error: unexpected top-level content; overlays and marketing must remain excluded" >&2
  printf 'expected:\n%s\nactual:\n%s\n' \
    "${sorted_expected_top_level}" "${actual_top_level}" >&2
  exit 1
fi

while IFS= read -r integration_commit; do
  subject="$(git show -s --format='%s' "${integration_commit}")"
  if [[ "${subject}" != HOR-472:* ]]; then
    echo "error: unexpected non-source history ${integration_commit}: ${subject}" >&2
    exit 1
  fi
done < <(git rev-list "${target}" --not "${source_heads[@]}")

if [[ -n "$(git tag --list 'v*')" ]]; then
  echo "error: conflicting raw v* tag refs were imported" >&2
  exit 1
fi

git fsck --full --no-dangling
printf 'verified exclusions, integration-only commits, raw-tag policy, and object graph for %s\n' "${target}"
