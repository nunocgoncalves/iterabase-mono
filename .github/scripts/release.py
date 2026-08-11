#!/usr/bin/env python3
"""Validate and plan protected monorepo releases.

The workflow remains intentionally thin: this module owns request parsing,
manifest consistency, deterministic temporary suite selection, and evidence
assembly so those security-sensitive decisions are fixture-tested.
"""

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
INPUT_TARGETS = {
    "control_plane_version": "control-plane",
    "inference_gateway_version": "inference-gateway",
    "forge_version": "forge",
    "control_plane_chart_version": "control-plane-chart",
    "inference_gateway_chart_version": "inference-gateway-chart",
    "platform_chart_version": "iterabase-platform-chart",
}
KIND_TARGETS = {
    "controlplane-identity": ("test-e2e-controlplane", 20),
    "inference-contract": ("test-e2e-inference", 20),
    "cert-issuers": ("test-e2e-cert-issuers", 20),
    "internal-tls": ("test-e2e-internal-tls", 25),
    "tool-runner-contract": ("test-e2e-tool-runner", 35),
}


class ReleaseError(ValueError):
    """A release contract or request is invalid."""


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
    return result


def validate_contract(
    manifest: dict[str, Any], targets: dict[str, Any], root: Path | None = None
) -> None:
    if manifest.get("schema_version") != 1:
        raise ReleaseError("compatibility manifest schema_version must be 1")
    if targets.get("schema_version") != 1:
        raise ReleaseError("release targets schema_version must be 1")
    if targets.get("temporary_until") != "HOR-476":
        raise ReleaseError("temporary suite mapping must name HOR-476 as its replacement owner")

    components = manifest.get("components")
    charts = manifest.get("charts")
    fixtures = manifest.get("fixtures")
    if not all(isinstance(value, dict) for value in (components, charts, fixtures)):
        raise ReleaseError("manifest components, charts, and fixtures must be objects")

    for name in ("control-plane", "inference-gateway", "forge"):
        if name not in components or not isinstance(components[name], dict):
            raise ReleaseError(f"manifest is missing component {name}")
        require_semver(components[name].get("version"), f"components.{name}.version")

    for name in ("control-plane", "inference-gateway", "iterabase-platform"):
        entry = charts.get(name)
        if not isinstance(entry, dict):
            raise ReleaseError(f"manifest is missing chart {name}")
        require_semver(entry.get("version"), f"charts.{name}.version")
        require_semver(entry.get("app_version"), f"charts.{name}.app_version")

    # A chart appVersion records that chart archive's default component image.
    # It deliberately need not equal the latest compatible component version:
    # components and charts release independently, and candidate E2E overrides
    # only the selected component while retaining manifest-pinned dependencies.
    companions = charts["iterabase-platform"].get("companions")
    if not isinstance(companions, dict):
        raise ReleaseError("platform chart companions must be an object")
    substrate = require_semver(
        companions.get("cert-manager-substrate"),
        "charts.iterabase-platform.companions.cert-manager-substrate",
    )
    if substrate != charts["iterabase-platform"]["version"]:
        raise ReleaseError("cert-manager-substrate must match the platform chart version")

    for name in (
        "platform_chart",
        "control_plane_chart",
        "certificate_migration_source",
    ):
        require_semver(fixtures.get(name), f"fixtures.{name}")

    definitions = targets.get("targets")
    if not isinstance(definitions, dict) or set(definitions) != set(INPUT_TARGETS.values()):
        raise ReleaseError("release target names do not match the workflow input contract")
    for name, definition in definitions.items():
        if not isinstance(definition, dict):
            raise ReleaseError(f"target {name} must be an object")
        section = definition.get("manifest_section")
        manifest_name = definition.get("manifest_name")
        if section not in {"components", "charts"} or manifest_name not in manifest[section]:
            raise ReleaseError(f"target {name} points at an unknown manifest entry")
        if not isinstance(definition.get("tag_prefix"), str):
            raise ReleaseError(f"target {name} has no tag prefix")
        scenarios = definition.get("kind_scenarios")
        if not isinstance(scenarios, list) or any(item not in KIND_TARGETS for item in scenarios):
            raise ReleaseError(f"target {name} has an unknown Kind scenario")
        suites = definition.get("source_suites")
        if not isinstance(suites, list) or any(
            item not in {"control-plane", "inference-gateway", "forge", "charts"}
            for item in suites
        ):
            raise ReleaseError(f"target {name} has an unknown source suite")

    if root is not None:
        for name, entry in charts.items():
            metadata = chart_metadata(root / "charts" / "charts" / name / "Chart.yaml")
            if metadata["name"] != name:
                raise ReleaseError(f"chart directory {name} declares name {metadata['name']}")
            if metadata["version"] != entry["version"]:
                raise ReleaseError(
                    f"chart {name} version {metadata['version']} does not match manifest {entry['version']}"
                )
            if metadata["appVersion"] != entry["app_version"]:
                raise ReleaseError(
                    f"chart {name} appVersion {metadata['appVersion']} does not match manifest {entry['app_version']}"
                )
        companion_metadata = chart_metadata(
            root / "charts" / "charts" / "cert-manager-substrate" / "Chart.yaml"
        )
        if companion_metadata["version"] != substrate:
            raise ReleaseError("cert-manager-substrate Chart.yaml does not match the manifest")

        fixture_file = root / "forge" / "test" / "e2e" / "chart_fixture_test.go"
        fixture_source = fixture_file.read_text(encoding="utf-8")
        expected_literals = {
            "pinnedPlatformChartVersion": fixtures["platform_chart"],
            "pinnedControlPlaneChartVersion": fixtures["control_plane_chart"],
            "certificateMigrationSourceVersion": fixtures["certificate_migration_source"],
        }
        for constant, version in expected_literals.items():
            pattern = rf'{constant}\s*=\s*"{re.escape(version)}"'
            if re.search(pattern, fixture_source) is None:
                raise ReleaseError(
                    f"fixture {constant} does not match manifest version {version}"
                )


