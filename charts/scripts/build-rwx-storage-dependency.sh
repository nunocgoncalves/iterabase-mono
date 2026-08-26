#!/usr/bin/env bash
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
source_archive="$root/vendor/longhorn-1.12.1.tgz"
destination_dir="$root/charts/rwx-storage-substrate/charts"
destination_archive="$destination_dir/longhorn-1.12.1.tgz"
expected_sha256=d70764e2d6cce673482da4d91da5b44a9791cda842c1914f77e7806ad1cd94bb

[[ -f "$source_archive" ]] || {
  echo "missing repository-owned Longhorn dependency: $source_archive" >&2
  exit 1
}
observed_sha256="$(python3 - "$source_archive" <<'PY'
import hashlib
import pathlib
import sys

path = pathlib.Path(sys.argv[1])
digest = hashlib.sha256()
with path.open("rb") as stream:
    for block in iter(lambda: stream.read(1024 * 1024), b""):
        digest.update(block)
print(digest.hexdigest())
PY
)"
[[ "$observed_sha256" == "$expected_sha256" ]] || {
  echo "Longhorn dependency digest mismatch: observed $observed_sha256 required $expected_sha256" >&2
  exit 1
}
metadata="$(helm show chart "$source_archive")"
grep -Eq '^name: longhorn$' <<<"$metadata"
grep -Eq '^version: 1\.12\.1$' <<<"$metadata"
grep -Eq '^appVersion: v1\.12\.1$' <<<"$metadata"

mkdir -p "$destination_dir"
cp "$source_archive" "$destination_archive"
chmod 0644 "$destination_archive"
echo "Longhorn dependency ready: version=1.12.1 sha256=$observed_sha256"
