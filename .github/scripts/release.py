#!/usr/bin/env python3
"""Plan, record, and verify affected-target build-once release bundles."""

from __future__ import annotations

import argparse
from functools import lru_cache
import hashlib
import json
import os
from pathlib import Path
import re
import sys
import tempfile
from typing import Any

import e2e as e2e_contract

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
RUNNABLE_E2E_TIERS = {"F2", "F3"}
CANDIDATE_ALIAS_SCHEME = "source-run-attempt-v1"
POSITIVE_INTEGER = re.compile(r"^[1-9][0-9]*$")


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


@lru_cache(maxsize=4)
def load_scenario_catalogue(root_value: str | Path) -> dict[str, Any]:
    try:
        return e2e_contract.load_catalogue(Path(root_value).resolve())
    except e2e_contract.E2EError as exc:
        raise ReleaseError(str(exc)) from exc


def catalogue_scenarios(catalogue: dict[str, Any]) -> list[dict[str, Any]]:
    try:
        return e2e_contract.catalogue_scenarios(catalogue)
    except e2e_contract.E2EError as exc:
        raise ReleaseError(str(exc)) from exc


def require_semver(value: Any, label: str) -> str:
    if not isinstance(value, str) or not SEMVER.fullmatch(value):
        raise ReleaseError(f"{label} must be stable SemVer without a v prefix: {value!r}")
    return value


def candidate_image_alias(source_sha: Any, run_id: Any, run_attempt: Any) -> str:
    if not isinstance(source_sha, str) or not SHA.fullmatch(source_sha):
        raise ReleaseError("candidate alias source_sha must be a full lowercase commit SHA")
    if not isinstance(run_id, str) or not POSITIVE_INTEGER.fullmatch(run_id):
        raise ReleaseError("candidate alias run_id must be a positive integer")
    if not isinstance(run_attempt, str) or not POSITIVE_INTEGER.fullmatch(run_attempt):
        raise ReleaseError("candidate alias run_attempt must be a positive integer")
    return f"{source_sha}-{run_id}-{run_attempt}"


def validate_candidate_aliases(plan: dict[str, Any]) -> None:
    scheme = plan.get("candidate_alias_scheme")
    if scheme is None:
        # Promotion remains able to verify retained pre-HOR-523 schema-v3
        # candidates. Newly generated plans always declare the immutable scheme.
        return
    if scheme != CANDIDATE_ALIAS_SCHEME:
        raise ReleaseError(f"unsupported candidate alias scheme {scheme!r}")
    expected = candidate_image_alias(
        plan.get("source_sha"), plan.get("run_id"), plan.get("run_attempt")
    )
    for image in plan.get("image_matrix", []):
        if not isinstance(image, dict) or image.get("candidate_tag") != expected:
            raise ReleaseError("candidate image alias does not bind source SHA, run ID, and run attempt")


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


def chart_value(path: Path, key: str) -> str:
    wanted = key.split(".")
    stack: list[tuple[int, str]] = []
    try:
        lines = path.read_text(encoding="utf-8").splitlines()
    except OSError as exc:
        raise ReleaseError(f"cannot read chart values {path}: {exc}") from exc
    for raw in lines:
        if not raw.strip() or raw.lstrip().startswith("#"):
            continue
        match = re.match(r"^(\s*)([A-Za-z0-9_-]+):(?:\s*(.*?))?\s*$", raw)
        if match is None:
            continue
        indent = len(match.group(1))
        name = match.group(2)
        value = (match.group(3) or "").split(" #", 1)[0].strip()
        while stack and stack[-1][0] >= indent:
            stack.pop()
        current = [item[1] for item in stack] + [name]
        if value and current == wanted:
            return value.strip("\"'")
        if not value:
            stack.append((indent, name))
    raise ReleaseError(f"cannot resolve {key} from {path}")


