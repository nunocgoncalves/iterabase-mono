#!/usr/bin/env python3
"""Plan, record, and verify single-target build-once releases."""

from __future__ import annotations

import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import sys
from typing import Any

SEMVER = re.compile(r"^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$")
SHA = re.compile(r"^[0-9a-f]{40}$")
TARGET_NAMES = (
    "control-plane",
    "inference-gateway",
    "forge",
    "control-plane-chart",
    "inference-gateway-chart",
    "iterabase-platform-chart",
)
KIND_TARGETS = {
    "controlplane-identity": ("test-e2e-controlplane", 20),
    "inference-contract": ("test-e2e-inference", 20),
    "cert-issuers": ("test-e2e-cert-issuers", 20),
    "internal-tls": ("test-e2e-internal-tls", 25),
    "tool-runner-contract": ("test-e2e-tool-runner", 35),
}


class ReleaseError(ValueError):
    """The repository release contract or candidate is invalid."""


def compact(value: Any) -> str:
    return json.dumps(value, sort_keys=True, separators=(",", ":"))


def load_json(path: Path) -> dict[str, Any]:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        raise ReleaseError(f"cannot read {path}: {exc}") from exc
    if not isinstance(value, dict):
        raise ReleaseError(f"{path} must contain a JSON object")
    return value


def require_semver(value: Any, label: str) -> str:
    if not isinstance(value, str) or not SEMVER.fullmatch(value):
        raise ReleaseError(f"{label} must be stable SemVer without a v prefix: {value!r}")
    return value


def read_version(path: Path) -> str:
    try:
        value = path.read_text(encoding="utf-8").strip()
    except OSError as exc:
        raise ReleaseError(f"cannot read version authority {path}: {exc}") from exc
    return require_semver(value, str(path))


def chart_metadata(path: Path) -> dict[str, str]:
    result: dict[str, str] = {}
    try:
        lines = path.read_text(encoding="utf-8").splitlines()
    except OSError as exc:
        raise ReleaseError(f"cannot read {path}: {exc}") from exc
    for raw in lines:
        match = re.match(r"^(name|version|appVersion):\s*[\"']?([^\"'#\s]+)", raw)
        if match:
            result[match.group(1)] = match.group(2)
    missing = {"name", "version", "appVersion"} - result.keys()
    if missing:
        raise ReleaseError(f"{path} is missing {', '.join(sorted(missing))}")
    require_semver(result["version"], f"{path} version")
    # Helm appVersion is an opaque application identity and may legitimately
    # include a v prefix (for example cert-manager v1.21.0).
    return result


def chart_dependencies(path: Path) -> list[dict[str, str]]:
    dependencies: list[dict[str, str]] = []
    current: dict[str, str] = {}
    in_dependencies = False
    for raw in path.read_text(encoding="utf-8").splitlines():
        if raw == "dependencies:":
            in_dependencies = True
            continue
        if not in_dependencies:
            continue
        name = re.match(r"^\s*-\s+name:\s*([^\s#]+)", raw)
        if name:
            if current.get("name") and current.get("version"):
                dependencies.append(current)
            current = {"name": name.group(1)}
            continue
        version = re.match(r"^\s+version:\s*[\"']?([^\"'#\s]+)", raw)
        if version and current:
            current["version"] = version.group(1)
    if current.get("name") and current.get("version"):
        dependencies.append(current)
    return dependencies


def fixture_versions(root: Path) -> dict[str, str]:
    source = (root / "forge" / "test" / "e2e" / "chart_fixture_test.go").read_text(
        encoding="utf-8"
    )
    constants = {
        "platform_chart": "pinnedPlatformChartVersion",
        "control_plane_chart": "pinnedControlPlaneChartVersion",
        "certificate_migration_source": "certificateMigrationSourceVersion",
    }
    result: dict[str, str] = {}
    for name, constant in constants.items():
        match = re.search(rf'{constant}\s*=\s*"([^"]+)"', source)
        if match is None:
            raise ReleaseError(f"cannot resolve test fixture {constant}")
        result[name] = require_semver(match.group(1), constant)
    return result