def compact(value: Any) -> str:
    return json.dumps(value, sort_keys=True, separators=(",", ":"))


def selected_request(args: argparse.Namespace) -> dict[str, str]:
    selected: dict[str, str] = {}
    for input_name, target in INPUT_TARGETS.items():
        value = getattr(args, input_name, "").strip()
        if value:
            selected[target] = require_semver(value, input_name)
    if not selected:
        raise ReleaseError("at least one component or chart version must be requested")
    return selected


def make_plan(
    manifest: dict[str, Any],
    targets: dict[str, Any],
    selected: dict[str, str],
    master_sha: str,
    run_id: str,
    dry_run: bool,
) -> dict[str, Any]:
    if not SHA.fullmatch(master_sha):
        raise ReleaseError("master_sha must be a full lowercase 40-character commit SHA")
    if not run_id or not re.fullmatch(r"[0-9]+", run_id):
        raise ReleaseError("run_id must be numeric")
    validate_contract(manifest, targets)

    definitions = targets["targets"]
    release_targets: list[dict[str, Any]] = []
    images: list[dict[str, Any]] = []
    charts: list[dict[str, Any]] = []
    source_suites: set[str] = set()
    scenarios: set[str] = set()
    real_machine = False
    candidate_suffix = f"{master_sha[:12]}-{run_id}"

    for target in sorted(selected):
        definition = definitions[target]
        entry = manifest[definition["manifest_section"]][definition["manifest_name"]]
        expected = entry["version"]
        requested = selected[target]
        if requested != expected:
            raise ReleaseError(
                f"requested {target} version {requested} does not match manifest {expected}"
            )
        production_tag = definition["tag_prefix"] + requested
        release_tag = (
            f"dry-run/{production_tag}-{run_id}" if dry_run else production_tag
        )
        release_targets.append(
            {
                "target": target,
                "version": requested,
                "production_tag": production_tag,
                "release_tag": release_tag,
                "manifest_section": definition["manifest_section"],
                "manifest_name": definition["manifest_name"],
                "artifact_type": (
                    "forge"
                    if target == "forge"
                    else "chart"
                    if "chart" in definition
                    else "image"
                ),
            }
        )
        for image in definition.get("images", []):
            images.append(
                {
                    **image,
                    "target": target,
                    "version": requested,
                    "candidate_tag": f"candidate-{candidate_suffix}",
                    "dry_run_tag": f"dry-run-{requested}-{run_id}",
                }
            )
        if "chart" in definition:
            charts.append(
                {
                    "target": target,
                    "chart": definition["chart"],
                    "version": requested,
                    "companions": definition.get("companions", []),
                    "candidate_namespace": f"iterabase-release-candidates/{candidate_suffix}",
                    "dry_run_namespace": f"iterabase-release-dry-run/{run_id}",
                }
            )
        source_suites.update(definition["source_suites"])
        scenarios.update(definition["kind_scenarios"])
        real_machine |= bool(definition["real_machine"])

    kind_matrix = [
        {"name": name, "target": KIND_TARGETS[name][0], "timeout": KIND_TARGETS[name][1]}
        for name in KIND_TARGETS
        if name in scenarios
    ]
    return {
        "schema_version": 1,
        "master_sha": master_sha,
        "run_id": run_id,
        "dry_run": dry_run,
        "candidate_suffix": candidate_suffix,
        "selected": release_targets,
        "image_matrix": images,
        "chart_matrix": charts,
        "kind_matrix": kind_matrix,
        "source_suites": sorted(source_suites),
        "real_machine": real_machine,
        "compatibility": manifest,
        "temporary_suite_mapping_owner": targets["temporary_until"],
    }