def chart_image_version(path: Path, key: str = "image.tag") -> str:
    return require_semver(chart_value(path, key), f"{path} {key}")


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
        alias = re.match(r"^\s+alias:\s*([^\s#]+)", raw)
        if alias and current:
            # Helm aliases are the deployed dependency identity. Preserve that
            # name in release evidence instead of recording two indistinguishable
            # copies of the source chart.
            current["name"] = alias.group(1)
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

    published = load_json(root / "forge" / "test" / "e2e" / "published-fixture.json")
    if published.get("mode") != "published" or not isinstance(published.get("inputs"), list):
        raise ReleaseError("published E2E fixture must record exact published inputs")
    inputs: dict[str, dict[str, Any]] = {}
    for item in published["inputs"]:
        if not isinstance(item, dict):
            raise ReleaseError("published E2E fixture input must be an object")
        name = item.get("name")
        reference = item.get("reference")
        if not isinstance(name, str) or not isinstance(reference, str):
            raise ReleaseError("published E2E fixture input must have a name and reference")
        if name in inputs:
            raise ReleaseError(f"published E2E fixture input {name} is duplicated")
        if "latest" in reference.lower():
            raise ReleaseError("published E2E fixture must not use floating latest")
        inputs[name] = item

    authorities = {
        "platform_chart": (
            "iterabase-platform",
            "oci://ghcr.io/nunocgoncalves/iterabase-charts/iterabase-platform",
        ),
        "control_plane_chart": (
            "control-plane",
            "oci://ghcr.io/nunocgoncalves/iterabase-charts/control-plane",
        ),
        "certificate_migration_source": (
            "certificate-migration-source",
            "oci://ghcr.io/nunocgoncalves/iterabase-charts/iterabase-platform",
        ),
    }
    for authority, (input_name, repository) in authorities.items():
        item = inputs.get(input_name)
        expected = f"{repository}:{result[authority]}"
        if item is None or item.get("kind") != "published-chart" or item.get("reference") != expected:
            raise ReleaseError(
                f"published E2E fixture does not record {authority}={result[authority]}"
            )

    substrate = inputs.get("cert-manager-substrate")
    expected_substrate = (
        "oci://ghcr.io/nunocgoncalves/iterabase-charts/"
        f"cert-manager-substrate:{result['platform_chart']}"
    )
    if (
        substrate is None
        or substrate.get("kind") != "published-chart"
        or substrate.get("reference") != expected_substrate
    ):
        raise ReleaseError("published E2E fixture substrate does not match platform_chart")
    return result


def chart_transition_baselines(root: Path) -> dict[str, list[dict[str, str]]]:
    path = root / "charts" / "test" / "e2e" / "transition-baselines.json"
    fixture = load_json(path)
    if fixture.get("mode") != "published" or not isinstance(fixture.get("inputs"), list):
        raise ReleaseError("chart transition baselines must record published fixture inputs")
    expected = {
        "supported-platform-predecessor": "iterabase-platform",
        "supported-substrate-predecessor": "cert-manager-substrate",
        "metallb-platform-predecessor": "iterabase-platform",
        "metallb-substrate-predecessor": "cert-manager-substrate",
    }
    charts: list[dict[str, str]] = []
    for item in fixture["inputs"]:
        if not isinstance(item, dict):
            raise ReleaseError("chart transition baseline input must be an object")
        name = item.get("name")
        reference = item.get("reference")
        checksum = item.get("checksum")
        if item.get("kind") != "published-chart" or name not in expected:
            raise ReleaseError(f"unexpected chart transition baseline {name!r}")
        if not isinstance(reference, str) or ":" not in reference:
            raise ReleaseError(f"chart transition baseline {name} has no exact OCI version")
        repository, version = reference.rsplit(":", 1)
        require_semver(version, f"chart transition baseline {name}")
        chart = repository.rsplit("/", 1)[-1]
        if not repository.startswith("oci://") or chart != expected[name]:
            raise ReleaseError(f"chart transition baseline {name} has invalid reference {reference!r}")
        if not isinstance(checksum, str) or not re.fullmatch(r"[0-9a-f]{64}", checksum):
            raise ReleaseError(f"chart transition baseline {name} has no exact archive checksum")
        charts.append(
            {
                "name": name,
                "chart": chart,
                "repository": repository,
                "version": version,
                "sha256": checksum,
            }
        )
    if len(charts) != len(expected) or {item["name"] for item in charts} != set(expected):
        raise ReleaseError("chart transition baseline pair is incomplete or duplicated")
    current = chart_metadata(
        root / "charts" / "charts" / "iterabase-platform" / "Chart.yaml"
    )["version"]
    substrate_current = chart_metadata(
        root / "charts" / "charts" / "cert-manager-substrate" / "Chart.yaml"
    )["version"]
    currents = {"iterabase-platform": current, "cert-manager-substrate": substrate_current}
    for item in charts:
        chart_version = currents[item["chart"]]
        if tuple(map(int, item["version"].split("."))) >= tuple(map(int, chart_version.split("."))):
            raise ReleaseError(
                f"chart transition predecessor {item['name']} must be older than current {item['chart']} {chart_version}"
            )
    return {"charts": charts}