def validate_contract(targets: dict[str, Any], root: Path) -> None:
    if targets.get("schema_version") != 2:
        raise ReleaseError("release targets schema_version must be 2")
    if targets.get("suite_mapping_until") != "HOR-476":
        raise ReleaseError("temporary suite mapping must name HOR-476")
    definitions = targets.get("targets")
    if not isinstance(definitions, dict) or tuple(definitions) != TARGET_NAMES:
        raise ReleaseError("release target names or order do not match the workflow contract")

    for target, definition in definitions.items():
        if not isinstance(definition, dict):
            raise ReleaseError(f"target {target} must be an object")
        if not isinstance(definition.get("tag_prefix"), str):
            raise ReleaseError(f"target {target} is missing tag_prefix")
        suites = definition.get("source_suites")
        if not isinstance(suites, list) or any(
            item not in {"control-plane", "inference-gateway", "forge", "charts"}
            for item in suites
        ):
            raise ReleaseError(f"target {target} has an unknown source suite")
        scenarios = definition.get("kind_scenarios")
        if not isinstance(scenarios, list) or any(item not in KIND_TARGETS for item in scenarios):
            raise ReleaseError(f"target {target} has an unknown Kind scenario")
        if not isinstance(definition.get("real_machine"), bool):
            raise ReleaseError(f"target {target} must declare real_machine")
        images = definition.get("images")
        if not isinstance(images, list):
            raise ReleaseError(f"target {target} must declare images")

        if "version_file" in definition:
            read_version(root / definition["version_file"])
        elif "chart" in definition:
            chart = definition["chart"]
            metadata = chart_metadata(root / "charts" / "charts" / chart / "Chart.yaml")
            if metadata["name"] != chart:
                raise ReleaseError(f"chart target {target} points at {metadata['name']}")
            for companion in definition.get("companions", []):
                companion_metadata = chart_metadata(
                    root / "charts" / "charts" / companion / "Chart.yaml"
                )
                if companion_metadata["version"] != metadata["version"]:
                    raise ReleaseError(f"companion {companion} must match {chart} version")
        else:
            raise ReleaseError(f"target {target} has no version authority")

    fixture_versions(root)


def repository_versions(root: Path, targets: dict[str, Any]) -> dict[str, str]:
    versions: dict[str, str] = {}
    for target, definition in targets["targets"].items():
        if "version_file" in definition:
            versions[target] = read_version(root / definition["version_file"])
        else:
            versions[target] = chart_metadata(
                root / "charts" / "charts" / definition["chart"] / "Chart.yaml"
            )["version"]
    return versions


