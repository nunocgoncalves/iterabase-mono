#!/usr/bin/env python3
"""Select and require the complete compiled nightly E2E catalogue."""

from __future__ import annotations

import argparse
from collections import Counter
import json
import os
from pathlib import Path
import re
import subprocess
import sys
from typing import Any

RUNNABLE_TIERS = ("F2", "F3")
SOURCE_MODE = "source"
PLAN_SCHEMA_VERSION = 1
JOB_CLEANUP_GRACE_MINUTES = 5
SHA = re.compile(r"^[0-9a-f]{40}$")
SCHEDULE_REQUIRED_JOBS = (
    "changes",
    "harness",
    "nightly-kind",
    "nightly-real-machine",
)


class NightlyError(ValueError):
    """The compiled catalogue or scheduled aggregate is incomplete."""


def compact(value: Any) -> str:
    return json.dumps(value, sort_keys=True, separators=(",", ":"))


def load_scenario_catalogue(root: Path) -> dict[str, Any]:
    command = [
        "go",
        "run",
        "./testkit/e2e/cmd/e2e-catalogue",
        "--root",
        str(root),
        "--format",
        "json",
    ]
    try:
        completed = subprocess.run(
            command,
            cwd=root,
            check=True,
            capture_output=True,
            text=True,
            timeout=600,
        )
        catalogue = json.loads(completed.stdout)
    except (OSError, subprocess.SubprocessError, json.JSONDecodeError) as exc:
        stderr = getattr(exc, "stderr", "")
        raise NightlyError(
            f"cannot compile E2E scenario catalogue: {exc}\n{stderr}"
        ) from exc
    if not isinstance(catalogue, dict):
        raise NightlyError("compiled E2E scenario catalogue must be an object")
    return catalogue


def catalogue_scenarios(catalogue: dict[str, Any]) -> list[dict[str, Any]]:
    if catalogue.get("schema_version") != 1:
        raise NightlyError("compiled E2E scenario catalogue must use schema_version 1")
    suites = catalogue.get("suites")
    if not isinstance(suites, list):
        raise NightlyError("compiled E2E catalogue suites must be a list")

    scenarios: list[dict[str, Any]] = []
    seen_ids: set[str] = set()
    for suite_value in suites:
        if not isinstance(suite_value, dict):
            raise NightlyError("compiled E2E catalogue has an invalid suite")
        suite = suite_value.get("suite")
        suite_scenarios = suite_value.get("scenarios")
        if not isinstance(suite, dict) or not isinstance(suite_scenarios, list):
            raise NightlyError("compiled E2E catalogue has an invalid suite")
        for field in ("name", "owner", "entrypoint"):
            if not isinstance(suite.get(field), str) or not suite[field]:
                raise NightlyError(f"compiled E2E suite has no {field}")
        for scenario in suite_scenarios:
            if not isinstance(scenario, dict) or not isinstance(
                scenario.get("metadata"), dict
            ):
                raise NightlyError("compiled E2E catalogue has an invalid scenario")
            scenario_id = scenario.get("id")
            if not isinstance(scenario_id, str) or not scenario_id:
                raise NightlyError("compiled E2E scenario has no id")
            if scenario_id in seen_ids:
                raise NightlyError(f"compiled E2E catalogue repeats scenario {scenario_id!r}")
            seen_ids.add(scenario_id)
            scenarios.append({**scenario, "suite": suite})
    return sorted(scenarios, key=lambda item: item["id"])


def scenario_matrix_entry(scenario: dict[str, Any]) -> dict[str, Any]:
    metadata = scenario["metadata"]
    scenario_id = scenario["id"]
    tier = metadata.get("tier")
    fixture_modes = metadata.get("fixture_modes")
    make_target = metadata.get("make_target")
    timeout = metadata.get("timeout_minutes")

    if not isinstance(fixture_modes, list) or SOURCE_MODE not in fixture_modes:
        raise NightlyError(
            f"required nightly scenario {scenario_id!r} does not support source fixtures"
        )
    if not isinstance(make_target, str) or not make_target:
        raise NightlyError(
            f"required nightly scenario {scenario_id!r} has no make target"
        )
    if not isinstance(timeout, int) or isinstance(timeout, bool) or timeout <= 0:
        raise NightlyError(
            f"required nightly scenario {scenario_id!r} has no positive timeout"
        )

    entry = {
        "id": scenario_id,
        "owner": scenario["suite"]["owner"],
        "entrypoint": scenario["suite"]["entrypoint"],
        "name": metadata.get("name"),
        "target": make_target,
        "tier": tier,
        "fixture_mode": SOURCE_MODE,
        "scenario_timeout": timeout,
        # Give owner diagnostics and cleanup a bounded grace period before the
        # GitHub job timeout terminates the runner.
        "timeout": timeout + JOB_CLEANUP_GRACE_MINUTES,
        "artifact": scenario_id.replace("/", "-"),
    }
    if tier == "F3":
        capacity = metadata.get("capacity")
        if not isinstance(capacity, str) or not capacity:
            raise NightlyError(
                f"required real-machine scenario {scenario_id!r} has no capacity"
            )
        if metadata.get("mandatory_capacity") is not True:
            raise NightlyError(
                f"required real-machine scenario {scenario_id!r} is not mandatory"
            )
        entry.update({"capacity": capacity, "mandatory": True})
    return entry