def validate_contract(
    targets: dict[str, Any], root: Path, catalogue: dict[str, Any] | None = None
) -> None:
    if targets.get("schema_version") != 4:
        raise ReleaseError("release targets schema_version must be 4")
    definitions = targets.get("targets")
    recipes = targets.get("artifact_recipes")
    if not isinstance(definitions, dict) or tuple(definitions) != TARGET_NAMES:
        raise ReleaseError("release target names or order do not match the workflow contract")
    if not isinstance(recipes, dict):
        raise ReleaseError("release artifact recipes are missing")

    for target, definition in definitions.items():
        if not isinstance(definition, dict) or not isinstance(definition.get("tag_prefix"), str):
            raise ReleaseError(f"target {target} is incomplete")
        artifacts = definition.get("artifacts")
        if not isinstance(artifacts, list) or not artifacts:
            raise ReleaseError(f"target {target} has no artifact recipes")
        for artifact in artifacts:
            recipe = recipes.get(artifact)
            if not isinstance(recipe, dict) or recipe.get("target") != target:
                raise ReleaseError(f"target {target} has invalid artifact recipe {artifact!r}")
        if "version_file" in definition:
            read_version(root / definition["version_file"])
        elif "chart" in definition:
            chart = definition["chart"]
            metadata = chart_metadata(root / "charts" / "charts" / chart / "Chart.yaml")
            if metadata["name"] != chart:
                raise ReleaseError(f"chart target {target} points at {metadata['name']}")
        else:
            raise ReleaseError(f"target {target} has no version authority")

    compiled = catalogue or load_scenario_catalogue(root)
    try:
        e2e_contract.validate_catalogue_contract(compiled, targets)
    except e2e_contract.E2EError as exc:
        raise ReleaseError(str(exc)) from exc
    fixture_versions(root)
    chart_transition_baselines(root)


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


def parse_targets(value: str | list[str] | tuple[str, ...]) -> list[str]:
    raw = value.split(",") if isinstance(value, str) else list(value)
    requested = [item.strip() for item in raw]
    if not requested or any(not item for item in requested):
        raise ReleaseError("targets must be a non-empty comma-separated target set")
    unknown = sorted(set(requested) - set(TARGET_NAMES))
    if unknown:
        raise ReleaseError(f"unknown release targets: {', '.join(unknown)}")
    duplicates = sorted({item for item in requested if requested.count(item) > 1})
    if duplicates:
        raise ReleaseError(f"duplicate release targets: {', '.join(duplicates)}")
    return [target for target in TARGET_NAMES if target in requested]


def source_suites_for_targets(selected: list[str]) -> list[str]:
    suites: list[str] = []
    for target in selected:
        suite = "charts" if target.endswith("-chart") else target
        if suite not in suites:
            suites.append(suite)
    return suites