def make_plan(
    targets: dict[str, Any], target: str, master_sha: str, run_id: str, root: Path
) -> dict[str, Any]:
    if target not in targets.get("targets", {}):
        raise ReleaseError(f"unknown release target {target!r}")
    if not SHA.fullmatch(master_sha):
        raise ReleaseError("master_sha must be a full lowercase commit SHA")
    if not run_id.isdigit():
        raise ReleaseError("run_id must be numeric")

    definition = targets["targets"][target]
    versions = repository_versions(root, targets)
    version = versions[target]
    images = [
        {
            **image,
            "target": target,
            "version": version,
            "candidate_tag": master_sha,
        }
        for image in definition["images"]
    ]
    chart = definition.get("chart")
    chart_matrix: list[dict[str, Any]] = []
    selected_dependencies: list[dict[str, str]] = []
    if chart:
        chart_path = root / "charts" / "charts" / chart / "Chart.yaml"
        selected_dependencies = chart_dependencies(chart_path)
        chart_matrix.append(
            {
                "target": target,
                "chart": chart,
                "version": version,
                "companions": definition.get("companions", []),
            }
        )

    plan = {
        "schema_version": 2,
        "candidate_workflow": "release-candidate.yml",
        "run_id": run_id,
        "source_sha": master_sha,
        "target": target,
        "version": version,
        "production_tag": f"{definition['tag_prefix']}{version}",
        "source_suites": definition["source_suites"],
        "kind_matrix": [
            {"name": name, "target": KIND_TARGETS[name][0], "timeout": KIND_TARGETS[name][1]}
            for name in definition["kind_scenarios"]
        ],
        "real_machine": definition["real_machine"],
        "chart_runtime": bool(definition.get("chart_runtime")),
        "image_matrix": images,
        "chart_matrix": chart_matrix,
        "forge": target == "forge",
        "tested_with": {
            "repository_versions": versions,
            "chart_metadata": {
                name: chart_metadata(root / "charts" / "charts" / name / "Chart.yaml")
                for name in (
                    "control-plane",
                    "inference-gateway",
                    "iterabase-platform",
                    "cert-manager-substrate",
                )
            },
            "selected_chart_dependencies": selected_dependencies,
            "fixture_versions": fixture_versions(root),
        },
    }
    return plan


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as source:
        for chunk in iter(lambda: source.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def asset_records(directory: Path) -> list[dict[str, Any]]:
    if not directory.exists():
        return []
    return [
        {
            "path": str(path.relative_to(directory)),
            "sha256": sha256_file(path),
            "size": path.stat().st_size,
        }
        for path in sorted(directory.rglob("*"))
        if path.is_file()
    ]


def validate_candidate_assets(plan: dict[str, Any], assets: Path) -> None:
    target = plan["target"]
    if plan["image_matrix"]:
        expected = {item["name"]: item for item in plan["image_matrix"]}
        discovered: set[str] = set()
        for metadata_path in sorted((assets / "images").glob("candidate-*.json")):
            if metadata_path.name.endswith(".spdx.json"):
                continue
            metadata = load_json(metadata_path)
            name = metadata.get("name")
            if not isinstance(name, str) or name not in expected:
                raise ReleaseError(f"{metadata_path} is unexpected candidate image metadata")
            if name in discovered:
                raise ReleaseError(f"candidate image metadata for {name} is duplicated")
            planned = expected[name]
            required_identity = {
                "schema_version": 2,
                "artifact_type": "image",
                "name": planned["name"],
                "target": planned["target"],
                "repository": planned["repository"],
                "candidate_tag": planned["candidate_tag"],
                "version": planned["version"],
                "source_sha": plan["source_sha"],
            }
            for field, value in required_identity.items():
                if metadata.get(field) != value:
                    raise ReleaseError(
                        f"{metadata_path} {field} does not match the planned candidate identity"
                    )
            digest = metadata.get("digest")
            if not isinstance(digest, str) or not re.fullmatch(r"sha256:[0-9a-f]{64}", digest):
                raise ReleaseError(f"{metadata_path} has no canonical digest")
            discovered.add(name)
        if discovered != set(expected):
            raise ReleaseError(
                f"candidate image evidence mismatch: {discovered} != {set(expected)}"
            )

    if plan["chart_matrix"]:
        chart = plan["chart_matrix"][0]["chart"]
        if not list((assets / "charts").glob(f"{chart}-*.tgz")):
            raise ReleaseError(f"candidate chart archive for {chart} is missing")
        if not (assets / "charts" / f"checksums-{chart}.txt").is_file():
            raise ReleaseError(f"candidate chart checksums for {chart} are missing")

    if plan["forge"]:
        archives = sorted((assets / "forge").glob("forge_*_*.tar.gz"))
        expected_platforms = {"linux_amd64", "linux_arm64", "darwin_amd64", "darwin_arm64"}
        found = {
            next((platform for platform in expected_platforms if platform in archive.name), "")
            for archive in archives
        }
        if found != expected_platforms:
            raise ReleaseError(f"Forge candidate platform matrix is incomplete: {found}")
        if not (assets / "forge" / "checksums.txt").is_file():
            raise ReleaseError("Forge candidate checksums are missing")


def assemble_evidence(plan: dict[str, Any], assets: Path) -> dict[str, Any]:
    validate_candidate_assets(plan, assets)
    records = asset_records(assets)
    if not records:
        raise ReleaseError("candidate has no recorded assets")
    return {
        "schema_version": 2,
        "candidate": {
            "workflow": plan["candidate_workflow"],
            "run_id": plan["run_id"],
            "source_sha": plan["source_sha"],
            "target": plan["target"],
            "version": plan["version"],
            "production_tag": plan["production_tag"],
        },
        "tested_with": plan["tested_with"],
        "validation": {"status": "passed"},
        "plan_sha256": hashlib.sha256((compact(plan) + "\n").encode()).hexdigest(),
        "assets": records,
    }


def verify_candidate(directory: Path) -> dict[str, Any]:
    plan_path = directory / "candidate-plan.json"
    evidence_path = directory / "candidate-evidence.json"
    assets = directory / "assets"
    plan = load_json(plan_path)
    evidence = load_json(evidence_path)
    if evidence.get("schema_version") != 2 or evidence.get("validation", {}).get("status") != "passed":
        raise ReleaseError("candidate evidence is not a passed schema-v2 record")
    expected_plan_hash = hashlib.sha256((compact(plan) + "\n").encode()).hexdigest()
    if evidence.get("plan_sha256") != expected_plan_hash:
        raise ReleaseError("candidate plan does not match evidence")
    actual = asset_records(assets)
    if evidence.get("assets") != actual:
        raise ReleaseError("candidate assets do not match recorded checksums")
    candidate = evidence.get("candidate", {})
    for field in ("run_id", "source_sha", "target", "version", "production_tag"):
        if str(candidate.get(field)) != str(plan.get(field)):
            raise ReleaseError(f"candidate evidence {field} does not match plan")
    validate_candidate_assets(plan, assets)
    return plan


def write_github_outputs(path: Path, plan: dict[str, Any]) -> None:
    outputs = {
        "plan": compact(plan),
        "target": plan["target"],
        "version": plan["version"],
        "production_tag": plan["production_tag"],
        "image_matrix": compact(plan["image_matrix"]),
        "chart_matrix": compact(plan["chart_matrix"]),
        "kind_matrix": compact(plan["kind_matrix"]),
        "has_images": str(bool(plan["image_matrix"])).lower(),
        "has_chart": str(bool(plan["chart_matrix"])).lower(),
        "has_forge": str(bool(plan["forge"])).lower(),
        "run_control_plane": str("control-plane" in plan["source_suites"]).lower(),
        "run_inference_gateway": str("inference-gateway" in plan["source_suites"]).lower(),
        "run_forge": str("forge" in plan["source_suites"]).lower(),
        "run_charts": str("charts" in plan["source_suites"]).lower(),
        "run_chart_runtime": str(plan["chart_runtime"]).lower(),
        "run_kind": str(bool(plan["kind_matrix"])).lower(),
        "run_real_machine": str(bool(plan["real_machine"])).lower(),
    }
    with path.open("a", encoding="utf-8") as output:
        for name, value in outputs.items():
            output.write(f"{name}={value}\n")


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser()
    sub = parser.add_subparsers(dest="command", required=True)
    sub.add_parser("validate")
    plan = sub.add_parser("plan")
    plan.add_argument("--target", required=True, choices=TARGET_NAMES)
    plan.add_argument("--master-sha", required=True)
    plan.add_argument("--run-id", required=True)
    plan.add_argument("--output", type=Path, required=True)
    plan.add_argument("--github-output", type=Path)
    evidence = sub.add_parser("evidence")
    evidence.add_argument("--plan", type=Path, required=True)
    evidence.add_argument("--assets", type=Path, required=True)
    evidence.add_argument("--output", type=Path, required=True)
    verify = sub.add_parser("verify-candidate")
    verify.add_argument("--directory", type=Path, required=True)
    return parser


def main() -> int:
    args = build_parser().parse_args()
    root = Path(__file__).resolve().parents[2]
    targets = load_json(root / "release" / "targets.json")
    try:
        validate_contract(targets, root)
        if args.command == "validate":
            print("release contract valid")
        elif args.command == "plan":
            plan = make_plan(targets, args.target, args.master_sha, args.run_id, root)
            args.output.write_text(compact(plan) + "\n", encoding="utf-8")
            print(compact(plan))
            output = args.github_output
            if output is None and os.environ.get("GITHUB_OUTPUT"):
                output = Path(os.environ["GITHUB_OUTPUT"])
            if output:
                write_github_outputs(output, plan)
        elif args.command == "evidence":
            evidence = assemble_evidence(load_json(args.plan), args.assets)
            args.output.write_text(compact(evidence) + "\n", encoding="utf-8")
            print(compact(evidence))
        elif args.command == "verify-candidate":
            print(compact(verify_candidate(args.directory)))
    except ReleaseError as exc:
        print(f"release contract error: {exc}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
