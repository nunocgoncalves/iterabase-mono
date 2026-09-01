#!/usr/bin/env python3
"""Compile, compose, execute, and reconcile one E2E contract in every workflow."""

from __future__ import annotations

import argparse
from collections import Counter
import fnmatch
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tarfile
import tempfile
from typing import Any, Iterable
from urllib.parse import urlparse

PLAN_SCHEMA_VERSION = 2
CATALOGUE_SCHEMA_VERSION = 2
RUNTIME_SCHEMA_VERSION = 1
RESULT_SCHEMA_VERSION = 1
RUNNABLE_TIERS = {"F2", "F3"}
INTENTS = {"pr", "nightly", "candidate"}
SHA = re.compile(r"^[0-9a-f]{40}$")
SHA256 = re.compile(r"^(?:sha256:)?[0-9a-f]{64}$")
NAME = re.compile(r"^[a-z0-9]+(?:[a-z0-9-]*[a-z0-9])?$")
JOB_GRACE_MINUTES = 5
CAPACITY_JOB_GRACE_MINUTES = 30
SHARED_PR_PATHS = (
    ".github/actions/**",
    ".github/scripts/e2e.py",
    ".github/scripts/test_e2e.py",
    ".github/workflows/e2e.yml",
    ".github/workflows/release-candidate.yml",
    "testkit/e2e/**",
    "Makefile",
    "go.work",
    "go.work.sum",
    "release/targets.json",
)
IMAGE_ENV = {
    "control-plane-image": "CONTROL_PLANE",
    "harness-image": "HARNESS",
    "tool-runner-image": "TOOL_RUNNER",
    "inference-gateway-image": "INFERENCE_GATEWAY",
    "runtime-fixture-image": "FORGE_E2E_RUNTIME",
}
TRANSITION_ENV = {
    "certificate-migration-chart": "ITERABASE_E2E_CERTIFICATE_MIGRATION_ARCHIVE",
    "supported-platform-predecessor": "ITERABASE_E2E_PREDECESSOR_PLATFORM_ARCHIVE",
    "supported-substrate-predecessor": "ITERABASE_E2E_PREDECESSOR_SUBSTRATE_ARCHIVE",
    "metallb-platform-predecessor": "ITERABASE_E2E_METALLB_PREDECESSOR_PLATFORM_ARCHIVE",
    "metallb-substrate-predecessor": "ITERABASE_E2E_METALLB_PREDECESSOR_SUBSTRATE_ARCHIVE",
}


class E2EError(ValueError):
    """The generated plan, artifact set, or result evidence is incomplete."""


def compact(value: Any) -> str:
    return json.dumps(value, sort_keys=True, separators=(",", ":"))


def hash_bytes(value: bytes) -> str:
    return hashlib.sha256(value).hexdigest()