def make_plan(
    targets: dict[str, Any],
    selected_targets: str | list[str],
    master_sha: str,
    run_id: str,
    root: Path,
    catalogue: dict[str, Any] | None = None,
    *,
    run_attempt: str = "1",
) -> dict[str, Any]:
    selected = parse_targets(selected_targets)
    candidate_tag = candidate_image_alias(master_sha, run_id, run_attempt)

    versions = repository_versions(root, targets)
    metadata = {
        name: chart_metadata(root / "charts" / "charts" / name / "Chart.yaml")
        for name in (
            "control-plane",
            "inference-gateway",
            "iterabase-platform",
            "cert-manager-substrate",
        )
    }
    fixtures = fixture_versions(root)
    compiled_catalogue = catalogue or load_scenario_catalogue(root)
    validate_contract(targets, root, compiled_catalogue)
    releases: list[dict[str, Any]] = []
    images: list[dict[str, Any]] = []
    chart_matrix: list[dict[str, Any]] = []
    selected_chart_dependencies: list[dict[str, Any]] = []

    recipes = targets["artifact_recipes"]
    for target in selected:
        definition = targets["targets"][target]
        version = versions[target]
        artifact_types: list[str] = []
        target_recipes = [recipes[name] for name in definition["artifacts"]]
        image_recipes = [recipe for recipe in target_recipes if recipe["kind"] == "image"]
        if image_recipes:
            artifact_types.append("image")
            images.extend(
                {
                    "name": image["name"],
                    "artifact": next(name for name in definition["artifacts"] if recipes[name] is image),
                    "repository": image["repository"],
                    "context": image["context"],
                    "dockerfile": image["dockerfile"],
                    "build_args": e2e_contract.render_recipe_values(image["build_args"], version=version, source_sha=master_sha),
                    "labels": e2e_contract.render_recipe_values(image["labels"], version=version, source_sha=master_sha),
                    "build_args_text": "\n".join(e2e_contract.render_recipe_values(image["build_args"], version=version, source_sha=master_sha)),
                    "labels_text": "\n".join(e2e_contract.render_recipe_values(image["labels"], version=version, source_sha=master_sha)),
                    "recipe_sha256": e2e_contract.recipe_hash(image),
                    "target": target,
                    "version": version,
                    "candidate_tag": candidate_tag,
                }
                for image in image_recipes
            )
        chart_recipes = [recipe for recipe in target_recipes if recipe["kind"] == "chart"]
        if chart_recipes:
            artifact_types.append("chart")
            recipe = chart_recipes[0]
            chart = recipe["chart"]
            companions = recipe.get("companions", [])
            dependencies = chart_dependencies(root / "charts" / "charts" / chart / "Chart.yaml")
            selected_chart_dependencies.append({"target": target, "chart": chart, "dependencies": dependencies})
            for companion in companions:
                selected_chart_dependencies.append(
                    {
                        "target": target,
                        "chart": companion,
                        "dependencies": chart_dependencies(
                            root / "charts" / "charts" / companion / "Chart.yaml"
                        ),
                    }
                )
            chart_matrix.append(
                {
                    "target": target,
                    "chart": chart,
                    "version": version,
                    "companions": companions,
                    "recipe_sha256": e2e_contract.recipe_hash(recipe),
                    "companion_recipes": [
                        {
                            "chart": companion,
                            "artifact": name,
                            "recipe_sha256": e2e_contract.recipe_hash(recipes[name]),
                        }
                        for companion in companions
                        for name in definition["artifacts"]
                        if recipes[name].get("chart") == companion
                    ],
                }
            )
        if any(recipe["kind"] == "forge" for recipe in target_recipes):
            artifact_types.append("forge")
        releases.append(
            {
                "target": target,
                "version": version,
                "production_tag": f"{definition['tag_prefix']}{version}",
                "artifact_types": artifact_types,
            }
        )

    source_suites = source_suites_for_targets(selected)
    try:
        execution_plan = e2e_contract.make_plan(
            root,
            compiled_catalogue,
            targets,
            intent="candidate",
            source_sha=master_sha,
            targets=selected,
        )
    except e2e_contract.E2EError as exc:
        raise ReleaseError(str(exc)) from exc
    selected_scenario_ids = set(execution_plan["selected_scenario_ids"])
    scenarios = [
        scenario
        for scenario in catalogue_scenarios(compiled_catalogue)
        if scenario["id"] in selected_scenario_ids
    ]
    chart_runtime = False
    kind_matrix = execution_plan["kind_matrix"]
    real_machine_matrix = execution_plan["real_machine_matrix"]

    # Derive product baselines from every artifact-backed runtime fixture in the
    # selected suite union. Owner/source checks use the exact checkout; Kind and
    # real-machine fixtures must use either a selected candidate or an immutable
    # published identity recorded here.
    selected_set = set(selected)
    scenario_names = {scenario["metadata"]["name"] for scenario in scenarios}
    platform_scenarios = {
        "deployed-execution-contracts",
        "deployed-identity-api",
        "deployed-work-recovery",
        "deployed-artifact-durability",
        "certificate-ownership-migration",
        "fresh-install",
        "observability",
        "observability-tls",
        "internal-tls",
        "n-minus-one-upgrade",
        "feature-enable-upgrade",
        "single-node-observability-ingress-recovery",
        "reapply-rollback-recovery",
    }
    real_machine = bool(real_machine_matrix)
    transition_baselines = (
        chart_transition_baselines(root)
        if any(scenario["suite"]["owner"] == "charts" for scenario in scenarios)
        else {"charts": []}
    )
    uses_platform_chart = bool(platform_scenarios.intersection(scenario_names)) or real_machine
    uses_substrate_chart = uses_platform_chart

    baseline_charts: list[dict[str, Any]] = []

    def add_baseline_chart(chart: str, version: str) -> None:
        if any(item["chart"] == chart for item in baseline_charts):
            return
        baseline_charts.append(
            {
                "chart": chart,
                "version": version,
                "repository": f"oci://ghcr.io/nunocgoncalves/iterabase-charts/{chart}",
            }
        )

    if uses_platform_chart and "iterabase-platform-chart" not in selected_set:
        add_baseline_chart("iterabase-platform", fixtures["platform_chart"])
    if uses_substrate_chart and "iterabase-platform-chart" not in selected_set:
        add_baseline_chart("cert-manager-substrate", fixtures["platform_chart"])

    image_definitions = {
        recipe["name"]: recipe
        for recipe in targets["artifact_recipes"].values()
        if recipe.get("kind") == "image" and recipe.get("target") in {"control-plane", "inference-gateway"}
    }
    baseline_images: list[dict[str, Any]] = []

    def add_baseline_image(
        name: str,
        target: str,
        *,
        version: str | None = None,
        version_chart: str | None = None,
        values_path: str | None = None,
        value_key: str = "image.tag",
    ) -> None:
        image: dict[str, Any] = {
            "name": name,
            "target": target,
            "repository": image_definitions[name]["repository"],
        }
        if version is not None:
            image["version"] = version
        else:
            image["version_from"] = {
                "chart": version_chart,
                "values_path": values_path,
                "value_key": value_key,
            }
        baseline_images.append(image)

    control_values = root / "charts" / "charts" / "control-plane" / "values.yaml"
    inference_values = root / "charts" / "charts" / "inference-gateway" / "values.yaml"
    selected_control_chart = bool(
        selected_set.intersection({"control-plane-chart", "iterabase-platform-chart"})
    )
    selected_inference_chart = bool(
        selected_set.intersection({"inference-gateway-chart", "iterabase-platform-chart"})
    )

    uses_control_image = bool(scenario_names) or real_machine
    uses_harness_image = "deployed-execution-contracts" in scenario_names or real_machine
    uses_tool_runner_image = "deployed-execution-contracts" in scenario_names or real_machine
    uses_inference_image = uses_platform_chart
    if "control-plane" not in selected_set and uses_control_image:
        if selected_control_chart:
            add_baseline_image(
                "control-plane",
                "control-plane",
                version=chart_image_version(control_values),
            )
        else:
            source_chart = "iterabase-platform" if uses_platform_chart else "control-plane"
            values_path = (
                "charts/control-plane/values.yaml"
                if source_chart == "iterabase-platform"
                else "values.yaml"
            )
            add_baseline_image(
                "control-plane",
                "control-plane",
                version_chart=source_chart,
                values_path=values_path,
            )
    if "control-plane" not in selected_set and uses_harness_image:
        add_baseline_image(
            "control-plane-harness",
            "control-plane",
            version=versions["control-plane"],
        )
    if "control-plane" not in selected_set and uses_tool_runner_image:
        if selected_control_chart:
            add_baseline_image(
                "control-plane-tool-runner",
                "control-plane",
                version=chart_image_version(control_values, "toolRunner.image.tag"),
            )
        else:
            source_chart = "iterabase-platform" if uses_platform_chart else "control-plane"
            values_path = (
                "charts/control-plane/values.yaml"
                if source_chart == "iterabase-platform"
                else "values.yaml"
            )
            add_baseline_image(
                "control-plane-tool-runner",
                "control-plane",
                version_chart=source_chart,
                values_path=values_path,
                value_key="toolRunner.image.tag",
            )
    if "inference-gateway" not in selected_set and uses_inference_image:
        if selected_inference_chart:
            add_baseline_image(
                "inference-gateway",
                "inference-gateway",
                version=chart_image_version(inference_values),
            )
        else:
            add_baseline_image(
                "inference-gateway",
                "inference-gateway",
                version_chart="iterabase-platform",
                values_path="charts/inference-gateway/values.yaml",
            )

    plan = {
        "schema_version": 3,
        "candidate_workflow": "release-candidate.yml",
        "candidate_alias_scheme": CANDIDATE_ALIAS_SCHEME,
        "run_id": run_id,
        "run_attempt": run_attempt,
        "source_sha": master_sha,
        "targets": selected,
        "releases": releases,
        "source_suites": source_suites,
        "selected_scenarios": execution_plan["selected_scenario_ids"],
        "execution_plan": execution_plan,
        "kind_matrix": kind_matrix,
        "real_machine_matrix": real_machine_matrix,
        "real_machine": real_machine,
        "chart_runtime": chart_runtime,
        "image_matrix": images,
        "chart_matrix": chart_matrix,
        "forge": "forge" in selected,
        "forge_recipe_sha256": e2e_contract.recipe_hash(targets["artifact_recipes"]["forge-binary"]),
        "baseline_dependencies": {
            "images": baseline_images,
            "charts": baseline_charts,
        },
        "transition_baselines": transition_baselines,
        "tested_with": {
            "repository_versions": versions,
            "chart_metadata": metadata,
            "selected_chart_dependencies": selected_chart_dependencies,
            "fixture_versions": fixtures,
            "transition_baselines": transition_baselines,
            "scenario_catalogue": {
                "schema_version": compiled_catalogue["schema_version"],
                "selected": [
                    {
                        "id": scenario["id"],
                        "metadata": scenario["metadata"],
                        "stages": scenario["stages"],
                    }
                    for scenario in scenarios
                ],
            },
        },
    }
    return plan


