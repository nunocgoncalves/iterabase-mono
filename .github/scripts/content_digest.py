#!/usr/bin/env python3
"""Return a deterministic SHA-256 for named immutable cache inputs."""

from __future__ import annotations

import argparse
import hashlib
from pathlib import Path


def content_digest(paths: list[Path]) -> str:
    digest = hashlib.sha256()
    for path in sorted(paths, key=lambda item: item.as_posix()):
        if not path.is_file():
            raise FileNotFoundError(path)
        digest.update(path.as_posix().encode())
        digest.update(b"\0")
        digest.update(path.read_bytes())
        digest.update(b"\0")
    return digest.hexdigest()


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("paths", nargs="+", type=Path)
    args = parser.parse_args()
    print(content_digest(args.paths))


if __name__ == "__main__":
    main()