def write_outputs(plan: dict[str, Any], path: Path) -> None:
    suites = set(plan["source_suites"])
    outputs: dict[str, Any] = {
        "plan": plan,
        "selected_matrix": plan["selected"],
        "image_matrix": plan["image_matrix"],
        "chart_matrix": plan["chart_matrix"],
        "kind_matrix": plan["kind_matrix"],
        "has_images": bool(plan["image_matrix"]),
        "has_charts": bool(plan["chart_matrix"]),
        "has_forge": any(item["target"] == "forge" for item in plan["selected"]),
        "has_platform_chart": any(
            item["target"] == "iterabase-platform-chart" for item in plan["selected"]
        ),
        "run_control_plane": "control-plane" in suites,
        "run_inference_gateway": "inference-gateway" in suites,
        "run_forge": "forge" in suites,
        "run_charts": "charts" in suites,
        "run_kind": bool(plan["kind_matrix"]),
        "run_real_machine": plan["real_machine"],
        "candidate_suffix": plan["candidate_suffix"],
        "dry_run": plan["dry_run"],
    }
    with path.open("a", encoding="utf-8") as output:
        for name, value in outputs.items():
            if isinstance(value, bool):
                rendered = str(value).lower()
            elif isinstance(value, str):
                rendered = value
            else:
                rendered = compact(value)
            output.write(f"{name}={rendered}\n")


def sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as source:
        for block in iter(lambda: source.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def assemble_evidence(plan: dict[str, Any], assets: Path) -> dict[str, Any]:
    files = []
    for path in sorted(item for item in assets.rglob("*") if item.is_file()):
        files.append(
            {
                "path": str(path.relative_to(assets)),
                "sha256": sha256(path),
                "size": path.stat().st_size,
            }
        )
    return {
        "schema_version": 1,
        "source": {
            "repository": os.environ.get("GITHUB_REPOSITORY", "nunocgoncalves/iterabase-mono"),
            "master_sha": plan["master_sha"],
            "workflow_run_id": plan["run_id"],
        },
        "mode": "dry-run" if plan["dry_run"] else "release",
        "selected": plan["selected"],
        "compatibility": plan["compatibility"],
        "validation": {
            "source_suites": plan["source_suites"],
            "kind_scenarios": [item["name"] for item in plan["kind_matrix"]],
            "real_machine": plan["real_machine"],
            "status": "passed",
            "temporary_mapping_replacement": plan["temporary_suite_mapping_owner"],
        },
        "assets": files,
    }


def parser() -> argparse.ArgumentParser:
    result = argparse.ArgumentParser()
    sub = result.add_subparsers(dest="command", required=True)

    validate = sub.add_parser("validate")
    validate.add_argument("--manifest", type=Path, default=Path("release/compatibility.json"))
    validate.add_argument("--targets", type=Path, default=Path("release/targets.json"))
    validate.add_argument("--root", type=Path, default=Path("."))

    plan = sub.add_parser("plan")
    plan.add_argument("--manifest", type=Path, default=Path("release/compatibility.json"))
    plan.add_argument("--targets", type=Path, default=Path("release/targets.json"))
    plan.add_argument("--master-sha", required=True)
    plan.add_argument("--run-id", required=True)
    plan.add_argument("--dry-run", action="store_true")
    plan.add_argument("--output", type=Path, required=True)
    plan.add_argument("--github-output", type=Path)
    for input_name in INPUT_TARGETS:
        plan.add_argument("--" + input_name.replace("_", "-"), default="")

    evidence = sub.add_parser("evidence")
    evidence.add_argument("--plan", type=Path, required=True)
    evidence.add_argument("--assets", type=Path, required=True)
    evidence.add_argument("--output", type=Path, required=True)
    return result


def main() -> int:
    args = parser().parse_args()
    try:
        if args.command == "validate":
            validate_contract(load_json(args.manifest), load_json(args.targets), args.root)
            print("release compatibility and target contracts are valid")
        elif args.command == "plan":
            manifest = load_json(args.manifest)
            targets = load_json(args.targets)
            plan = make_plan(
                manifest,
                targets,
                selected_request(args),
                args.master_sha,
                args.run_id,
                args.dry_run,
            )
            args.output.write_text(json.dumps(plan, indent=2, sort_keys=True) + "\n", encoding="utf-8")
            if args.github_output:
                write_outputs(plan, args.github_output)
            print(json.dumps(plan, indent=2, sort_keys=True))
        elif args.command == "evidence":
            plan = load_json(args.plan)
            evidence = assemble_evidence(plan, args.assets)
            args.output.write_text(
                json.dumps(evidence, indent=2, sort_keys=True) + "\n", encoding="utf-8"
            )
            print(json.dumps(evidence, indent=2, sort_keys=True))
    except ReleaseError as exc:
        print(f"release contract error: {exc}", file=sys.stderr)
        return 2
    return 0


if __name__ == "__main__":
    sys.exit(main())