def candidate_job_selection(plan: dict[str, Any]) -> dict[str, bool]:
    source_suites = plan.get("source_suites")
    if not isinstance(source_suites, list) or any(
        not isinstance(suite, str) for suite in source_suites
    ):
        raise ReleaseError("candidate plan source_suites must be a list of names")
    for field in ("image_matrix", "chart_matrix", "kind_matrix"):
        if not isinstance(plan.get(field), list):
            raise ReleaseError(f"candidate plan {field} must be a list")
    if not isinstance(plan.get("real_machine_matrix"), list):
        raise ReleaseError("candidate plan real_machine_matrix must be a list")
    execution = plan.get("execution_plan")
    if not isinstance(execution, dict) or not isinstance(execution.get("artifact_build_matrix"), list):
        raise ReleaseError("candidate plan has no compiled execution plan")
    for field in ("forge", "chart_runtime", "real_machine"):
        if not isinstance(plan.get(field), bool):
            raise ReleaseError(f"candidate plan {field} must be a boolean")

    return {
        "preflight": True,
        "control-plane-source": "control-plane" in source_suites,
        "inference-gateway-source": "inference-gateway" in source_suites,
        "forge-source": "forge" in source_suites,
        "charts-source": "charts" in source_suites,
        "image-candidates": bool(plan["image_matrix"]),
        "runtime-artifacts": bool(execution["artifact_build_matrix"]),
        "chart-candidate": bool(plan["chart_matrix"]),
        "forge-candidate": plan["forge"],
        "kind-candidates": bool(plan["kind_matrix"]),
        "real-machine-candidates": plan["real_machine"],
    }


