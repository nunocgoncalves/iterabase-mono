#!/usr/bin/env bash
set -euo pipefail

repo_root=$(git rev-parse --show-toplevel)
python3 "$repo_root/.github/scripts/test_remote_content.py"