def hash_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as source:
        for chunk in iter(lambda: source.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def hash_json(value: Any) -> str:
    return hash_bytes(compact(value).encode("utf-8"))


def hash_stage_graph(stages: list[dict[str, Any]]) -> str:
    # Go emits StageMetadata in struct-field order and the runner hashes that
    # exact representation; preserve parsed insertion order here.
    return hash_bytes(json.dumps(stages, separators=(",", ":")).encode("utf-8"))


def read_object(path: Path) -> dict[str, Any]:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        raise E2EError(f"cannot read {path}: {exc}") from exc
    if not isinstance(value, dict):
        raise E2EError(f"{path} must contain a JSON object")
    return value


def run(command: list[str], *, cwd: Path | None = None, capture: bool = False) -> str:
    try:
        completed = subprocess.run(
            command,
            cwd=cwd,
            check=True,
            text=True,
            stdout=subprocess.PIPE if capture else None,
            stderr=subprocess.PIPE if capture else None,
        )
    except (OSError, subprocess.CalledProcessError) as exc:
        stderr = getattr(exc, "stderr", "") or ""
        raise E2EError(f"command failed: {' '.join(command)}\n{stderr}") from exc
    return (completed.stdout or "").strip()


def load_catalogue(root: Path) -> dict[str, Any]:
    output = run(
        [
            "go",
            "run",
            "./testkit/e2e/cmd/e2e-catalogue",
            "--root",
            str(root),
            "--format",
            "json",
        ],
        cwd=root,
        capture=True,
    )
    try:
        catalogue = json.loads(output)
    except json.JSONDecodeError as exc:
        raise E2EError(f"compiled E2E catalogue is not JSON: {exc}") from exc
    if not isinstance(catalogue, dict) or catalogue.get("schema_version") != CATALOGUE_SCHEMA_VERSION:
        raise E2EError(
            f"compiled E2E catalogue must use schema_version {CATALOGUE_SCHEMA_VERSION}"
        )
    return catalogue


def catalogue_scenarios(catalogue: dict[str, Any]) -> list[dict[str, Any]]:
    suites = catalogue.get("suites")
    if not isinstance(suites, list):
        raise E2EError("compiled E2E catalogue suites must be a list")
    result: list[dict[str, Any]] = []
    seen: set[str] = set()
    for suite_value in suites:
        if not isinstance(suite_value, dict):
            raise E2EError("compiled E2E catalogue contains an invalid suite")
        suite = suite_value.get("suite")
        scenarios = suite_value.get("scenarios")
        if not isinstance(suite, dict) or not isinstance(scenarios, list):
            raise E2EError("compiled E2E catalogue contains an invalid suite")
        for field in ("name", "owner", "entrypoint"):
            if not isinstance(suite.get(field), str) or not suite[field]:
                raise E2EError(f"compiled E2E suite has no {field}")
        for scenario in scenarios:
            if not isinstance(scenario, dict) or not isinstance(scenario.get("metadata"), dict):
                raise E2EError("compiled E2E catalogue contains an invalid scenario")
            scenario_id = scenario.get("id")
            if not isinstance(scenario_id, str) or not scenario_id:
                raise E2EError("compiled E2E scenario has no ID")
            if scenario_id in seen:
                raise E2EError(f"compiled E2E catalogue repeats scenario {scenario_id!r}")
            seen.add(scenario_id)
            result.append({**scenario, "suite": suite})
    return sorted(result, key=lambda item: item["id"])


def load_contract(root: Path) -> dict[str, Any]:
    contract = read_object(root / "release" / "targets.json")
    if contract.get("schema_version") != 4:
        raise E2EError("release target and artifact recipe contract must use schema_version 4")
    recipes = contract.get("artifact_recipes")
    targets = contract.get("targets")
    baselines = contract.get("published_baselines")
    if not isinstance(recipes, dict) or not isinstance(targets, dict) or not isinstance(baselines, dict):
        raise E2EError("release target contract is missing recipes, targets, or baselines")
    for name, recipe in recipes.items():
        if not NAME.fullmatch(name) or not isinstance(recipe, dict) or not isinstance(recipe.get("kind"), str):
            raise E2EError(f"invalid artifact recipe {name!r}")
        if recipe.get("kind") in {"image", "chart", "chart-companion", "forge"}:
            paths = recipe.get("paths")
            if not isinstance(paths, list) or not paths or any(not isinstance(path, str) for path in paths):
                raise E2EError(f"buildable artifact {name!r} has no PR path routing")
        if recipe.get("kind") == "image":
            for field in ("name", "repository", "context", "dockerfile", "build_args", "labels"):
                if not recipe.get(field):
                    raise E2EError(f"image recipe {name!r} has no {field}")
        if recipe.get("kind") == "forge" and not recipe.get("goreleaser_version"):
            raise E2EError("Forge recipe has no GoReleaser version")
    for target, definition in targets.items():
        if not isinstance(definition, dict) or not isinstance(definition.get("artifacts"), list):
            raise E2EError(f"release target {target!r} has no artifact list")
        for artifact in definition["artifacts"]:
            if artifact not in recipes or recipes[artifact].get("target") != target:
                raise E2EError(f"release target {target!r} has invalid artifact {artifact!r}")
    return contract


def validate_catalogue_contract(catalogue: dict[str, Any], contract: dict[str, Any]) -> None:
    recipes = contract["artifact_recipes"]
    targets = contract["targets"]
    covered_targets: set[str] = set()
    for scenario in catalogue_scenarios(catalogue):
        metadata = scenario["metadata"]
        if metadata.get("tier") not in RUNNABLE_TIERS:
            continue
        artifacts = metadata.get("required_artifacts")
        intents = metadata.get("intents")
        modes = metadata.get("fixture_modes")
        release_targets = metadata.get("release_targets")
        if not isinstance(artifacts, list) or not artifacts:
            raise E2EError(f"runnable scenario {scenario['id']} has no artifact requirements")
        unknown_artifacts = sorted(set(artifacts) - set(recipes))
        if unknown_artifacts:
            raise E2EError(
                f"scenario {scenario['id']} requires unknown artifacts: {unknown_artifacts}"
            )
        if not isinstance(intents, list) or set(intents) != INTENTS:
            raise E2EError(f"runnable scenario {scenario['id']} must route PR, nightly, and candidate intent")
        if not isinstance(modes, list) or "source" not in modes or "candidate" not in modes:
            raise E2EError(f"runnable scenario {scenario['id']} lacks a supported source/candidate fixture path")
        if not metadata.get("make_target") or not isinstance(metadata.get("timeout_minutes"), int) or metadata["timeout_minutes"] <= 0:
            raise E2EError(f"runnable scenario {scenario['id']} has incomplete runtime metadata")
        if not isinstance(release_targets, list) or not release_targets:
            raise E2EError(f"runnable scenario {scenario['id']} has no candidate routing")
        unknown_targets = sorted(set(release_targets) - set(targets))
        if unknown_targets:
            raise E2EError(f"scenario {scenario['id']} has unknown release targets: {unknown_targets}")
        for target in release_targets:
            if not any(recipes[artifact].get("target") == target for artifact in artifacts):
                raise E2EError(f"scenario {scenario['id']} routes candidate target {target} without requiring one of its artifacts")
        covered_targets.update(release_targets)
        stages = scenario.get("stages")
        if not isinstance(stages, list) or not stages:
            raise E2EError(f"runnable scenario {scenario['id']} has no declared stage graph")
        if metadata.get("tier") == "F3":
            if not metadata.get("capacity") or metadata.get("mandatory_capacity") is not True:
                raise E2EError(f"selected capacity scenario {scenario['id']} is not mandatory")
    missing = sorted(set(targets) - covered_targets)
    if missing:
        raise E2EError(f"compiled catalogue has no candidate coverage for targets: {missing}")


def matches(path: str, patterns: Iterable[str]) -> bool:
    return any(fnmatch.fnmatchcase(path, pattern) for pattern in patterns)


def is_docs(path: str) -> bool:
    return path.endswith(".md") or path.startswith("docs/")


def target_version(root: Path, contract: dict[str, Any], target: str | None) -> str:
    if not target:
        return "source"
    definition = contract["targets"][target]
    if "version_file" in definition:
        return (root / definition["version_file"]).read_text(encoding="utf-8").strip()
    chart = definition["chart"]
    for line in (root / "charts" / "charts" / chart / "Chart.yaml").read_text(encoding="utf-8").splitlines():
        if line.startswith("version:"):
            return line.split(":", 1)[1].strip().strip('"')
    raise E2EError(f"chart target {target} has no version")


def recipe_hash(recipe: dict[str, Any]) -> str:
    return hash_json(recipe)


def scenario_entry(scenario: dict[str, Any], fixture_mode: str, expected: list[dict[str, Any]]) -> dict[str, Any]:
    metadata = scenario["metadata"]
    stages = scenario["stages"]
    entry = {
        "id": scenario["id"],
        "owner": scenario["suite"]["owner"],
        "entrypoint": scenario["suite"]["entrypoint"],
        "name": metadata["name"],
        "target": metadata["make_target"],
        "tier": metadata["tier"],
        "fixture_mode": fixture_mode,
        "scenario_timeout": metadata["timeout_minutes"],
        "timeout": metadata["timeout_minutes"] + JOB_GRACE_MINUTES,
        "artifact": scenario["id"].replace("/", "-"),
        "stage_graph_sha256": hash_stage_graph(stages),
        "stages": stages,
        "artifacts": expected,
    }
    if metadata["tier"] == "F3":
        capacity = metadata["capacity"]
        entry.update(
            {
                "capacity": capacity,
                "mandatory": True,
                "capacity_group": f"e2e-capacity-{capacity}",
            }
        )
    return entry


def select_scenarios(
    scenarios: list[dict[str, Any]],
    recipes: dict[str, Any],
    intent: str,
    paths: list[str],
    targets: list[str],
    select_all: bool,
) -> tuple[list[dict[str, Any]], set[str]]:
    runnable = [
        scenario
        for scenario in scenarios
        if scenario["metadata"].get("tier") in RUNNABLE_TIERS
        and intent in scenario["metadata"].get("intents", [])
    ]
    if intent == "nightly" or select_all:
        return runnable, {
            artifact
            for scenario in runnable
            for artifact in scenario["metadata"]["required_artifacts"]
            if recipes[artifact]["kind"] not in {"published-chart"}
        }
    if intent == "candidate":
        selected_targets = set(targets)
        selected = [
            scenario
            for scenario in runnable
            if selected_targets.intersection(scenario["metadata"]["release_targets"])
        ]
        affected = {
            artifact
            for artifact, recipe in recipes.items()
            if recipe.get("target") in selected_targets
        }
        return selected, affected

    normalized = [path.strip().removeprefix("./") for path in paths if path.strip()]
    meaningful = [path for path in normalized if not is_docs(path)]
    if not meaningful:
        return [], set()
    if any(matches(path, SHARED_PR_PATHS) for path in meaningful):
        return runnable, {
            artifact
            for scenario in runnable
            for artifact in scenario["metadata"]["required_artifacts"]
            if recipes[artifact]["kind"] != "published-chart"
        }
    affected = {
        artifact
        for artifact, recipe in recipes.items()
        if any(matches(path, recipe.get("paths", [])) for path in meaningful)
    }
    owners = {
        owner
        for owner in {scenario["suite"]["owner"] for scenario in runnable}
        if any(path.startswith(f"{owner}/test/e2e/") for path in meaningful)
    }
    selected = [
        scenario
        for scenario in runnable
        if set(scenario["metadata"]["required_artifacts"]).intersection(affected)
        or scenario["suite"]["owner"] in owners
    ]
    if owners:
        affected.update(
            artifact
            for scenario in selected
            for artifact in scenario["metadata"]["required_artifacts"]
            if recipes[artifact]["kind"] not in {"published-chart"}
        )
    return selected, affected


def expected_artifact(
    root: Path,
    contract: dict[str, Any],
    artifact: str,
    intent: str,
    source_sha: str,
    affected: set[str],
    selected_targets: set[str],
) -> dict[str, Any]:
    recipe = contract["artifact_recipes"][artifact]
    kind = recipe["kind"]
    target = recipe.get("target")
    buildable = kind in {"image", "chart", "chart-companion", "forge"}
    selected_candidate = intent == "candidate" and target in selected_targets
    temporary = (intent in {"pr", "nightly"} and artifact in affected) or recipe.get("temporary_only") is True
    if kind == "published-chart":
        custody = "published-baseline"
    elif selected_candidate and not recipe.get("temporary_only"):
        custody = "selected-candidate"
    elif temporary:
        custody = "selected-temporary"
    else:
        custody = "published-baseline"
    if intent == "pr" and artifact in affected and custody == "published-baseline":
        raise E2EError(f"affected PR artifact {artifact} cannot use a published baseline")
    if intent == "candidate" and target in selected_targets and custody == "published-baseline":
        raise E2EError(f"selected candidate target {target} cannot use a published baseline for {artifact}")
    expected: dict[str, Any] = {
        "name": artifact,
        "kind": kind,
        "custody": custody,
        "recipe_sha256": recipe_hash(recipe),
    }
    if custody == "published-baseline":
        reference = recipe.get("reference") or contract["published_baselines"].get(artifact)
        if not reference:
            if buildable:
                # Validation-only fixtures have no semantic publication and must
                # therefore be built from the exact source in every selected run.
                expected["custody"] = "selected-temporary"
                expected["source_sha"] = source_sha
            else:
                raise E2EError(f"artifact {artifact} has no immutable published baseline")
        else:
            expected["reference"] = reference
            if recipe.get("checksum"):
                expected["checksum"] = recipe["checksum"]
    else:
        expected["source_sha"] = source_sha
        expected["version"] = target_version(root, contract, target)
    return expected


def make_plan(
    root: Path,
    catalogue: dict[str, Any],
    contract: dict[str, Any],
    *,
    intent: str,
    source_sha: str,
    paths: list[str] | None = None,
    targets: list[str] | None = None,
    select_all: bool = False,
) -> dict[str, Any]:
    if intent not in INTENTS:
        raise E2EError(f"unsupported E2E intent {intent!r}")
    if not SHA.fullmatch(source_sha):
        raise E2EError("E2E source SHA must be a full lowercase commit SHA")
    validate_catalogue_contract(catalogue, contract)
    paths = paths or []
    targets = targets or []
    unknown_targets = sorted(set(targets) - set(contract["targets"]))
    if unknown_targets:
        raise E2EError(f"unknown candidate targets: {unknown_targets}")
    scenarios, affected = select_scenarios(
        catalogue_scenarios(catalogue),
        contract["artifact_recipes"],
        intent,
        paths,
        targets,
        select_all,
    )
    selected_targets = set(targets)
    fixture_mode = "candidate" if intent == "candidate" else "source"
    matrix: list[dict[str, Any]] = []
    all_expected: dict[str, dict[str, Any]] = {}
    for scenario in scenarios:
        expected = [
            expected_artifact(
                root,
                contract,
                artifact,
                intent,
                source_sha,
                affected,
                selected_targets,
            )
            for artifact in scenario["metadata"]["required_artifacts"]
        ]
        expected.sort(key=lambda item: item["name"])
        for item in expected:
            prior = all_expected.get(item["name"])
            if prior is not None and prior != item:
                raise E2EError(f"artifact {item['name']} has inconsistent custody across selected scenarios")
            all_expected[item["name"]] = item
        matrix.append(scenario_entry(scenario, fixture_mode, expected))

    build_matrix: list[dict[str, Any]] = []
    for artifact in sorted(all_expected):
        expected = all_expected[artifact]
        if expected["custody"] != "selected-temporary":
            continue
        recipe = contract["artifact_recipes"][artifact]
        build_matrix.append(
            {
                "artifact": artifact,
                "kind": recipe["kind"],
                "target": recipe.get("target", ""),
                "version": expected.get("version", "source"),
                "recipe_sha256": expected["recipe_sha256"],
            }
        )
    kind_matrix = [entry for entry in matrix if entry["tier"] == "F2"]
    real_scenarios = [entry for entry in matrix if entry["tier"] == "F3"]
    real_matrix = []
    for capacity in sorted({entry["capacity"] for entry in real_scenarios}):
        capacity_scenarios = [entry for entry in real_scenarios if entry["capacity"] == capacity]
        real_matrix.append(
            {
                "capacity": capacity,
                "capacity_group": f"e2e-capacity-{capacity}",
                "artifact": capacity,
                "timeout": sum(entry["timeout"] for entry in capacity_scenarios) + CAPACITY_JOB_GRACE_MINUTES,
                "scenarios": capacity_scenarios,
            }
        )
    selected_ids = [entry["id"] for entry in matrix]
    if len(selected_ids) != len(set(selected_ids)):
        raise E2EError("generated E2E plan repeats a selected scenario")
    return {
        "schema_version": PLAN_SCHEMA_VERSION,
        "intent": intent,
        "source_sha": source_sha,
        "catalogue_schema_version": catalogue["schema_version"],
        "catalogue_sha256": hash_json(catalogue),
        "changed_paths": sorted(set(paths)) if intent == "pr" else [],
        "selected_targets": targets if intent == "candidate" else [],
        "affected_artifacts": sorted(affected),
        "scenario_total": len(matrix),
        "owner_totals": dict(sorted(Counter(entry["owner"] for entry in matrix).items())),
        "selected_scenario_ids": selected_ids,
        "artifact_build_matrix": build_matrix,
        "scenario_matrix": matrix,
        "kind_matrix": kind_matrix,
        "real_machine_matrix": real_matrix,
    }


def write_outputs(path: Path, plan: dict[str, Any]) -> None:
    outputs = {
        "plan": compact(plan),
        "artifact_build_matrix": plan["artifact_build_matrix"],
        "scenario_matrix": plan["scenario_matrix"],
        "kind_matrix": plan["kind_matrix"],
        "real_machine_matrix": plan["real_machine_matrix"],
        "has_artifacts": bool(plan["artifact_build_matrix"]),
        "has_scenarios": bool(plan["scenario_matrix"]),
        "has_kind": bool(plan["kind_matrix"]),
        "has_real_machine": bool(plan["real_machine_matrix"]),
        "scenario_total": plan["scenario_total"],
    }
    with path.open("a", encoding="utf-8") as output:
        for name, value in outputs.items():
            if isinstance(value, bool):
                rendered = str(value).lower()
            elif isinstance(value, (dict, list)):
                rendered = compact(value)
            else:
                rendered = str(value)
            output.write(f"{name}={rendered}\n")


def verify_source(root: Path, source_sha: str) -> None:
    head = run(["git", "rev-parse", "HEAD"], cwd=root, capture=True)
    if head != source_sha:
        raise E2EError(f"checked-out source {head} does not match planned exact head {source_sha}")
    dirty = run(["git", "status", "--porcelain", "--untracked-files=no"], cwd=root, capture=True)
    if dirty:
        raise E2EError("exact-source artifact/composition checkout has tracked modifications")


def render_recipe_values(value: list[str], *, version: str, source_sha: str) -> list[str]:
    repository = os.environ.get("GITHUB_REPOSITORY", "nunocgoncalves/iterabase-mono")
    return [item.format(version=version, source_sha=source_sha, repository=repository) for item in value]


def write_metadata(output: Path, artifact: str, metadata: dict[str, Any]) -> None:
    (output / f"{artifact}.json").write_text(
        json.dumps(metadata, indent=2, sort_keys=True) + "\n", encoding="utf-8"
    )


def build_artifact(root: Path, plan: dict[str, Any], contract: dict[str, Any], artifact: str, output: Path) -> None:
    execution = plan.get("execution_plan", plan)
    verify_source(root, execution["source_sha"])
    matrix = {item["artifact"]: item for item in execution["artifact_build_matrix"]}
    if artifact not in matrix:
        raise E2EError(f"artifact {artifact!r} is not selected for temporary production")
    recipe = contract["artifact_recipes"][artifact]
    selected = matrix[artifact]
    if recipe_hash(recipe) != selected["recipe_sha256"]:
        raise E2EError(f"artifact recipe drift for {artifact}")
    output.mkdir(parents=True, exist_ok=True)
    kind = recipe["kind"]
    source_sha = execution["source_sha"]
    version = selected["version"]
    metadata: dict[str, Any] = {
        "schema_version": 1,
        "artifact_type": kind,
        "name": artifact,
        "source_sha": source_sha,
        "version": version,
        "recipe_sha256": selected["recipe_sha256"],
    }
    if kind == "image":
        tag = f"iterabase-e2e/{recipe['name']}:{source_sha}"
        command = ["docker", "build", "-t", tag, "-f", str(root / recipe["dockerfile"])]
        for build_arg in render_recipe_values(recipe["build_args"], version=version, source_sha=source_sha):
            command.extend(["--build-arg", build_arg])
        for label in render_recipe_values(recipe["labels"], version=version, source_sha=source_sha):
            command.extend(["--label", label])
        command.append(str(root / recipe["context"]))
        run(command, cwd=root)
        digest = run(["docker", "image", "inspect", "--format={{.Id}}", tag], capture=True)
        if not SHA256.fullmatch(digest):
            raise E2EError(f"built image {artifact} has no canonical digest")
        archive = output / f"{artifact}.tar"
        run(["docker", "save", "-o", str(archive), tag])
        metadata.update(
            {
                "repository": f"iterabase-e2e/{recipe['name']}",
                "tag": source_sha,
                "reference": tag,
                "digest": digest,
                "file": archive.name,
                "sha256": hash_file(archive),
            }
        )
    elif kind in {"chart", "chart-companion"}:
        for dependency in recipe.get("dependency_builds", []):
            run(["helm", "dependency", "build", str(root / "charts" / "charts" / dependency)])
        chart = recipe["chart"]
        run(["helm", "package", str(root / "charts" / "charts" / chart), "--destination", str(output)])
        archive = output / f"{chart}-{version}.tgz"
        if not archive.is_file():
            raise E2EError(f"Helm did not produce {archive}")
        metadata.update(
            {
                "chart": chart,
                "reference": archive.name,
                "file": archive.name,
                "sha256": hash_file(archive),
            }
        )
    elif kind == "forge":
        run(
            [
                "go", "run", f"github.com/goreleaser/goreleaser/v2@{recipe['goreleaser_version']}",
                "build", "--snapshot", "--clean", "--single-target",
            ],
            cwd=root / "forge",
        )
        built = [path for path in (root / "forge" / "dist").rglob("forge") if path.is_file()]
        if len(built) != 1:
            raise E2EError(f"GoReleaser produced an ambiguous runtime Forge binary: {built}")
        binary = output / "forge"
        shutil.copy2(built[0], binary)
        binary.chmod(0o755)
        metadata.update(
            {
                "reference": binary.name,
                "file": binary.name,
                "sha256": hash_file(binary),
                "goreleaser_version": recipe["goreleaser_version"],
                "goreleaser_config_sha256": hash_file(root / recipe["goreleaser_config"]),
            }
        )
    else:
        raise E2EError(f"artifact {artifact} uses non-buildable kind {kind}")
    verify_source(root, source_sha)
    write_metadata(output, artifact, metadata)


def find_metadata(artifacts: Path, artifact: str, recipe: dict[str, Any]) -> tuple[dict[str, Any], Path] | None:
    direct = sorted(artifacts.rglob(f"{artifact}.json")) if artifacts.exists() else []
    for path in direct:
        value = read_object(path)
        if value.get("name") == artifact:
            return value, path.parent
    release_name = recipe.get("name")
    if release_name:
        for path in sorted(artifacts.rglob(f"candidate-{release_name}.json")):
            value = read_object(path)
            if value.get("name") == release_name:
                return value, path.parent
    chart = recipe.get("chart")
    if chart:
        for path in sorted(artifacts.rglob(f"candidate-chart-{chart}.json")):
            return read_object(path), path.parent
        # Companion archives share the selected outer chart's source/version
        # metadata and checksum file; they intentionally do not manufacture a
        # second semantic release target.
        for path in sorted(artifacts.rglob("candidate-chart-*.json")):
            value = read_object(path)
            version = value.get("version")
            if isinstance(version, str) and (path.parent / f"{chart}-{version}.tgz").is_file():
                return value, path.parent
    return None


def split_image(reference: str) -> tuple[str, str]:
    if "@" in reference:
        reference = reference.split("@", 1)[0]
    index = reference.rfind(":")
    if index <= reference.rfind("/") or index == len(reference) - 1:
        raise E2EError(f"image baseline is not exactly tagged: {reference!r}")
    return reference[:index], reference[index + 1 :]


def split_chart(reference: str) -> tuple[str, str, str]:
    index = reference.rfind(":")
    if index <= reference.rfind("/") or index == len(reference) - 1:
        raise E2EError(f"chart baseline is not exactly versioned: {reference!r}")
    repository = reference[:index]
    version = reference[index + 1 :]
    chart = repository.rstrip("/").split("/")[-1]
    return repository, chart, version


def load_image_archive(metadata: dict[str, Any], directory: Path, expected: dict[str, Any]) -> tuple[str, str, str, Path]:
    archive = directory / str(metadata.get("file", ""))
    if not archive.is_file() or hash_file(archive) != metadata.get("sha256"):
        raise E2EError(f"temporary image {expected['name']} archive checksum mismatch")
    run(["docker", "load", "-i", str(archive)])
    if metadata.get("source_sha") != expected.get("source_sha") or metadata.get("recipe_sha256") != expected["recipe_sha256"]:
        raise E2EError(f"temporary image {expected['name']} identity does not match the plan")
    repository, tag = split_image(metadata["reference"])
    digest = metadata.get("digest")
    if not isinstance(digest, str) or not SHA256.fullmatch(digest):
        raise E2EError(f"temporary image {expected['name']} has no digest")
    revision = run(["docker", "image", "inspect", "--format={{index .Config.Labels \"org.opencontainers.image.revision\"}}", metadata["reference"]], capture=True)
    if revision != expected["source_sha"]:
        raise E2EError(f"temporary image {expected['name']} revision label does not match exact source")
    return repository, tag, digest, archive


def pull_image(reference: str, expected_digest: str | None = None) -> tuple[str, str, str, Path]:
    repository, tag = split_image(reference)
    tagged_reference = reference.split("@", 1)[0]
    run(["docker", "pull", reference])
    digests = run(["docker", "image", "inspect", "--format={{join .RepoDigests \"\\n\"}}", tagged_reference], capture=True).splitlines()
    matching = [value.split("@", 1)[1] for value in digests if value.startswith(repository + "@") and "@" in value]
    if len(set(matching)) != 1 or not SHA256.fullmatch(matching[0]):
        raise E2EError(f"image {reference} has ambiguous immutable identity: {digests}")
    digest = matching[0]
    if expected_digest and digest != expected_digest:
        raise E2EError(f"image {reference} digest {digest} != {expected_digest}")
    temporary = Path(tempfile.mkdtemp(prefix="iterabase-e2e-image-")) / "image.tar"
    run(["docker", "save", "-o", str(temporary), tagged_reference])
    return repository, tag, digest, temporary


def pull_chart(reference: str, destination: Path, checksum: str | None = None) -> tuple[Path, str]:
    repository, chart, version = split_chart(reference)
    destination.mkdir(parents=True, exist_ok=True)
    run(["helm", "pull", repository, "--version", version, "--destination", str(destination)])
    archive = destination / f"{chart}-{version}.tgz"
    if not archive.is_file():
        raise E2EError(f"Helm did not pull {reference}")
    actual = hash_file(archive)
    if checksum and actual != checksum.removeprefix("sha256:"):
        raise E2EError(f"chart {reference} checksum {actual} != {checksum}")
    return archive, actual


def extract_chart(archive: Path, destination: Path) -> Path:
    destination.mkdir(parents=True, exist_ok=True)
    with tarfile.open(archive, "r:gz") as bundle:
        members = bundle.getmembers()
        if not members or any(Path(member.name).is_absolute() or ".." in Path(member.name).parts for member in members):
            raise E2EError(f"unsafe chart archive {archive}")
        bundle.extractall(destination, filter="data")
    chart = archive.name.rsplit("-", 1)[0]
    path = destination / chart
    if not path.is_dir():
        candidates = [entry for entry in destination.iterdir() if entry.is_dir()]
        if len(candidates) != 1:
            raise E2EError(f"chart archive {archive} has ambiguous root")
        path = candidates[0]
    return path


def chart_version(path: Path) -> str:
    for line in (path / "Chart.yaml").read_text(encoding="utf-8").splitlines():
        if line.startswith("version:"):
            return line.split(":", 1)[1].strip().strip('"')
    raise E2EError(f"chart {path} has no version")


def set_chart_dependency_version(platform: Path, dependency: str, version: str) -> None:
    path = platform / "Chart.yaml"
    lines = path.read_text(encoding="utf-8").splitlines()
    try:
        header = next(index for index, line in enumerate(lines) if line.strip() == "dependencies:")
    except StopIteration as error:
        raise E2EError(f"platform chart has no dependency {dependency!r}") from error

    # `helm package` canonicalizes Chart.yaml: sequence markers move to column
    # zero and map keys are sorted, so an item commonly starts with
    # `- condition:` while `name:` appears later. Inspect dependency item
    # boundaries rather than assuming the source-file `- name:` layout.
    header_indent = len(lines[header]) - len(lines[header].lstrip())
    item_indent: int | None = None
    current: list[int] = []
    items: list[list[int]] = []
    for index in range(header + 1, len(lines)):
        line = lines[index]
        stripped = line.strip()
        marker = re.match(r"^(\s*)-\s+[^#\s]", line)
        if marker and (item_indent is None or len(marker.group(1)) == item_indent):
            indent = len(marker.group(1))
            if item_indent is None:
                item_indent = indent
            if current:
                items.append(current)
            current = [index]
            continue
        if item_indent is None:
            if stripped and not stripped.startswith("#") and len(line) - len(line.lstrip()) <= header_indent:
                break
            continue
        if stripped and not stripped.startswith("#") and len(line) - len(line.lstrip()) <= header_indent:
            break
        if current:
            current.append(index)
    if current:
        items.append(current)

    scalar = re.compile(r"^\s*(?:-\s*)?(name|version):\s*(['\"]?)([^'\"#\s]+)\2\s*(?:#.*)?$")
    for item in items:
        name = ""
        version_index: int | None = None
        for index in item:
            match = scalar.match(lines[index])
            if match is None:
                continue
            if match.group(1) == "name":
                name = match.group(3)
            elif match.group(1) == "version":
                version_index = index
        if name != dependency or version_index is None:
            continue
        match = scalar.match(lines[version_index])
        assert match is not None
        prefix = lines[version_index][: match.start(2)]
        quote = match.group(2)
        suffix = lines[version_index][match.end(3) + len(quote) :]
        lines[version_index] = f"{prefix}{quote}{version}{quote}{suffix}"
        path.write_text("\n".join(lines) + "\n", encoding="utf-8")
        lock = platform / "Chart.lock"
        if lock.exists():
            lock.unlink()
        return
    raise E2EError(f"platform chart has no dependency {dependency!r}")


def scenario_from_plan(plan: dict[str, Any], scenario_id: str) -> dict[str, Any]:
    execution = plan.get("execution_plan", plan)
    if not isinstance(execution, dict) or execution.get("schema_version") != PLAN_SCHEMA_VERSION:
        raise E2EError("execution plan uses an unsupported schema")
    matches = [item for item in execution.get("scenario_matrix", []) if item.get("id") == scenario_id]
    if len(matches) != 1:
        raise E2EError(f"plan does not select scenario {scenario_id!r} exactly once")
    return matches[0]


def compose_runtime(plan_path: Path, scenario_id: str, artifacts: Path, output: Path, env_output: Path, root: Path, contract: dict[str, Any]) -> None:
    plan = read_object(plan_path)
    execution = plan.get("execution_plan", plan)
    scenario = scenario_from_plan(plan, scenario_id)
    source_sha = execution["source_sha"]
    verify_source(root, source_sha)
    output.mkdir(parents=True, exist_ok=True)
    runtime = output / "runtime"
    runtime.mkdir(parents=True, exist_ok=True)
    records: list[dict[str, Any]] = []
    env: dict[str, str] = {}
    chart_paths: dict[str, Path] = {}
    chart_archives: dict[str, Path] = {}

    for expected in scenario["artifacts"]:
        name = expected["name"]
        recipe = contract["artifact_recipes"][name]
        if recipe_hash(recipe) != expected["recipe_sha256"]:
            raise E2EError(f"recipe drift for composed artifact {name}")
        custody = expected["custody"]
        kind = recipe["kind"]
        record: dict[str, Any] = {
            "name": name,
            "kind": kind,
            "custody": custody,
            "reference": expected.get("reference", name),
            "recipe_sha256": expected["recipe_sha256"],
        }
        if custody != "published-baseline":
            record["source_sha"] = source_sha
        discovered = find_metadata(artifacts, name, recipe)

        if kind == "image":
            archive: Path
            if custody == "selected-temporary":
                if discovered is None:
                    raise E2EError(f"selected temporary image {name} is missing")
                metadata, directory = discovered
                repository, tag, digest, archive = load_image_archive(metadata, directory, expected)
                reference = f"{repository}:{tag}"
            elif custody == "selected-candidate":
                if discovered is None:
                    raise E2EError(f"selected candidate image {name} is missing")
                metadata, _ = discovered
                if metadata.get("source_sha") != source_sha or metadata.get("recipe_sha256") != expected["recipe_sha256"]:
                    raise E2EError(f"candidate image {name} uses the wrong source SHA or recipe")
                repository = metadata.get("repository")
                tag = metadata.get("candidate_tag")
                wanted = metadata.get("digest")
                if not isinstance(repository, str) or not isinstance(tag, str) or not isinstance(wanted, str):
                    raise E2EError(f"candidate image {name} has incomplete identity")
                reference = f"{repository}:{tag}"
                repository, tag, digest, archive = pull_image(reference, wanted)
            else:
                reference = expected["reference"]
                repository, tag, digest, archive = pull_image(reference, expected.get("digest"))
            local_archive = runtime / f"{name}.tar"
            shutil.copy2(archive, local_archive)
            record.update({"reference": reference, "digest": digest, "checksum": hash_file(local_archive), "path": str(local_archive)})
            prefix = IMAGE_ENV[name]
            env[f"{prefix}_IMAGE_REPO"] = repository
            env[f"{prefix}_IMAGE_TAG"] = tag
            env[f"{prefix}_IMAGE_DIGEST"] = digest
            env[f"{prefix}_IMAGE_ARCHIVE"] = str(local_archive)
            if custody != "published-baseline":
                env[f"{prefix}_IMAGE_SOURCE_SHA"] = source_sha
            forge_archive_env = {
                "control-plane-image": "FORGE_E2E_CONTROL_PLANE_IMAGE_ARCHIVE",
                "harness-image": "FORGE_E2E_HARNESS_IMAGE_ARCHIVE",
                "tool-runner-image": "FORGE_E2E_TOOL_RUNNER_IMAGE_ARCHIVE",
                "inference-gateway-image": "FORGE_E2E_INFERENCE_IMAGE_ARCHIVE",
                "runtime-fixture-image": "FORGE_E2E_RUNTIME_IMAGE_ARCHIVE",
            }[name]
            env[forge_archive_env] = str(local_archive)
        elif kind in {"chart", "chart-companion", "published-chart"}:
            if custody in {"selected-temporary", "selected-candidate"}:
                if discovered is None:
                    raise E2EError(f"selected chart artifact {name} is missing")
                metadata, directory = discovered
                if metadata.get("source_sha") != source_sha:
                    raise E2EError(f"selected chart {name} uses the wrong source SHA")
                if metadata.get("recipe_sha256") != expected["recipe_sha256"]:
                    raise E2EError(f"selected chart {name} recipe drifted")
                if custody == "selected-temporary":
                    archive = directory / metadata["file"]
                    checksum = metadata.get("sha256")
                else:
                    chart = recipe["chart"]
                    version = metadata.get("version")
                    archive = next(iter(sorted(directory.rglob(f"{chart}-{version}.tgz"))), Path())
                    checksum = hash_file(archive) if archive.is_file() else None
                if not archive.is_file() or hash_file(archive) != checksum:
                    raise E2EError(f"selected chart {name} archive checksum mismatch")
            else:
                archive, checksum = pull_chart(expected["reference"], runtime / "published", expected.get("checksum"))
            local_archive = runtime / archive.name
            shutil.copy2(archive, local_archive)
            record.update({"reference": expected.get("reference", archive.name), "checksum": checksum, "path": str(local_archive)})
            if name in {"control-plane-chart", "inference-gateway-chart", "iterabase-platform-chart", "cert-manager-substrate-chart"}:
                chart_archives[name] = local_archive
                chart_paths[name] = extract_chart(local_archive, runtime / "charts" / name)
            if name in TRANSITION_ENV:
                env[TRANSITION_ENV[name]] = str(local_archive)
        elif kind == "forge":
            if custody == "selected-temporary":
                if discovered is None:
                    raise E2EError("selected temporary Forge binary is missing")
                metadata, directory = discovered
                binary = directory / metadata["file"]
                checksum = metadata.get("sha256")
                if metadata.get("source_sha") != source_sha or metadata.get("recipe_sha256") != expected["recipe_sha256"]:
                    raise E2EError("temporary Forge identity does not match the plan")
            elif custody == "selected-candidate":
                forge_metadata = sorted(artifacts.rglob("candidate-forge.json"))
                if len(forge_metadata) != 1:
                    raise E2EError("selected candidate Forge metadata is missing or duplicated")
                identity = read_object(forge_metadata[0])
                if identity.get("source_sha") != source_sha or identity.get("recipe_sha256") != expected["recipe_sha256"]:
                    raise E2EError("candidate Forge source or recipe identity does not match the plan")
                candidates = sorted(artifacts.rglob("forge_*_linux_amd64.tar.gz"))
                if len(candidates) != 1:
                    raise E2EError("selected candidate Forge archive is missing or duplicated")
                archive = candidates[0]
                with tarfile.open(archive, "r:gz") as bundle:
                    members = [member for member in bundle.getmembers() if Path(member.name).name == "forge"]
                    if len(members) != 1:
                        raise E2EError("candidate Forge archive has no unique binary")
                    bundle.extract(members[0], runtime, filter="data")
                    binary = runtime / members[0].name
                checksum = hash_file(binary)
            else:
                reference = expected["reference"]
                parsed = urlparse(reference)
                if parsed.scheme != "https":
                    raise E2EError("published Forge baseline must use HTTPS")
                archive = runtime / Path(parsed.path).name
                run(["curl", "--fail", "--location", "--output", str(archive), reference])
                with tarfile.open(archive, "r:gz") as bundle:
                    members = [member for member in bundle.getmembers() if Path(member.name).name == "forge"]
                    if len(members) != 1:
                        raise E2EError("published Forge archive has no unique binary")
                    bundle.extract(members[0], runtime, filter="data")
                    binary = runtime / members[0].name
                archive_checksum = hash_file(archive)
                if expected.get("checksum") and archive_checksum != expected["checksum"]:
                    raise E2EError("published Forge archive checksum does not match the plan")
                checksum = hash_file(binary)
            final_binary = runtime / "forge"
            if binary != final_binary:
                shutil.copy2(binary, final_binary)
            final_binary.chmod(0o755)
            record.update({"reference": expected.get("reference", "forge"), "checksum": checksum, "path": str(final_binary)})
            env["FORGE_E2E_BINARY"] = str(final_binary)
        else:
            raise E2EError(f"unsupported runtime artifact kind {kind!r}")
        records.append(record)

    # Compose selected nested charts into the exact outer platform archive.
    platform = chart_paths.get("iterabase-platform-chart")
    substrate = chart_paths.get("cert-manager-substrate-chart")
    if platform:
        nested = platform / "charts"
        nested.mkdir(parents=True, exist_ok=True)
        for name in ("control-plane-chart", "inference-gateway-chart"):
            selected = chart_paths.get(name)
            if selected:
                for stale in nested.glob(selected.name + "*"):
                    if stale.is_dir():
                        shutil.rmtree(stale)
                    else:
                        stale.unlink()
                selected_archive = chart_archives[name]
                shutil.copy2(selected_archive, nested / selected_archive.name)
                set_chart_dependency_version(platform, selected.name, chart_version(selected))
        env["ITERABASE_PLATFORM_LOCAL_CHART"] = str(platform)
        env["ITERABASE_LOCAL_CHART"] = str(platform)
        platform_identity = next((item for item in scenario["artifacts"] if item["name"] == "iterabase-platform-chart"), {})
        version = platform_identity.get("version", "")
        if not version and platform_identity.get("reference"):
            _, _, version = split_chart(platform_identity["reference"])
        if version:
            env["ITERABASE_CHART_VERSION"] = str(version)
    if substrate:
        # Owners expect the companion beside the composed platform directory.
        companion = platform.parent / "cert-manager-substrate" if platform else runtime / "cert-manager-substrate"
        if companion.exists():
            shutil.rmtree(companion)
        shutil.copytree(substrate, companion)
        env["FORGE_E2E_SUBSTRATE_CHART_ARCHIVE"] = str(runtime / "cert-manager-substrate-composed.tgz")
        with tarfile.open(env["FORGE_E2E_SUBSTRATE_CHART_ARCHIVE"], "w:gz") as bundle:
            bundle.add(companion, arcname="cert-manager-substrate")
        substrate_record = next(item for item in records if item["name"] == "cert-manager-substrate-chart")
        substrate_record.update({
            "reference": substrate_record["reference"] + "#composed-runtime",
            "checksum": hash_file(Path(env["FORGE_E2E_SUBSTRATE_CHART_ARCHIVE"])),
            "path": env["FORGE_E2E_SUBSTRATE_CHART_ARCHIVE"],
        })
    if platform:
        env["FORGE_E2E_PLATFORM_CHART_ARCHIVE"] = str(runtime / "iterabase-platform-composed.tgz")
        with tarfile.open(env["FORGE_E2E_PLATFORM_CHART_ARCHIVE"], "w:gz") as bundle:
            bundle.add(platform, arcname="iterabase-platform")
        platform_record = next(item for item in records if item["name"] == "iterabase-platform-chart")
        platform_record.update({
            "reference": platform_record["reference"] + "#composed-runtime",
            "checksum": hash_file(Path(env["FORGE_E2E_PLATFORM_CHART_ARCHIVE"])),
            "path": env["FORGE_E2E_PLATFORM_CHART_ARCHIVE"],
        })

    records.sort(key=lambda item: item["name"])
    if [item["name"] for item in records] != sorted(item["name"] for item in scenario["artifacts"]):
        raise E2EError("composed runtime artifact set does not match the selected scenario")
    bundle = {
        "schema_version": RUNTIME_SCHEMA_VERSION,
        "intent": execution["intent"],
        "source_sha": source_sha,
        "plan_sha256": hash_file(plan_path),
        "catalogue_sha256": execution["catalogue_sha256"],
        "artifacts": records,
    }
    bundle_path = output / "runtime-bundle.json"
    bundle_path.write_text(json.dumps(bundle, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    env.update(
        {
            "ITERABASE_E2E_FIXTURE_MODE": "candidate" if execution["intent"] == "candidate" else "source",
            "ITERABASE_E2E_SOURCE_SHA": source_sha,
            "ITERABASE_E2E_SOURCE_DIRTY": "false",
            "ITERABASE_E2E_RUNTIME_BUNDLE": str(bundle_path),
            "ITERABASE_E2E_PLAN": str(plan_path.resolve()),
            "ITERABASE_E2E_SCENARIO_ID": scenario_id,
            "ITERABASE_E2E_REQUIRED": "true",
        }
    )
    with env_output.open("a", encoding="utf-8") as target:
        for name, value in sorted(env.items()):
            if "\n" in value:
                raise E2EError(f"environment value {name} contains a newline")
            target.write(f"{name}={value}\n")


def resolve_baselines(plan_path: Path, contract: dict[str, Any]) -> None:
    plan = read_object(plan_path)
    execution = plan.get("execution_plan", plan)
    scenarios = execution.get("scenario_matrix")
    if not isinstance(scenarios, list):
        raise E2EError("execution plan has no scenario matrix")
    resolved: dict[str, dict[str, str]] = {}
    with tempfile.TemporaryDirectory(prefix="iterabase-e2e-baselines-") as value:
        directory = Path(value)
        for scenario in scenarios:
            for artifact in scenario.get("artifacts", []):
                if artifact.get("custody") != "published-baseline":
                    continue
                name = artifact["name"]
                recipe = contract["artifact_recipes"][name]
                prior = resolved.get(name)
                if prior is not None:
                    artifact.update(prior)
                    continue
                kind = recipe["kind"]
                reference = artifact.get("reference")
                if not isinstance(reference, str):
                    raise E2EError(f"published baseline {name} has no reference")
                identity: dict[str, str]
                if kind == "image":
                    _, _, digest, _ = pull_image(reference, artifact.get("digest"))
                    identity = {"digest": digest, "reference": reference.split("@", 1)[0] + "@" + digest}
                elif kind in {"chart", "chart-companion", "published-chart"}:
                    _, checksum = pull_chart(reference, directory / name, artifact.get("checksum"))
                    identity = {"checksum": checksum}
                elif kind == "forge":
                    parsed = urlparse(reference)
                    if parsed.scheme != "https":
                        raise E2EError("published Forge baseline must use HTTPS")
                    archive = directory / Path(parsed.path).name
                    run(["curl", "--fail", "--location", "--output", str(archive), reference])
                    identity = {"checksum": hash_file(archive)}
                else:
                    raise E2EError(f"unsupported published baseline kind {kind!r}")
                artifact.update(identity)
                resolved[name] = identity
    baseline_images: list[dict[str, Any]] = []
    baseline_charts: list[dict[str, Any]] = []
    baseline_forge: list[dict[str, Any]] = []
    transition_charts: list[dict[str, Any]] = []
    for name, identity in sorted(resolved.items()):
        recipe = contract["artifact_recipes"][name]
        reference = next(
            artifact["reference"]
            for scenario in scenarios
            for artifact in scenario["artifacts"]
            if artifact["name"] == name
        )
        if recipe["kind"] == "image":
            repository, version = split_image(reference)
            baseline_images.append(
                {
                    "name": recipe["name"], "artifact": name,
                    "target": recipe.get("target", ""), "repository": repository,
                    "version": version, "digest": identity["digest"],
                    "immutable_reference": reference,
                }
            )
        elif recipe["kind"] in {"chart", "chart-companion", "published-chart"}:
            repository, chart, version = split_chart(reference)
            item = {
                "name": name, "chart": chart, "repository": repository,
                "version": version, "sha256": identity["checksum"],
            }
            if name in TRANSITION_ENV:
                transition_charts.append(item)
            else:
                baseline_charts.append(item)
        elif recipe["kind"] == "forge":
            baseline_forge.append(
                {"name": name, "reference": reference, "sha256": identity["checksum"]}
            )
    plan["baseline_dependencies"] = {
        "images": baseline_images, "charts": baseline_charts, "forge": baseline_forge,
    }
    plan["transition_baselines"] = {"charts": transition_charts}
    plan_path.write_text(compact(plan) + "\n", encoding="utf-8")


def validate_result(result: dict[str, Any], scenario: dict[str, Any], execution: dict[str, Any], plan_sha: str) -> None:
    scenario_id = scenario["id"]
    if result.get("schema_version") != RESULT_SCHEMA_VERSION or result.get("scenario_id") != scenario_id:
        raise E2EError(f"result for {scenario_id} has invalid schema or identity")
    if result.get("status") != "passed":
        raise E2EError(f"result for {scenario_id} is {result.get('status')!r}")
    expected_fields = {
        "source_sha": execution["source_sha"],
        "plan_sha256": plan_sha,
        "catalogue_sha256": execution["catalogue_sha256"],
        "stage_graph_sha256": scenario["stage_graph_sha256"],
        "fixture_mode": scenario["fixture_mode"],
    }
    for field, value in expected_fields.items():
        if result.get(field) != value:
            raise E2EError(f"result for {scenario_id} has wrong {field}")
    if not SHA256.fullmatch(str(result.get("runtime_bundle_sha256", ""))):
        raise E2EError(f"result for {scenario_id} has no runtime bundle identity")
    stages = result.get("stages")
    expected_stages = scenario["stages"]
    if not isinstance(stages, list) or len(stages) != len(expected_stages):
        raise E2EError(f"result for {scenario_id} has missing or extra stages")
    for actual, expected in zip(stages, expected_stages, strict=True):
        if actual.get("name") != expected.get("name") or actual.get("depends_on", []) != expected.get("depends_on", []) or actual.get("status") != "passed":
            raise E2EError(f"result for {scenario_id} has non-terminal or mismatched stage evidence: {actual}")
    artifacts = result.get("artifacts")
    if not isinstance(artifacts, list):
        raise E2EError(f"result for {scenario_id} has no artifact identities")
    expected_artifacts = {item["name"]: item for item in scenario["artifacts"]}
    actual_artifacts = {item.get("name"): item for item in artifacts if isinstance(item, dict)}
    if set(actual_artifacts) != set(expected_artifacts) or len(actual_artifacts) != len(artifacts):
        raise E2EError(f"result for {scenario_id} has missing, extra, or duplicate artifacts")
    for name, expected in expected_artifacts.items():
        actual = actual_artifacts[name]
        if actual.get("custody") != expected["custody"] or actual.get("recipe_sha256") != expected["recipe_sha256"]:
            raise E2EError(f"result for {scenario_id} has wrong identity for {name}")
        if expected["custody"] != "published-baseline" and actual.get("source_sha") != execution["source_sha"]:
            raise E2EError(f"result for {scenario_id} substitutes a baseline for selected {name}")
        if expected["custody"] == "published-baseline" and actual.get("source_sha"):
            raise E2EError(f"result for {scenario_id} gives baseline {name} selected-source custody")
        if expected["kind"] == "image" and not SHA256.fullmatch(str(actual.get("digest", ""))):
            raise E2EError(f"result for {scenario_id} has incomplete image digest for {name}")
        if expected["kind"] != "image" and not SHA256.fullmatch(str(actual.get("checksum", ""))):
            raise E2EError(f"result for {scenario_id} has incomplete checksum for {name}")


def validate_results(plan_path: Path, results_dir: Path, needs: dict[str, Any] | None = None) -> list[dict[str, Any]]:
    plan = read_object(plan_path)
    execution = plan.get("execution_plan", plan)
    scenarios = execution.get("scenario_matrix")
    if not isinstance(scenarios, list):
        raise E2EError("execution plan has no scenario matrix")
    expected = {scenario["id"]: scenario for scenario in scenarios}
    discovered: dict[str, dict[str, Any]] = {}
    for path in sorted(results_dir.rglob("*.json")) if results_dir.exists() else []:
        value = read_object(path)
        if value.get("schema_version") != RESULT_SCHEMA_VERSION or "scenario_id" not in value:
            continue
        scenario_id = value["scenario_id"]
        if scenario_id in discovered:
            raise E2EError(f"result for {scenario_id} is duplicated")
        discovered[scenario_id] = value
    if set(discovered) != set(expected):
        raise E2EError(
            "result artifact set does not match the generated plan: "
            + compact({"missing": sorted(set(expected) - set(discovered)), "extra": sorted(set(discovered) - set(expected))})
        )
    plan_sha = hash_file(plan_path)
    for scenario_id in sorted(expected):
        validate_result(discovered[scenario_id], expected[scenario_id], execution, plan_sha)
    if needs is not None:
        for name, job in needs.items():
            result = job.get("result") if isinstance(job, dict) else None
            if result not in {"success", "skipped"}:
                raise E2EError(f"required workflow job {name} is {result!r}")
        selected_jobs = {
            "artifacts": bool(execution.get("artifact_build_matrix")),
            "kind": bool(execution.get("kind_matrix")),
            "real-machine": bool(execution.get("real_machine_matrix")),
        }
        for name, selected in selected_jobs.items():
            if selected and (not isinstance(needs.get(name), dict) or needs[name].get("result") != "success"):
                raise E2EError(f"selected workflow job {name} did not succeed")
    return [discovered[name] for name in sorted(discovered)]


def parse_targets(value: str) -> list[str]:
    values = [item.strip() for item in value.split(",")]
    if not values or any(not item for item in values) or len(values) != len(set(values)):
        raise E2EError("candidate targets must be a non-empty, duplicate-free comma-separated set")
    return values


def parser() -> argparse.ArgumentParser:
    value = argparse.ArgumentParser(description=__doc__)
    commands = value.add_subparsers(dest="command", required=True)
    commands.add_parser("validate-contract")
    plan = commands.add_parser("plan")
    plan.add_argument("--intent", choices=sorted(INTENTS), required=True)
    plan.add_argument("--source-sha", required=True)
    plan.add_argument("--paths-file", type=Path)
    plan.add_argument("--targets", default="")
    plan.add_argument("--all", action="store_true", dest="select_all")
    plan.add_argument("--output", type=Path, required=True)
    plan.add_argument("--github-output", type=Path)
    resolve = commands.add_parser("resolve-baselines")
    resolve.add_argument("--plan", type=Path, required=True)
    build = commands.add_parser("build-artifact")
    build.add_argument("--plan", type=Path, required=True)
    build.add_argument("--artifact", required=True)
    build.add_argument("--output", type=Path, required=True)
    compose = commands.add_parser("compose")
    compose.add_argument("--plan", type=Path, required=True)
    compose.add_argument("--scenario", required=True)
    compose.add_argument("--artifacts", type=Path, required=True)
    compose.add_argument("--output", type=Path, required=True)
    compose.add_argument("--env-output", type=Path, required=True)
    validate = commands.add_parser("validate-results")
    validate.add_argument("--plan", type=Path, required=True)
    validate.add_argument("--results", type=Path, required=True)
    validate.add_argument("--needs-env", default="")
    return value


def main() -> int:
    args = parser().parse_args()
    root = Path(__file__).resolve().parents[2]
    try:
        contract = load_contract(root)
        if args.command == "validate-contract":
            catalogue = load_catalogue(root)
            validate_catalogue_contract(catalogue, contract)
            print("E2E plan, recipe, runtime, and result contract valid")
        elif args.command == "plan":
            paths = args.paths_file.read_text(encoding="utf-8").splitlines() if args.paths_file else []
            targets = parse_targets(args.targets) if args.targets else []
            plan = make_plan(
                root,
                load_catalogue(root),
                contract,
                intent=args.intent,
                source_sha=args.source_sha,
                paths=paths,
                targets=targets,
                select_all=args.select_all,
            )
            args.output.write_text(compact(plan) + "\n", encoding="utf-8")
            output = args.github_output or (Path(os.environ["GITHUB_OUTPUT"]) if os.environ.get("GITHUB_OUTPUT") else None)
            if output:
                write_outputs(output, plan)
            print(compact({"scenario_total": plan["scenario_total"], "owner_totals": plan["owner_totals"]}))
        elif args.command == "resolve-baselines":
            resolve_baselines(args.plan, contract)
        elif args.command == "build-artifact":
            build_artifact(root, read_object(args.plan), contract, args.artifact, args.output)
        elif args.command == "compose":
            compose_runtime(args.plan, args.scenario, args.artifacts, args.output, args.env_output, root, contract)
        elif args.command == "validate-results":
            needs = None
            if args.needs_env:
                try:
                    needs = json.loads(os.environ.get(args.needs_env, ""))
                except json.JSONDecodeError as exc:
                    raise E2EError(f"{args.needs_env} is not a needs object: {exc}") from exc
                if not isinstance(needs, dict):
                    raise E2EError(f"{args.needs_env} is not a needs object")
            results = validate_results(args.plan, args.results, needs)
            print(compact({"validated_scenarios": [result["scenario_id"] for result in results]}))
    except E2EError as exc:
        print(f"E2E contract error: {exc}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