def validate_candidate_job_results(
    plan: dict[str, Any], needs: dict[str, Any]
) -> dict[str, str]:
    selected = candidate_job_selection(plan)
    missing = sorted(set(selected) - set(needs))
    unexpected = sorted(set(needs) - set(selected))
    if missing or unexpected:
        raise ReleaseError(
            "candidate validation job set mismatch: "
            + compact({"missing": missing, "unexpected": unexpected})
        )

    results: dict[str, str] = {}
    incomplete: dict[str, dict[str, Any]] = {}
    for name, is_selected in selected.items():
        job = needs[name]
        result = job.get("result") if isinstance(job, dict) else None
        if not isinstance(result, str):
            raise ReleaseError(f"candidate validation job {name} has no result")
        results[name] = result
        if result == "success" or (result == "skipped" and not is_selected):
            continue
        incomplete[name] = {"result": result, "selected": is_selected}

    if incomplete:
        raise ReleaseError("candidate validation incomplete: " + compact(incomplete))
    return results


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
    validate_candidate_aliases(plan)
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
                "recipe_sha256": planned["recipe_sha256"],
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

    for chart_plan in plan["chart_matrix"]:
        chart = chart_plan["chart"]
        version = chart_plan["version"]
        metadata = load_json(assets / "charts" / f"candidate-chart-{chart}.json")
        for field, expected in {
            "schema_version": 2,
            "artifact_type": "chart",
            "target": chart_plan["target"],
            "chart": chart,
            "version": version,
            "source_sha": plan["source_sha"],
            "recipe_sha256": chart_plan["recipe_sha256"],
        }.items():
            if metadata.get(field) != expected:
                raise ReleaseError(f"candidate chart {chart} {field} does not match the plan")
        expected_archives = [chart, *chart_plan["companions"]]
        for expected_chart in expected_archives:
            if not (assets / "charts" / f"{expected_chart}-{version}.tgz").is_file():
                raise ReleaseError(f"candidate chart archive for {expected_chart} is missing")
        if not (assets / "charts" / f"checksums-{chart}.txt").is_file():
            raise ReleaseError(f"candidate chart checksums for {chart} are missing")

    if plan["forge"]:
        metadata = load_json(assets / "forge" / "candidate-forge.json")
        if metadata.get("source_sha") != plan["source_sha"] or metadata.get("recipe_sha256") != plan["forge_recipe_sha256"]:
            raise ReleaseError("Forge candidate source or recipe identity does not match the plan")
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
    with tempfile.TemporaryDirectory(prefix="iterabase-candidate-plan-") as value:
        normalized_plan = Path(value) / "candidate-plan.json"
        normalized_plan.write_text(compact(plan) + "\n", encoding="utf-8")
        try:
            scenario_results = e2e_contract.validate_results(normalized_plan, assets / "results")
        except e2e_contract.E2EError as exc:
            raise ReleaseError(f"candidate scenario evidence is incomplete: {exc}") from exc
    records = asset_records(assets)
    if not records:
        raise ReleaseError("candidate has no recorded assets")
    candidate = {
        "workflow": plan["candidate_workflow"],
        "run_id": plan["run_id"],
        "source_sha": plan["source_sha"],
        "targets": plan["targets"],
        "releases": plan["releases"],
    }
    for field in ("candidate_alias_scheme", "run_attempt"):
        if field in plan:
            candidate[field] = plan[field]
    return {
        "schema_version": 3,
        "candidate": candidate,
        "tested_with": plan["tested_with"],
        "validation": {
            "status": "passed",
            "scenario_results": [
                {
                    "scenario_id": result["scenario_id"],
                    "stage_graph_sha256": result["stage_graph_sha256"],
                    "runtime_bundle_sha256": result["runtime_bundle_sha256"],
                    "stages": result["stages"],
                    "artifacts": result["artifacts"],
                }
                for result in scenario_results
            ],
        },
        "plan_sha256": hashlib.sha256((compact(plan) + "\n").encode()).hexdigest(),
        "assets": records,
    }


