#!/usr/bin/env bash
set -euo pipefail

# Historical callers retain this pre-build contract name, but mutable repository
# indexes are no longer trusted. The chart dependency builder downloads only the
# exact archives recorded in the repository content manifest.
repo_root=$(git rev-parse --show-toplevel)
python3 "$repo_root/.github/scripts/remote_content.py" validate
