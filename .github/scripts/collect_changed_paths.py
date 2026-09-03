#!/usr/bin/env python3
"""Collect every path side that can affect monorepo CI owner selection."""

from __future__ import annotations

import argparse
import json
import os
from pathlib import Path
import re
import subprocess
import sys


ZERO_SHA = re.compile(r"^0{40}$")
SHA = re.compile(r"^[0-9a-f]{40}$")
EVENTS = {"pull_request", "push", "workflow_dispatch"}


def collect_changed_paths(
    repo: Path,
    event_name: str,
    base_sha: str,
    head_sha: str,
    all_events: set[str] | None = None,
) -> tuple[bool, list[str]]:
    """Return whether all owners are selected and the changed paths otherwise."""
    if event_name not in EVENTS:
        raise ValueError(f"unsupported event kind: {event_name!r}")
    if not SHA.fullmatch(head_sha):
        raise ValueError("head SHA must be a full lowercase commit SHA")
    subprocess.run(
        ["git", "-C", str(repo), "rev-parse", "--verify", f"{head_sha}^{{commit}}"],
        check=True,
        text=True,
        stdout=subprocess.DEVNULL,
        stderr=subprocess.PIPE,
    )
    if event_name in (all_events or set()):
        return True, []

    if not base_sha or ZERO_SHA.fullmatch(base_sha):
        if event_name == "pull_request":
            raise ValueError("pull_request base SHA must be a full commit SHA")
        diff_args = [
            "diff-tree",
            "--root",
            "--no-commit-id",
            "--name-only",
            "--no-renames",
            "-r",
            head_sha,
        ]
    else:
        if not SHA.fullmatch(base_sha):
            raise ValueError("base SHA must be empty, all-zero, or a full lowercase commit SHA")
        subprocess.run(
            ["git", "-C", str(repo), "rev-parse", "--verify", f"{base_sha}^{{commit}}"],
            check=True,
            text=True,
            stdout=subprocess.DEVNULL,
            stderr=subprocess.PIPE,
        )
        revisions = (
            [f"{base_sha}...{head_sha}"]
            if event_name == "pull_request"
            else [base_sha, head_sha]
        )
        # Disabling rename collapsing reports both the deleted source and added
        # destination. Including deletions prevents removal-only changes from
        # silently skipping the path's owner.
        diff_args = [
            "diff",
            "--name-only",
            "--no-renames",
            "--diff-filter=ACMRTD",
            *revisions,
        ]

    completed = subprocess.run(
        ["git", "-C", str(repo), *diff_args],
        check=True,
        text=True,
        stdout=subprocess.PIPE,
    )
    return False, [path for path in completed.stdout.splitlines() if path]


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--event-name", default=os.environ.get("EVENT_NAME", ""))
    parser.add_argument("--base-sha", default=os.environ.get("BASE_SHA", ""))
    parser.add_argument("--head-sha", default=os.environ.get("HEAD_SHA", ""))
    parser.add_argument("--all-event", action="append", default=[])
    parser.add_argument("--repo", type=Path, default=Path.cwd())
    parser.add_argument("--selection-file", type=Path, default=Path("/tmp/changed-path-selection.json"))
    parser.add_argument("--github-output", type=Path)
    args = parser.parse_args()

    if not args.event_name:
        parser.error("--event-name or EVENT_NAME is required")

    select_all, paths = collect_changed_paths(
        repo=args.repo,
        event_name=args.event_name,
        base_sha=args.base_sha,
        head_sha=args.head_sha,
        all_events=set(args.all_event),
    )
    record = {
        "schema_version": 1,
        "event_name": args.event_name,
        "base_sha": args.base_sha,
        "head_sha": args.head_sha,
        "select_all": select_all,
        "paths": paths,
    }
    args.selection_file.write_text(json.dumps(record, sort_keys=True, separators=(",", ":")) + "\n", encoding="utf-8")

    output_path = args.github_output
    if output_path is None and os.environ.get("GITHUB_OUTPUT"):
        output_path = Path(os.environ["GITHUB_OUTPUT"])
    if output_path:
        with output_path.open("a", encoding="utf-8") as output:
            output.write(f"all={str(select_all).lower()}\n")
            output.write(f"selection_file={args.selection_file}\n")

    print("Changed paths:")
    for path in paths:
        print(path)
    return 0


if __name__ == "__main__":
    sys.exit(main())