def verify_candidate(directory: Path) -> dict[str, Any]:
    plan_path = directory / "candidate-plan.json"
    evidence_path = directory / "candidate-evidence.json"
    assets = directory / "assets"
    plan = load_json(plan_path)
    evidence = load_json(evidence_path)
    if evidence.get("schema_version") != 3 or evidence.get("validation", {}).get("status") != "passed":
        raise ReleaseError("candidate evidence is not a passed schema-v3 record")
    expected_plan_hash = hashlib.sha256((compact(plan) + "\n").encode()).hexdigest()
    if evidence.get("plan_sha256") != expected_plan_hash:
        raise ReleaseError("candidate plan does not match evidence")
    actual = asset_records(assets)
    if evidence.get("assets") != actual:
        raise ReleaseError("candidate assets do not match recorded checksums")
    with tempfile.TemporaryDirectory(prefix="iterabase-candidate-plan-") as value:
        normalized_plan = Path(value) / "candidate-plan.json"
        normalized_plan.write_text(compact(plan) + "\n", encoding="utf-8")
        try:
            scenario_results = e2e_contract.validate_results(normalized_plan, assets / "results")
        except e2e_contract.E2EError as exc:
            raise ReleaseError(f"candidate scenario evidence is incomplete: {exc}") from exc
    recorded_results = evidence.get("validation", {}).get("scenario_results")
    actual_results = [
        {
            "scenario_id": result["scenario_id"],
            "stage_graph_sha256": result["stage_graph_sha256"],
            "runtime_bundle_sha256": result["runtime_bundle_sha256"],
            "stages": result["stages"],
            "artifacts": result["artifacts"],
        }
        for result in scenario_results
    ]
    if recorded_results != actual_results:
        raise ReleaseError("candidate evidence does not retain the exact scenario/stage/runtime result records")
    candidate = evidence.get("candidate", {})
    fields = ["run_id", "source_sha", "targets", "releases"]
    fields.extend(
        field for field in ("candidate_alias_scheme", "run_attempt") if field in plan
    )
    for field in fields:
        if candidate.get(field) != plan.get(field):
            raise ReleaseError(f"candidate evidence {field} does not match plan")
    validate_candidate_assets(plan, assets)
    return plan


