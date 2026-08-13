#!/usr/bin/env bash
set -euo pipefail

plan=${1:?usage: check_release_availability.sh PLAN REPOSITORY_OWNER}
repository_owner=${2:?usage: check_release_availability.sh PLAN REPOSITORY_OWNER}
docker_bin=${DOCKER_BIN:-docker}
helm_bin=${HELM_BIN:-helm}

check_absent() {
  local identity=$1
  shift

  local output status
  set +e
  output="$("$@" 2>&1)"
  status=$?
  set -e

  if [ "$status" -eq 0 ]; then
    echo "$identity already exists; bump its source-authoritative version before building a candidate" >&2
    return 1
  fi
  if [ "$status" -eq 1 ] && grep -Eqi '(^|: )not found$|manifest unknown|name unknown' <<<"$output"; then
    return 0
  fi

  echo "could not verify that $identity is available:" >&2
  printf '%s\n' "$output" >&2
  return 1
}

image_rows=$(
  jq -r '
    if (.image_matrix | type) != "array" then
      error("image_matrix must be an array")
    else
      .image_matrix[]
    end
    | if ((.repository | type) != "string" or .repository == ""
        or (.version | type) != "string" or .version == "") then
        error("image_matrix entries require repository and version")
      else
        [.repository, .version] | @tsv
      end
  ' "$plan"
)
chart_rows=$(
  jq -r '
    if (.chart_matrix | type) != "array" then
      error("chart_matrix must be an array")
    else
      .chart_matrix[]
    end
    | if ((.chart | type) != "string" or .chart == ""
        or (.version | type) != "string" or .version == ""
        or (.companions | type) != "array"
        or (all(.companions[]; (type == "string" and length > 0)) | not)) then
        error("chart_matrix entries require chart, version, and companions")
      else
        .version as $version
        | ([.chart] + .companions)[]
        | [$version, .] | @tsv
      end
  ' "$plan"
)

while IFS=$'\t' read -r repository version; do
  [ -n "$repository" ] || continue
  check_absent "$repository:$version" \
    "$docker_bin" buildx imagetools inspect "$repository:$version"
done <<<"$image_rows"

while IFS=$'\t' read -r version chart; do
  [ -n "$chart" ] || continue
  reference="oci://ghcr.io/$repository_owner/iterabase-charts/$chart"
  check_absent "$reference:$version" \
    "$helm_bin" show chart "$reference" --version "$version"
done <<<"$chart_rows"