def make_plan(catalogue: dict[str, Any], source_sha: str) -> dict[str, Any]:
    if not SHA.fullmatch(source_sha):
        raise NightlyError("nightly source SHA must be a full lowercase commit SHA")

    matrices: dict[str, list[dict[str, Any]]] = {"F2": [], "F3": []}
    selected_ids: list[str] = []
    owner_totals: Counter[str] = Counter()
    for scenario in catalogue_scenarios(catalogue):
        tier = scenario["metadata"].get("tier")
        if tier not in RUNNABLE_TIERS:
            continue
        entry = scenario_matrix_entry(scenario)
        matrices[tier].append(entry)
        selected_ids.append(entry["id"])
        owner_totals[entry["owner"]] += 1

    if not matrices["F2"]:
        raise NightlyError("compiled E2E catalogue has no required Kind/browser scenarios")
    if not matrices["F3"]:
        raise NightlyError("compiled E2E catalogue has no required real-machine scenarios")
    if len(selected_ids) != len(set(selected_ids)):
        raise NightlyError("required nightly selection contains a duplicate scenario")

    return {
        "schema_version": PLAN_SCHEMA_VERSION,
        "source_sha": source_sha,
        "fixture_mode": SOURCE_MODE,
        "required_tiers": list(RUNNABLE_TIERS),
        "scenario_total": len(selected_ids),
        "owner_totals": dict(sorted(owner_totals.items())),
        "selected_scenario_ids": selected_ids,
        "kind_matrix": matrices["F2"],
        "real_machine_matrix": matrices["F3"],
    }


def validate_results(
    event_name: str, needs: dict[str, Any], complete_catalogue: bool = False
) -> None:
    if not isinstance(needs, dict):
        raise NightlyError("E2E aggregate needs must be an object")
    results: dict[str, str] = {}
    for name, job in needs.items():
        if not isinstance(job, dict) or not isinstance(job.get("result"), str):
            raise NightlyError(f"E2E aggregate job {name!r} has no result")
        results[name] = job["result"]

    failures = {
        name: result
        for name, result in results.items()
        if result not in {"success", "skipped"}
    }
    if event_name == "schedule" or complete_catalogue:
        for name in SCHEDULE_REQUIRED_JOBS:
            result = results.get(name, "missing")
            if result != "success":
                failures[name] = result
    if failures:
        raise NightlyError(
            "required E2E owners incomplete: " + compact(dict(sorted(failures.items())))
        )


def write_github_outputs(path: Path, plan: dict[str, Any]) -> None:
    outputs = {
        "nightly_kind_matrix": plan["kind_matrix"],
        "nightly_real_machine_matrix": plan["real_machine_matrix"],
        "nightly_owner_totals": plan["owner_totals"],
        "nightly_scenario_total": plan["scenario_total"],
    }
    with path.open("a", encoding="utf-8") as output:
        for name, value in outputs.items():
            output.write(f"{name}={compact(value)}\n")


def command_plan(args: argparse.Namespace) -> None:
    root = Path(args.root).resolve()
    catalogue = load_scenario_catalogue(root)
    plan = make_plan(catalogue, args.source_sha)
    output = Path(args.output)
    output.write_text(json.dumps(plan, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    if args.github_output:
        write_github_outputs(Path(args.github_output), plan)
    print(
        "nightly E2E selection:",
        compact(
            {
                "scenario_total": plan["scenario_total"],
                "owner_totals": plan["owner_totals"],
            }
        ),
    )


def command_validate_results(args: argparse.Namespace) -> None:
    raw = os.environ.get(args.needs_env, "")
    try:
        needs = json.loads(raw)
    except json.JSONDecodeError as exc:
        raise NightlyError(
            f"{args.needs_env} must contain the E2E needs object: {exc}"
        ) from exc
    validate_results(
        args.event_name, needs, complete_catalogue=args.complete_catalogue == "true"
    )
    print("required E2E results:", compact(needs))


def parser() -> argparse.ArgumentParser:
    value = argparse.ArgumentParser(description=__doc__)
    commands = value.add_subparsers(dest="command", required=True)

    plan = commands.add_parser("plan", help="compile and select the nightly catalogue")
    plan.add_argument("--root", default=".")
    plan.add_argument("--source-sha", required=True)
    plan.add_argument("--output", required=True)
    plan.add_argument("--github-output")
    plan.set_defaults(handler=command_plan)

    validate = commands.add_parser(
        "validate-results", help="require the event-specific E2E aggregate"
    )
    validate.add_argument("--event-name", required=True)
    validate.add_argument(
        "--complete-catalogue", choices=("true", "false"), default="false"
    )
    validate.add_argument("--needs-env", default="NEEDS")
    validate.set_defaults(handler=command_validate_results)
    return value


def main() -> None:
    args = parser().parse_args()
    try:
        args.handler(args)
    except NightlyError as exc:
        print(f"nightly E2E: {exc}", file=sys.stderr)
        raise SystemExit(1) from exc


if __name__ == "__main__":
    main()