def write_github_outputs(path: Path, plan: dict[str, Any]) -> None:
    forge_release = next(
        (release for release in plan["releases"] if release["target"] == "forge"), None
    )
    outputs = {
        "plan": compact(plan),
        "targets": compact(plan["targets"]),
        "releases": compact(plan["releases"]),
        "forge_version": forge_release["version"] if forge_release else "",
        "forge_recipe_sha256": plan["forge_recipe_sha256"],
        "image_matrix": compact(plan["image_matrix"]),
        "chart_matrix": compact(plan["chart_matrix"]),
        "kind_matrix": compact(plan["kind_matrix"]),
        "real_machine_matrix": compact(plan["real_machine_matrix"]),
        "runtime_artifact_matrix": compact(plan["execution_plan"]["artifact_build_matrix"]),
        "has_runtime_artifacts": str(bool(plan["execution_plan"]["artifact_build_matrix"])).lower(),
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
    validate_jobs = sub.add_parser("validate-jobs")
    validate_jobs.add_argument("--plan", type=Path, required=True)
    validate_jobs.add_argument("--needs", type=Path, required=True)
    outputs = sub.add_parser("outputs")
    outputs.add_argument("--plan", type=Path, required=True)
    outputs.add_argument("--github-output", type=Path, required=True)
    plan = sub.add_parser("plan")
    plan.add_argument("--targets", required=True)
    plan.add_argument("--master-sha", required=True)
    plan.add_argument("--run-id", required=True)
    plan.add_argument("--run-attempt", required=True)
    plan.add_argument("--output", type=Path, required=True)
    plan.add_argument("--github-output", type=Path)
    evidence = sub.add_parser("evidence")
    evidence.add_argument("--plan", type=Path, required=True)
    evidence.add_argument("--assets", type=Path, required=True)
    evidence.add_argument("--output", type=Path, required=True)
    verify = sub.add_parser("verify-candidate")
    verify.add_argument("--directory", type=Path, required=True)
    image_version = sub.add_parser("image-version")
    image_version.add_argument("--values", type=Path, required=True)
    image_version.add_argument("--key", required=True)
    return parser


def main() -> int:
    args = build_parser().parse_args()
    root = Path(__file__).resolve().parents[2]
    targets = load_json(root / "release" / "targets.json")
    try:
        validate_contract(targets, root)
        if args.command == "validate":
            print("release contract valid")
        elif args.command == "validate-jobs":
            plan = load_json(args.plan)
            needs = load_json(args.needs)
            results = validate_candidate_job_results(plan, needs)
            print("candidate validation results:", compact(results))
        elif args.command == "outputs":
            write_github_outputs(args.github_output, load_json(args.plan))
        elif args.command == "plan":
            plan = make_plan(
                targets,
                args.targets,
                args.master_sha,
                args.run_id,
                root,
                run_attempt=args.run_attempt,
            )
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
        elif args.command == "image-version":
            print(chart_image_version(args.values, args.key))
    except ReleaseError as exc:
        print(f"release contract error: {exc}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
