#!/usr/bin/env bash
set -euo pipefail

chart=${1:?usage: build-chart-dependency.sh CHART_DIR}
repo_root=$(git rev-parse --show-toplevel)
python3 "$repo_root/.github/scripts/remote_content.py" prepare-chart --chart "$chart"
