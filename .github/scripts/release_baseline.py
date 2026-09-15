#!/usr/bin/env python3
"""Resolve, validate, and protect one complete repository release baseline snapshot."""

from __future__ import annotations

import argparse
import copy
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile
from typing import Any

SCHEMA_VERSION = 1
REPOSITORY = "nunocgoncalves/iterabase-mono"
TARGET_ORDER = (
    "control-plane",
    "inference-gateway",
    "forge",
    "control-plane-chart",
    "inference-gateway-chart",
    "iterabase-platform-chart",
)
FORGE_PLATFORMS = ("linux_amd64", "linux_arm64", "darwin_amd64", "darwin_arm64")
FIXTURE_ORDER = (
    "certificate-migration-chart",
    "supported-platform-predecessor",
    "supported-substrate-predecessor",
    "metallb-platform-predecessor",
    "metallb-substrate-predecessor",
)
SHA = re.compile(r"^[0-9a-f]{40}$")
SHA256 = re.compile(r"^[0-9a-f]{64}$")
QUALIFIED_SHA256 = re.compile(r"^sha256:[0-9a-f]{64}$")
SEMVER = re.compile(r"^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$")
POSITIVE_INTEGER = re.compile(r"^[1-9][0-9]*$")

BOOTSTRAP_SOURCE = "b4b32b14d6ab89a85db24fc198879fdfe9621d2a"
BOOTSTRAP_RUN_ID = "34541902001"
BOOTSTRAP_RUN_ATTEMPT = "1"
BOOTSTRAP_PLAN_SHA256 = "c7f460fae540f17bfe06198bfaac9621f08260b6ba57744dc54b23d89b420746"
BOOTSTRAP_EVIDENCE_SHA256 = "c0bcd898dd9d1501b715f50c75508f0cf5049a4369a2eaf7c46b4644b806cbf3"
BOOTSTRAP_ANCHOR_ID = 386705918
BOOTSTRAP_SELECTED_TARGETS = (
    "control-plane",
    "forge",
    "control-plane-chart",
    "iterabase-platform-chart",
)
BOOTSTRAP_RELEASES: dict[str, dict[str, Any]] = {
    "control-plane": {
        "id": 386705747,
        "tag": "control-plane-v0.0.33",
        "version": "0.0.33",
        "manifest_id": 556111914,
        "manifest_sha256": "e63c2f2ed7e8cba40baf63edc7cd7ccbcbab3da7c9413754d7211af0a6e3f73e",
        "manifest_size": 2114,
        "tag_object_sha": "5e08589abe7b94b177128cd43037846a64e70045",
    },
    "forge": {
        "id": 386705816,
        "tag": "forge-v0.9.0",
        "version": "0.9.0",
        "manifest_id": 556112306,
        "manifest_sha256": "af086b171c6f3837c282120ac9e4a827791aa006697f1f019aa98f56fe3e33bb",
        "manifest_size": 2760,
        "tag_object_sha": "31f3b9cca550764009b54ba4c4b23bd12c7c0ed7",
    },
    "control-plane-chart": {
        "id": 386705691,
        "tag": "control-plane-0.5.0",
        "version": "0.5.0",
        "manifest_id": 556111416,
        "manifest_sha256": "c88e5c0a1dbca7f50ef4e640659963b42bc003bc36425e8f60053802e43ade9d",
        "manifest_size": 1663,
        "tag_object_sha": "d769404f97bb3a531cd258a00d66a6eff4a1e6a5",
    },
    "iterabase-platform-chart": {
        "id": BOOTSTRAP_ANCHOR_ID,
        "tag": "iterabase-platform-0.4.0",
        "version": "0.4.0",
        "manifest_id": 556112994,
        "manifest_sha256": "aaf2dbb7a2a6025abd58cf47eca8425ec979de5e2f3dda55f337b6915412d945",
        "manifest_size": 2103,
        "tag_object_sha": "b52bfcca53afdee4983dcf658df36d9ff0d7b7cb",
    },
}
BOOTSTRAP_CHART_IDENTITIES: dict[str, tuple[str, str, int, str]] = {
    "control-plane-chart": ("control-plane", "0.5.0", 62580, "9438dcbb049e95db60c06469d3f0a48e4b796d696daffb2cee773aed3ef95e4c"),
    "inference-gateway-chart": ("inference-gateway", "0.2.13", 5051, "358f66e86cf58c10c66fe6663561f07e1b4241d706f04765dc728c3b51b502c5"),
    "iterabase-platform-chart": ("iterabase-platform", "0.4.0", 1342241, "9b2fb4e8f44f305b0fbc009a17764704ef7dd70dcd994d9e64f1517d68a0e413"),
    "cert-manager-substrate-chart": ("cert-manager-substrate", "0.4.0", 167234, "3dcae97684d7fe1e67357572165a8605078d7af46405d518a99353e8a5de30ed"),
    "lvm-storage-substrate-chart": ("lvm-storage-substrate", "0.4.0", 14569, "6b1233c2c27cab8597f0a1b77de1d445f5a77ea696df75d30de7bc1747eda7dd"),
}
BOOTSTRAP_CHART_OCI_DIGESTS = {
    "control-plane-chart": "sha256:8de565055d14abb5cb98524af9bc8d11354f725aab8220901fc953b1986e3c16",
    "inference-gateway-chart": "sha256:3f032d285500efcaa5508d42a2800410f92dd0d0c9e9c19e00ea375d772198fd",
    "iterabase-platform-chart": "sha256:d6e4666ba5cc795d6c721ce83e194720943c9b30bbe26aaa6f82c0657ad63be6",
    "cert-manager-substrate-chart": "sha256:821544b95f55e042727afc6b1344d981157c66bad84096b4a7f710758f2feabd",
    "lvm-storage-substrate-chart": "sha256:38ba73a76dc2b913d6b32a9b4b85b81b0e9770630513f2477e3e20698c2b68f4",
}
BOOTSTRAP_FIXTURES: dict[str, tuple[str, str, int, str, str]] = {
    "certificate-migration-chart": ("iterabase-platform", "0.2.2", 1466733, "e7eb91720259785be130f6ce6167d274de98ed474abce1bcf7fff9301d0e5690", "sha256:9e01b9f183220c724172643234b6b1be02d1271f108a9a1d530e1c6b62f7342f"),
    "supported-platform-predecessor": ("iterabase-platform", "0.3.12", 1319770, "86b0f23012fb549e47b2afb19b16184c760adcd6fe930bc3d817e031f1d280dd", "sha256:c105927e20d47e2cae5bbc00c4b0da9b2ac99c6f416e58f3c5e8927beeccf39e"),
    "supported-substrate-predecessor": ("cert-manager-substrate", "0.3.12", 164198, "979eb26fbb5fa08a345929c59f5379b03f053f3ba0a66169bf2e84c5c919a67b", "sha256:80b11be91d77647223ccf139d46ebff3839a2822201ac141498d404ac2dbc67c"),
    "metallb-platform-predecessor": ("iterabase-platform", "0.3.19", 1327177, "252e5fea513c998eb04cfa75408ebc6d216831778cb96dfe23176ede5f6e76ff", "sha256:a3e9a32ed5277386b52b5bfab12275608018b8680e35ae768b51a6b886eed5f9"),
    "metallb-substrate-predecessor": ("cert-manager-substrate", "0.3.19", 164199, "437e5ab1e244261e7011fe369e7a59323ed628588e07e2dc5204531e867a5481", "sha256:d158cc5ca9ef8b2e5caf713c254bc9ce421050cd37cf794e8bfcb8bdd22d728f"),
}


class BaselineError(ValueError):
    """The selected release baseline is absent, mutable, or inconsistent."""


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


def envelope(snapshot: dict[str, Any]) -> dict[str, Any]:
    return {
        "schema_version": SCHEMA_VERSION,
        "snapshot_sha256": hash_bytes(compact(snapshot).encode("utf-8")),
        "snapshot": snapshot,
    }


def load_envelope(path: Path) -> dict[str, Any]:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        raise BaselineError(f"cannot read baseline snapshot {path}: {exc}") from exc
    if not isinstance(value, dict):
        raise BaselineError("baseline snapshot envelope must be an object")
    return value


def deterministic_anchor(selected_targets: list[str] | tuple[str, ...]) -> str:
    selected = list(selected_targets)
    if not selected or len(selected) != len(set(selected)) or any(item not in TARGET_ORDER for item in selected):
        raise BaselineError("snapshot selected_targets must be non-empty, unique, and known")
    canonical = [target for target in TARGET_ORDER if target in selected]
    if canonical != selected:
        raise BaselineError("snapshot selected_targets are not in repository order")
    return canonical[-1]


def _target_artifacts(contract: dict[str, Any]) -> dict[str, list[str]]:
    return {
        target: list(contract["targets"][target]["artifacts"])
        for target in TARGET_ORDER
    }


def _artifact_index(value: dict[str, Any]) -> dict[str, dict[str, Any]]:
    result: dict[str, dict[str, Any]] = {}
    for cohort in value["snapshot"]["targets"]:
        for artifact in cohort["artifacts"]:
            name = artifact["name"]
            if name in result:
                raise BaselineError(f"snapshot artifact {name} is duplicated")
            result[name] = artifact
    for fixture in value["snapshot"]["fixtures"]:
        name = fixture["name"]
        if name in result:
            raise BaselineError(f"snapshot fixture {name} is duplicated")
        result[name] = fixture
    return result


def validate_snapshot(value: dict[str, Any], contract: dict[str, Any]) -> None:
    if value.get("schema_version") != SCHEMA_VERSION or not isinstance(value.get("snapshot"), dict):
        raise BaselineError("baseline snapshot must use a schema-v1 envelope")
    snapshot = value["snapshot"]
    if value.get("snapshot_sha256") != hash_bytes(compact(snapshot).encode("utf-8")):
        raise BaselineError("baseline snapshot canonical hash does not match its bytes")
    if snapshot.get("schema_version") != SCHEMA_VERSION or snapshot.get("repository") != REPOSITORY:
        raise BaselineError("baseline snapshot repository or schema is invalid")
    if snapshot.get("generator") != {"name": "iterabase-release-baseline", "version": 1}:
        raise BaselineError("baseline snapshot generator identity is invalid")
    mode = snapshot.get("mode")
    if mode not in {"published", "candidate", "planned-final"}:
        raise BaselineError("baseline snapshot mode is invalid")
    selected = snapshot.get("selected_targets")
    if not isinstance(selected, list) or snapshot.get("anchor_target") != deterministic_anchor(selected):
        raise BaselineError("baseline snapshot deterministic anchor is invalid")
    candidate = snapshot.get("candidate")
    if not isinstance(candidate, dict):
        raise BaselineError("baseline snapshot has no candidate identity")
    for field in ("run_id", "run_attempt"):
        if not POSITIVE_INTEGER.fullmatch(str(candidate.get(field, ""))):
            raise BaselineError(f"baseline snapshot candidate {field} is invalid")
    for field in ("source_sha", "control_sha"):
        if not SHA.fullmatch(str(candidate.get(field, ""))):
            raise BaselineError(f"baseline snapshot candidate {field} is invalid")
    parent = snapshot.get("parent_anchor")
    if parent is not None and (
        not isinstance(parent, dict)
        or not POSITIVE_INTEGER.fullmatch(str(parent.get("release_id", "")))
        or not isinstance(parent.get("tag"), str)
        or not SHA256.fullmatch(str(parent.get("snapshot_sha256", "")))
    ):
        raise BaselineError("baseline snapshot parent anchor is invalid")
    anchor = snapshot.get("anchor")
    if not isinstance(anchor, dict) or anchor.get("target") != snapshot["anchor_target"]:
        raise BaselineError("baseline snapshot anchor pin is invalid")
    if mode == "published" and not POSITIVE_INTEGER.fullmatch(str(anchor.get("release_id", ""))):
        raise BaselineError("published baseline snapshot has no exact anchor Release ID")

    targets = snapshot.get("targets")
    if not isinstance(targets, list) or [item.get("target") for item in targets if isinstance(item, dict)] != list(TARGET_ORDER):
        raise BaselineError("baseline snapshot must contain all six targets in repository order")
    expected_artifacts = _target_artifacts(contract)
    for cohort in targets:
        target = cohort["target"]
        if not SEMVER.fullmatch(str(cohort.get("version", ""))):
            raise BaselineError(f"baseline target {target} has no stable semantic version")
        provenance = cohort.get("provenance")
        if provenance not in {"promoted", "bootstrap-promoted", "bootstrap-inherited-and-attested", "selected-candidate", "planned-final"}:
            raise BaselineError(f"baseline target {target} has invalid provenance")
        source = cohort.get("source_sha")
        if source is not None and not SHA.fullmatch(str(source)):
            raise BaselineError(f"baseline target {target} has invalid source SHA")
        if target in selected:
            expected_provenance = {
                "published": {"promoted", "bootstrap-promoted"},
                "candidate": {"selected-candidate"},
                "planned-final": {"planned-final"},
            }[mode]
            if (
                provenance not in expected_provenance
                or source != candidate["source_sha"]
                or str(cohort.get("candidate_run_id")) != str(candidate["run_id"])
                or str(cohort.get("candidate_run_attempt")) != str(candidate["run_attempt"])
            ):
                raise BaselineError(f"selected target {target} disagrees with its candidate cohort")
        release = cohort.get("release")
        if provenance != "bootstrap-inherited-and-attested":
            if not isinstance(release, dict) or not isinstance(release.get("tag"), str):
                raise BaselineError(f"baseline target {target} has no Release pin")
            if mode == "published" and not POSITIVE_INTEGER.fullmatch(str(release.get("id", ""))):
                raise BaselineError(f"published baseline target {target} has no exact Release ID")
            if mode == "published":
                tag_object = release.get("tag_object")
                if (
                    not isinstance(tag_object, dict)
                    or not SHA.fullmatch(str(tag_object.get("sha", "")))
                    or (source is not None and tag_object.get("target_sha") != source)
                ):
                    raise BaselineError(f"published baseline target {target} has no exact annotated tag object")
        artifacts = cohort.get("artifacts")
        if not isinstance(artifacts, list) or [item.get("name") for item in artifacts if isinstance(item, dict)] != expected_artifacts[target]:
            raise BaselineError(f"baseline target {target} artifact membership is incomplete or reordered")
        for artifact in artifacts:
            _validate_artifact(artifact, contract, cohort, mode, set(selected))

    anchor_cohort = next(item for item in targets if item["target"] == snapshot["anchor_target"])
    if (
        anchor.get("source_sha") != candidate["source_sha"]
        or anchor.get("tag") != anchor_cohort["release"]["tag"]
        or (mode == "published" and anchor.get("release_id") != anchor_cohort["release"]["id"])
    ):
        raise BaselineError("baseline snapshot anchor disagrees with its deterministic target cohort")

    fixtures = snapshot.get("fixtures")
    if not isinstance(fixtures, list) or [item.get("name") for item in fixtures if isinstance(item, dict)] != list(FIXTURE_ORDER):
        raise BaselineError("baseline snapshot fixture membership is incomplete or reordered")
    for fixture in fixtures:
        _validate_chart_identity(fixture, fixture["name"], fixture.get("version"), mode, fixture=True)
    _artifact_index(value)


def _validate_artifact(
    artifact: dict[str, Any],
    contract: dict[str, Any],
    cohort: dict[str, Any],
    mode: str,
    selected_targets: set[str],
) -> None:
    name = artifact["name"]
    recipe = contract["artifact_recipes"].get(name)
    if not isinstance(recipe, dict) or artifact.get("kind") != recipe.get("kind"):
        raise BaselineError(f"baseline artifact {name} kind disagrees with its recipe")
    if artifact.get("target") != cohort["target"] or artifact.get("version") != cohort["version"]:
        raise BaselineError(f"baseline artifact {name} disagrees with its target cohort")
    producer = artifact.get("producer_recipe_sha256")
    if producer is not None and not SHA256.fullmatch(str(producer)):
        raise BaselineError(f"baseline artifact {name} producer recipe identity is invalid")
    custody = artifact.get("custody")
    if mode == "published" and custody != "published-baseline":
        raise BaselineError(f"published snapshot artifact {name} has non-published custody")
    if mode != "published" and cohort["target"] in selected_targets:
        if custody not in {"selected-candidate", "planned-final"}:
            raise BaselineError(f"selected snapshot artifact {name} has invalid custody")
    kind = artifact["kind"]
    if kind == "image":
        reference = artifact.get("reference")
        repository = recipe.get("repository")
        digest = artifact.get("digest")
        if not isinstance(reference, str) or not isinstance(repository, str) or not QUALIFIED_SHA256.fullmatch(str(digest)):
            raise BaselineError(f"baseline image {name} has incomplete immutable identity")
        if not reference.startswith(repository + ":") or not reference.endswith("@" + digest):
            raise BaselineError(f"baseline image {name} uses an unauthorized or mutable reference")
    elif kind in {"chart", "chart-companion"}:
        _validate_chart_identity(artifact, name, cohort["version"], mode)
    elif kind == "forge":
        variants = artifact.get("variants")
        if not isinstance(variants, list) or [item.get("platform") for item in variants if isinstance(item, dict)] != list(FORGE_PLATFORMS):
            raise BaselineError("Forge baseline does not contain all four ordered variants")
        for item in variants:
            if not isinstance(item.get("filename"), str) or not item["filename"].endswith(f"_{item['platform']}.tar.gz"):
                raise BaselineError("Forge baseline variant filename is invalid")
            if not isinstance(item.get("size"), int) or item["size"] <= 0 or not SHA256.fullmatch(str(item.get("sha256", ""))):
                raise BaselineError("Forge baseline variant content identity is invalid")
            expected_url = (
                f"https://github.com/{REPOSITORY}/releases/download/"
                f"{cohort['release']['tag']}/{item['filename']}"
            )
            if custody == "published-baseline":
                if item.get("url") != expected_url or "candidate_path" in item:
                    raise BaselineError("published Forge variant uses an unauthorized Release URL or candidate path")
            elif (
                item.get("candidate_path") != f"assets/forge/{item['filename']}"
                or "url" in item
            ):
                raise BaselineError("candidate Forge variant has invalid local custody")
    else:
        raise BaselineError(f"published snapshot contains unsupported artifact kind {kind!r}")


def _validate_chart_identity(
    artifact: dict[str, Any], name: str, version: Any, mode: str, *, fixture: bool = False
) -> None:
    if not SEMVER.fullmatch(str(version or "")):
        raise BaselineError(f"baseline chart {name} has no semantic version")
    chart = artifact.get("chart")
    repository = f"oci://ghcr.io/nunocgoncalves/iterabase-charts/{chart}"
    expected_reference = f"{repository}:{version}"
    reference = artifact.get("reference") or artifact.get("planned_reference")
    if reference != expected_reference:
        raise BaselineError(f"baseline chart {name} uses an unauthorized reference")
    filename = f"{chart}-{version}.tgz"
    if artifact.get("filename") != filename or not isinstance(artifact.get("size"), int) or artifact["size"] <= 0:
        raise BaselineError(f"baseline chart {name} has invalid archive filename or size")
    if not SHA256.fullmatch(str(artifact.get("sha256", ""))):
        raise BaselineError(f"baseline chart {name} has no archive checksum")
    digest = artifact.get("oci_digest")
    if mode == "published" or fixture:
        if not QUALIFIED_SHA256.fullmatch(str(digest or "")):
            raise BaselineError(f"baseline chart {name} has no exact OCI digest")
    elif digest is not None and not QUALIFIED_SHA256.fullmatch(str(digest)):
        raise BaselineError(f"candidate chart {name} has an invalid OCI digest")


def artifact_map(value: dict[str, Any], contract: dict[str, Any]) -> dict[str, dict[str, Any]]:
    validate_snapshot(value, contract)
    return _artifact_index(value)


class GitHubBackend:
    def __init__(self, repository: str = REPOSITORY) -> None:
        if repository != REPOSITORY:
            raise BaselineError(f"baseline repository must be {REPOSITORY}")
        self.repository = repository
        self.latest_calls = 0

    def _run(self, command: list[str], *, binary: bool = False) -> bytes | str:
        try:
            result = subprocess.run(command, check=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        except (OSError, subprocess.CalledProcessError) as exc:
            stderr = getattr(exc, "stderr", b"") or b""
            raise BaselineError(f"baseline command failed: {' '.join(command)}\n{stderr.decode(errors='replace')}") from exc
        return result.stdout if binary else result.stdout.decode().strip()

    def api(self, path: str) -> Any:
        raw = self._run(["gh", "api", f"repos/{self.repository}/{path}"])
        try:
            return json.loads(str(raw))
        except json.JSONDecodeError as exc:
            raise BaselineError(f"GitHub API {path} did not return JSON") from exc

    def latest(self) -> dict[str, Any]:
        self.latest_calls += 1
        if self.latest_calls != 1:
            raise BaselineError("GitHub Latest was resolved more than once")
        value = self.api("releases/latest")
        if not isinstance(value, dict):
            raise BaselineError("GitHub Latest is not a Release object")
        return value

    def reread_latest_before_mutation(self) -> dict[str, Any]:
        value = self.api("releases/latest")
        if not isinstance(value, dict):
            raise BaselineError("GitHub Latest pre-mutation response is not a Release object")
        return value

    def reread_latest_after_mutation(self) -> dict[str, Any]:
        value = self.api("releases/latest")
        if not isinstance(value, dict):
            raise BaselineError("GitHub Latest post-mutation response is not a Release object")
        return value

    def set_latest(self, release_id: int) -> None:
        self._run(
            [
                "gh", "api", "--method", "PATCH",
                f"repos/{self.repository}/releases/{release_id}",
                "-f", "make_latest=true",
            ]
        )

    def release(self, release_id: int) -> dict[str, Any]:
        value = self.api(f"releases/{release_id}")
        if not isinstance(value, dict):
            raise BaselineError(f"GitHub Release {release_id} is not an object")
        return value

    def tag(self, name: str) -> dict[str, str]:
        ref = self.api(f"git/ref/tags/{name}")
        obj = ref.get("object") if isinstance(ref, dict) else None
        if not isinstance(obj, dict) or obj.get("type") != "tag" or not SHA.fullmatch(str(obj.get("sha", ""))):
            raise BaselineError(f"release tag {name} is not one exact annotated tag object")
        tag = self.api(f"git/tags/{obj['sha']}")
        target = tag.get("object") if isinstance(tag, dict) else None
        if not isinstance(target, dict) or target.get("type") != "commit" or not SHA.fullmatch(str(target.get("sha", ""))):
            raise BaselineError(f"release tag {name} does not target one exact commit")
        return {"object_sha": obj["sha"], "target_sha": target["sha"]}

    def download_asset(self, asset_id: int) -> bytes:
        value = self._run(
            ["gh", "api", "-H", "Accept: application/octet-stream", f"repos/{self.repository}/releases/assets/{asset_id}"],
            binary=True,
        )
        assert isinstance(value, bytes)
        return value

    def _attestation(self, command: list[str]) -> dict[str, Any]:
        raw = self._run(command)
        try:
            value = json.loads(str(raw))
        except json.JSONDecodeError as exc:
            raise BaselineError("GitHub Release attestation output is not JSON") from exc
        if not isinstance(value, dict):
            raise BaselineError("GitHub Release attestation output is not an object")
        return value

    def release_attestation(self, tag: str) -> dict[str, Any]:
        return self._attestation(["gh", "release", "verify", tag, "--repo", self.repository, "--format", "json"])

    def asset_attestation(self, tag: str, path: Path) -> dict[str, Any]:
        return self._attestation(["gh", "release", "verify-asset", tag, str(path), "--repo", self.repository, "--format", "json"])

    def image_digest(self, reference: str) -> str:
        raw = self._run(["docker", "buildx", "imagetools", "inspect", reference, "--format", "{{json .Manifest.Digest}}"])
        try:
            value = json.loads(str(raw))
        except json.JSONDecodeError as exc:
            raise BaselineError(f"registry identity for {reference} is malformed") from exc
        if not isinstance(value, str) or not QUALIFIED_SHA256.fullmatch(value):
            raise BaselineError(f"registry identity for {reference} is not an exact digest")
        return value

    def chart(self, reference: str) -> tuple[str, bytes]:
        oci_reference, version = reference.rsplit(":", 1)
        chart = oci_reference.rsplit("/", 1)[-1]
        before_digest = self.image_digest(reference.removeprefix("oci://"))
        with tempfile.TemporaryDirectory(prefix="iterabase-baseline-chart-") as value:
            directory = Path(value)
            self._run(["helm", "pull", oci_reference, "--version", version, "--destination", str(directory)])
            path = directory / f"{chart}-{version}.tgz"
            try:
                data = path.read_bytes()
            except OSError as exc:
                raise BaselineError(f"Helm did not retrieve exact chart {reference}") from exc
        after_digest = self.image_digest(reference.removeprefix("oci://"))
        if after_digest != before_digest:
            raise BaselineError(f"chart {reference} OCI identity moved during retrieval")
        return after_digest, data


def _assets_by_name(release: dict[str, Any]) -> dict[str, dict[str, Any]]:
    assets = release.get("assets")
    if not isinstance(assets, list) or any(not isinstance(item, dict) for item in assets):
        raise BaselineError("Release assets are malformed")
    result = {item.get("name"): item for item in assets}
    if len(result) != len(assets) or any(not isinstance(name, str) for name in result):
        raise BaselineError("Release assets are missing names or duplicated")
    return result  # type: ignore[return-value]


def _asset_digest(asset: dict[str, Any]) -> str:
    digest = asset.get("digest")
    if not isinstance(digest, str) or not QUALIFIED_SHA256.fullmatch(digest):
        raise BaselineError(f"Release asset {asset.get('name')} has no immutable digest")
    return digest.removeprefix("sha256:")


def _validate_attestation(
    value: dict[str, Any], release: dict[str, Any], tag_identity: dict[str, str], assets: dict[str, dict[str, Any]]
) -> None:
    result = value.get("verificationResult")
    statement = result.get("statement") if isinstance(result, dict) else None
    predicate = statement.get("predicate") if isinstance(statement, dict) else None
    subjects = statement.get("subject") if isinstance(statement, dict) else None
    if not isinstance(predicate, dict) or not isinstance(subjects, list):
        raise BaselineError("Release attestation has no verified statement")
    if (
        predicate.get("repository") != REPOSITORY
        or str(predicate.get("databaseId")) != str(release["id"])
        or predicate.get("tag") != release["tag_name"]
    ):
        raise BaselineError("Release attestation predicate disagrees with the exact Release")
    package = [item for item in subjects if isinstance(item, dict) and "uri" in item]
    members = {
        item.get("name"): item.get("digest", {}).get("sha256")
        for item in subjects
        if isinstance(item, dict) and "name" in item and isinstance(item.get("digest"), dict)
    }
    if (
        len(package) != 1
        or package[0].get("digest", {}).get("sha1") != tag_identity["object_sha"]
        or members != {name: _asset_digest(asset) for name, asset in assets.items()}
    ):
        raise BaselineError("Release attestation subjects do not cover the exact tag and complete asset set")


def _verify_release(
    backend: GitHubBackend,
    spec: dict[str, Any],
    target: str,
    *,
    captured: dict[str, Any] | None = None,
) -> tuple[dict[str, Any], dict[str, bytes], dict[str, Any]]:
    release = captured if captured is not None else backend.release(spec["id"])
    if (
        release.get("id") != spec["id"]
        or release.get("tag_name") != spec["tag"]
        or release.get("target_commitish") != BOOTSTRAP_SOURCE
        or release.get("draft") is not False
        or release.get("prerelease") is not False
        or release.get("immutable") is not True
    ):
        raise BaselineError(f"bootstrap Release {target} drifted from its exact immutable identity")
    assets = _assets_by_name(release)
    manifest_name = f"release-manifest-{target}.json"
    manifest_asset = assets.get(manifest_name)
    if (
        manifest_asset is None
        or manifest_asset.get("id") != spec["manifest_id"]
        or manifest_asset.get("size") != spec["manifest_size"]
        or _asset_digest(manifest_asset) != spec["manifest_sha256"]
    ):
        raise BaselineError(f"bootstrap Release {target} manifest identity drifted")
    tag_identity = backend.tag(spec["tag"])
    if tag_identity != {"object_sha": spec["tag_object_sha"], "target_sha": BOOTSTRAP_SOURCE}:
        raise BaselineError(f"bootstrap Release {target} tag object drifted")

    downloaded: dict[str, bytes] = {}
    with tempfile.TemporaryDirectory(prefix="iterabase-baseline-release-") as value:
        directory = Path(value)
        for name, asset in assets.items():
            data = backend.download_asset(int(asset["id"]))
            if len(data) != asset.get("size") or hash_bytes(data) != _asset_digest(asset):
                raise BaselineError(f"bootstrap Release {target} asset {name} bytes drifted")
            downloaded[name] = data
            path = directory / name
            path.write_bytes(data)
            _validate_attestation(backend.asset_attestation(spec["tag"], path), release, tag_identity, assets)
    _validate_attestation(backend.release_attestation(spec["tag"]), release, tag_identity, assets)
    try:
        manifest = json.loads(downloaded[manifest_name])
    except json.JSONDecodeError as exc:
        raise BaselineError(f"bootstrap Release {target} manifest is malformed") from exc
    if not isinstance(manifest, dict):
        raise BaselineError(f"bootstrap Release {target} manifest is not an object")
    expected_names = [item.get("name") for item in manifest.get("assets", []) if isinstance(item, dict)]
    if (
        manifest.get("schema_version") != 2
        or manifest.get("target") != target
        or manifest.get("version") != spec["version"]
        or manifest.get("tag") != spec["tag"]
        or manifest.get("source_sha") != BOOTSTRAP_SOURCE
        or manifest.get("candidate_run_id") != BOOTSTRAP_RUN_ID
        or sorted(assets) != sorted([*expected_names, manifest_name])
    ):
        raise BaselineError(f"bootstrap Release {target} manifest or complete member set drifted")
    for item in manifest["assets"]:
        data = downloaded.get(item["name"])
        if data is None or len(data) != item.get("size") or hash_bytes(data) != item.get("sha256"):
            raise BaselineError(f"bootstrap Release {target} manifest member {item.get('name')} drifted")
    return release, downloaded, {"name": manifest_name, "asset_id": spec["manifest_id"], "size": spec["manifest_size"], "sha256": spec["manifest_sha256"]}


def _recipe_hashes(plan: dict[str, Any]) -> dict[str, str]:
    result: dict[str, str] = {}
    for item in plan.get("image_matrix", []):
        result[item["artifact"]] = item["recipe_sha256"]
    for item in plan.get("chart_matrix", []):
        result[f"{item['chart']}-chart" if item["chart"] != "iterabase-platform" else "iterabase-platform-chart"] = item["recipe_sha256"]
        for companion in item.get("companion_recipes", []):
            result[companion["artifact"]] = companion["recipe_sha256"]
    if isinstance(plan.get("forge_recipe_sha256"), str):
        result["forge-binary"] = plan["forge_recipe_sha256"]
    execution = plan.get("execution_plan", {})
    for scenario in execution.get("scenario_matrix", []) if isinstance(execution, dict) else []:
        for artifact in scenario.get("artifacts", []):
            if isinstance(artifact, dict) and isinstance(artifact.get("recipe_sha256"), str):
                result.setdefault(artifact["name"], artifact["recipe_sha256"])
    return result


def _release_pin(spec: dict[str, Any], manifest: dict[str, Any]) -> dict[str, Any]:
    return {
        "id": spec["id"],
        "tag": spec["tag"],
        "immutable": True,
        "tag_object": {"sha": spec["tag_object_sha"], "target_sha": BOOTSTRAP_SOURCE},
        "manifest": manifest,
    }


def _chart_record(
    name: str,
    target: str,
    recipe_sha256: str | None,
    *,
    release_id: int | None = None,
    release_tag: str | None = None,
) -> dict[str, Any]:
    chart, version, size, sha256 = BOOTSTRAP_CHART_IDENTITIES[name]
    reference = f"oci://ghcr.io/nunocgoncalves/iterabase-charts/{chart}:{version}"
    result: dict[str, Any] = {
        "name": name,
        "kind": "chart-companion" if name.endswith("substrate-chart") else "chart",
        "target": target,
        "version": version,
        "custody": "published-baseline",
        "provenance": "bootstrap-promoted" if release_id else "bootstrap-inherited-and-attested",
        "producer_recipe_sha256": recipe_sha256,
        "chart": chart,
        "reference": reference,
        "oci_digest": BOOTSTRAP_CHART_OCI_DIGESTS[name],
        "filename": f"{chart}-{version}.tgz",
        "size": size,
        "sha256": sha256,
    }
    if release_id and release_tag:
        result["release_asset"] = {
            "release_id": release_id,
            "url": f"https://github.com/{REPOSITORY}/releases/download/{release_tag}/{chart}-{version}.tgz",
        }
    return result


def _verify_registry_identity(backend: GitHubBackend, artifact: dict[str, Any]) -> None:
    kind = artifact["kind"]
    if kind == "image":
        reference = artifact["reference"]
        actual = backend.image_digest(reference.split("@", 1)[0])
        if actual != artifact["digest"]:
            raise BaselineError(f"published image {artifact['name']} digest drifted")
    elif kind in {"chart", "chart-companion", "published-chart"}:
        digest, data = backend.chart(artifact["reference"])
        if digest != artifact["oci_digest"] or len(data) != artifact["size"] or hash_bytes(data) != artifact["sha256"]:
            raise BaselineError(f"published chart {artifact['name']} OCI or archive identity drifted")


def bootstrap_snapshot(backend: GitHubBackend, latest: dict[str, Any]) -> dict[str, Any]:
    if latest.get("id") != BOOTSTRAP_ANCHOR_ID:
        raise BaselineError(
            f"Latest Release ID {latest.get('id')!r} is not the approved bootstrap anchor {BOOTSTRAP_ANCHOR_ID}; explicit review is required"
        )
    verified: dict[str, tuple[dict[str, Any], dict[str, bytes], dict[str, Any]]] = {}
    for target in BOOTSTRAP_SELECTED_TARGETS:
        spec = BOOTSTRAP_RELEASES[target]
        verified[target] = _verify_release(
            backend, spec, target, captured=latest if spec["id"] == BOOTSTRAP_ANCHOR_ID else None
        )
    anchor_bytes = verified["iterabase-platform-chart"][1]
    if hash_bytes(anchor_bytes["candidate-plan.json"]) != BOOTSTRAP_PLAN_SHA256 or hash_bytes(anchor_bytes["candidate-evidence.json"]) != BOOTSTRAP_EVIDENCE_SHA256:
        raise BaselineError("bootstrap anchor candidate plan or evidence identity drifted")
    try:
        plan = json.loads(anchor_bytes["candidate-plan.json"])
        evidence = json.loads(anchor_bytes["candidate-evidence.json"])
    except json.JSONDecodeError as exc:
        raise BaselineError("bootstrap candidate plan or evidence is malformed") from exc
    if (
        not isinstance(plan, dict)
        or not isinstance(evidence, dict)
        or plan.get("source_sha") != BOOTSTRAP_SOURCE
        or plan.get("run_id") != BOOTSTRAP_RUN_ID
        or str(plan.get("run_attempt")) != BOOTSTRAP_RUN_ATTEMPT
        or tuple(plan.get("targets", [])) != BOOTSTRAP_SELECTED_TARGETS
        or evidence.get("plan_sha256") != BOOTSTRAP_PLAN_SHA256
        or evidence.get("candidate", {}).get("source_sha") != BOOTSTRAP_SOURCE
    ):
        raise BaselineError("bootstrap candidate plan/evidence authority drifted")
    recipes = _recipe_hashes(plan)

    target_records: list[dict[str, Any]] = []
    direct_images = {
        "control-plane-image": ("control-plane", "ghcr.io/nunocgoncalves/control-plane", "control-plane", "candidate-control-plane.json"),
        "harness-image": ("control-plane", "ghcr.io/nunocgoncalves/control-plane-harness", "control-plane-harness", "candidate-control-plane-harness.json"),
        "tool-runner-image": ("control-plane", "ghcr.io/nunocgoncalves/control-plane-tool-runner", "control-plane-tool-runner", "candidate-control-plane-tool-runner.json"),
    }
    control_artifacts: list[dict[str, Any]] = []
    control_bytes = verified["control-plane"][1]
    for name, (target, repository, image_name, metadata_name) in direct_images.items():
        metadata = json.loads(control_bytes[metadata_name])
        digest = metadata.get("digest")
        record = {
            "name": name,
            "kind": "image",
            "target": target,
            "version": "0.0.33",
            "custody": "published-baseline",
            "provenance": "bootstrap-promoted",
            "producer_recipe_sha256": metadata.get("recipe_sha256"),
            "repository": repository,
            "reference": f"{repository}:0.0.33@{digest}",
            "digest": digest,
            "attestation_subject": image_name,
        }
        _verify_registry_identity(backend, record)
        control_artifacts.append(record)
    target_records.append({
        "target": "control-plane", "version": "0.0.33", "source_sha": BOOTSTRAP_SOURCE,
        "candidate_run_id": BOOTSTRAP_RUN_ID, "candidate_run_attempt": BOOTSTRAP_RUN_ATTEMPT,
        "provenance": "bootstrap-promoted", "release": _release_pin(BOOTSTRAP_RELEASES["control-plane"], verified["control-plane"][2]),
        "artifacts": control_artifacts,
    })

    inference_digest = "sha256:10df17c7c021eb54ce09f4a30e71f3d7c0a58c1499d806fc4ad1dcc04de08450"
    inference_image = {
        "name": "inference-gateway-image", "kind": "image", "target": "inference-gateway", "version": "0.2.7",
        "custody": "published-baseline", "provenance": "bootstrap-inherited-and-attested",
        "producer_recipe_sha256": recipes.get("inference-gateway-image"),
        "repository": "ghcr.io/nunocgoncalves/inference-gateway",
        "reference": f"ghcr.io/nunocgoncalves/inference-gateway:0.2.7@{inference_digest}", "digest": inference_digest,
        "attestation_subject": "bootstrap-candidate-evidence",
    }
    _verify_registry_identity(backend, inference_image)
    target_records.append({
        "target": "inference-gateway", "version": "0.2.7", "source_sha": None,
        "candidate_run_id": BOOTSTRAP_RUN_ID, "candidate_run_attempt": BOOTSTRAP_RUN_ATTEMPT,
        "provenance": "bootstrap-inherited-and-attested", "release": None, "artifacts": [inference_image],
    })

    forge_assets = verified["forge"][1]
    variants = []
    for platform in FORGE_PLATFORMS:
        filename = f"forge_0.9.0_{platform}.tar.gz"
        data = forge_assets[filename]
        variants.append({
            "platform": platform, "filename": filename, "size": len(data), "sha256": hash_bytes(data),
            "url": f"https://github.com/{REPOSITORY}/releases/download/forge-v0.9.0/{filename}",
        })
    forge_record = {
        "name": "forge-binary", "kind": "forge", "target": "forge", "version": "0.9.0",
        "custody": "published-baseline", "provenance": "bootstrap-promoted",
        "producer_recipe_sha256": recipes.get("forge-binary"), "goreleaser_version": "v2.12.7",
        "goreleaser_config_sha256": "1de67067c99857cf19d9203f0b348d524bc98ba98a39bf1f635e0baabc93bea5",
        "variants": variants,
    }
    target_records.append({
        "target": "forge", "version": "0.9.0", "source_sha": BOOTSTRAP_SOURCE,
        "candidate_run_id": BOOTSTRAP_RUN_ID, "candidate_run_attempt": BOOTSTRAP_RUN_ATTEMPT,
        "provenance": "bootstrap-promoted", "release": _release_pin(BOOTSTRAP_RELEASES["forge"], verified["forge"][2]),
        "artifacts": [forge_record],
    })

    cp_chart = _chart_record("control-plane-chart", "control-plane-chart", recipes.get("control-plane-chart"), release_id=BOOTSTRAP_RELEASES["control-plane-chart"]["id"], release_tag=BOOTSTRAP_RELEASES["control-plane-chart"]["tag"])
    _verify_registry_identity(backend, cp_chart)
    target_records.append({
        "target": "control-plane-chart", "version": "0.5.0", "source_sha": BOOTSTRAP_SOURCE,
        "candidate_run_id": BOOTSTRAP_RUN_ID, "candidate_run_attempt": BOOTSTRAP_RUN_ATTEMPT,
        "provenance": "bootstrap-promoted", "release": _release_pin(BOOTSTRAP_RELEASES["control-plane-chart"], verified["control-plane-chart"][2]),
        "artifacts": [cp_chart],
    })

    inference_chart = _chart_record("inference-gateway-chart", "inference-gateway-chart", recipes.get("inference-gateway-chart"))
    _verify_registry_identity(backend, inference_chart)
    target_records.append({
        "target": "inference-gateway-chart", "version": "0.2.13", "source_sha": None,
        "candidate_run_id": BOOTSTRAP_RUN_ID, "candidate_run_attempt": BOOTSTRAP_RUN_ATTEMPT,
        "provenance": "bootstrap-inherited-and-attested", "release": None, "artifacts": [inference_chart],
    })

    platform_artifacts = [
        _chart_record(name, "iterabase-platform-chart", recipes.get(name), release_id=BOOTSTRAP_ANCHOR_ID, release_tag=BOOTSTRAP_RELEASES["iterabase-platform-chart"]["tag"])
        for name in ("iterabase-platform-chart", "cert-manager-substrate-chart", "lvm-storage-substrate-chart")
    ]
    for artifact in platform_artifacts:
        _verify_registry_identity(backend, artifact)
    target_records.append({
        "target": "iterabase-platform-chart", "version": "0.4.0", "source_sha": BOOTSTRAP_SOURCE,
        "candidate_run_id": BOOTSTRAP_RUN_ID, "candidate_run_attempt": BOOTSTRAP_RUN_ATTEMPT,
        "provenance": "bootstrap-promoted", "release": _release_pin(BOOTSTRAP_RELEASES["iterabase-platform-chart"], verified["iterabase-platform-chart"][2]),
        "artifacts": platform_artifacts,
    })

    fixtures = []
    transition_rows = {
        item.get("name"): item
        for item in plan.get("tested_with", {}).get("transition_baselines", {}).get("charts", [])
        if isinstance(item, dict)
    }
    for name in FIXTURE_ORDER:
        chart, version, size, sha256, oci_digest = BOOTSTRAP_FIXTURES[name]
        if name != "certificate-migration-chart":
            row = transition_rows.get(name)
            if row is None or row.get("version") != version or row.get("sha256") != sha256 or row.get("chart") != chart:
                raise BaselineError(f"bootstrap candidate evidence does not bind fixture {name}")
        elif plan.get("tested_with", {}).get("fixture_versions", {}).get("certificate_migration_source") != version:
            raise BaselineError("bootstrap candidate evidence does not bind the certificate migration fixture")
        fixture = {
            "name": name, "kind": "published-chart", "version": version,
            "provenance": "bootstrap-inherited-and-attested", "chart": chart,
            "reference": f"oci://ghcr.io/nunocgoncalves/iterabase-charts/{chart}:{version}",
            "oci_digest": oci_digest, "filename": f"{chart}-{version}.tgz", "size": size, "sha256": sha256,
        }
        _verify_registry_identity(backend, fixture)
        fixtures.append(fixture)

    snapshot = {
        "schema_version": SCHEMA_VERSION,
        "repository": REPOSITORY,
        "generator": {"name": "iterabase-release-baseline", "version": 1},
        "mode": "published",
        "cohort_id": f"bootstrap-{BOOTSTRAP_RUN_ID}-{BOOTSTRAP_RUN_ATTEMPT}",
        "parent_anchor": None,
        "candidate": {
            "run_id": BOOTSTRAP_RUN_ID, "run_attempt": BOOTSTRAP_RUN_ATTEMPT,
            "source_sha": BOOTSTRAP_SOURCE, "control_sha": BOOTSTRAP_SOURCE,
        },
        "selected_targets": list(BOOTSTRAP_SELECTED_TARGETS),
        "anchor_target": "iterabase-platform-chart",
        "anchor": {
            "target": "iterabase-platform-chart", "release_id": BOOTSTRAP_ANCHOR_ID,
            "tag": BOOTSTRAP_RELEASES["iterabase-platform-chart"]["tag"], "source_sha": BOOTSTRAP_SOURCE,
        },
        "targets": target_records,
        "fixtures": fixtures,
    }
    return envelope(snapshot)


def _validate_v3_manifest_authority(
    manifest: dict[str, Any],
    shared_value: dict[str, Any],
    cohort: dict[str, Any],
    release: dict[str, Any],
    release_id: int,
) -> None:
    shared = shared_value["snapshot"]
    if (
        manifest.get("release_id") != release_id
        or str(manifest.get("candidate_run_attempt"))
        != str(cohort.get("candidate_run_attempt"))
        or manifest.get("cohort_id") != shared["cohort_id"]
        or manifest.get("parent_anchor") != shared.get("parent_anchor")
        or manifest.get("anchor_target") != shared["anchor_target"]
        or manifest.get("baseline_snapshot_sha256") != shared_value["snapshot_sha256"]
    ):
        raise BaselineError(
            f"snapshot target {cohort['target']} schema-v3 manifest authority drifted"
        )
    expected_metadata = {
        "title": release.get("name"),
        "notes": release.get("body"),
        "target_commitish": release.get("target_commitish"),
        "prerelease": release.get("prerelease"),
        "make_latest": False,
    }
    if manifest.get("release_metadata") != expected_metadata:
        raise BaselineError(
            f"snapshot target {cohort['target']} governed Release metadata drifted"
        )


def _verify_published_snapshot(
    backend: GitHubBackend,
    value: dict[str, Any],
    captured_anchor: dict[str, Any],
    contract: dict[str, Any],
    *,
    target_filter: set[str] | None = None,
    expected_manifests: Path | None = None,
) -> None:
    snapshot = value["snapshot"]
    verified_releases: dict[int, tuple[dict[str, Any], dict[str, bytes]]] = {}
    for cohort in snapshot["targets"]:
        if target_filter is not None and cohort["target"] not in target_filter:
            continue
        release_pin = cohort.get("release")
        if not isinstance(release_pin, dict) or release_pin.get("id") is None:
            continue
        release_id = int(release_pin["id"])
        release = captured_anchor if release_id == captured_anchor.get("id") else backend.release(release_id)
        if (
            release.get("id") != release_id
            or release.get("tag_name") != release_pin.get("tag")
            or release.get("draft") is not False
            or release.get("prerelease") is not False
            or release.get("immutable") is not True
            or (cohort.get("source_sha") is not None and release.get("target_commitish") != cohort["source_sha"])
        ):
            raise BaselineError(f"snapshot target {cohort['target']} Release identity drifted")
        tag_identity = backend.tag(release_pin["tag"])
        pinned_tag = release_pin.get("tag_object")
        if (
            not isinstance(pinned_tag, dict)
            or tag_identity != {"object_sha": pinned_tag.get("sha"), "target_sha": pinned_tag.get("target_sha")}
            or (cohort.get("source_sha") is not None and tag_identity["target_sha"] != cohort["source_sha"])
        ):
            raise BaselineError(f"snapshot target {cohort['target']} tag/source identity drifted")
        assets = _assets_by_name(release)
        downloaded: dict[str, bytes] = {}
        with tempfile.TemporaryDirectory(prefix="iterabase-snapshot-release-") as raw:
            directory = Path(raw)
            for name, asset in assets.items():
                data = backend.download_asset(int(asset["id"]))
                if len(data) != asset.get("size") or hash_bytes(data) != _asset_digest(asset):
                    raise BaselineError(f"snapshot Release {release_id} asset {name} bytes drifted")
                downloaded[name] = data
                path = directory / name
                path.write_bytes(data)
                _validate_attestation(backend.asset_attestation(release_pin["tag"], path), release, tag_identity, assets)
        _validate_attestation(backend.release_attestation(release_pin["tag"]), release, tag_identity, assets)
        manifest_pin = release_pin.get("manifest")
        manifest_name = manifest_pin.get("name") if isinstance(manifest_pin, dict) else None
        if not isinstance(manifest_name, str) or manifest_name not in downloaded:
            raise BaselineError(f"snapshot target {cohort['target']} has no exact Release manifest")
        if expected_manifests is not None:
            expected_manifest = expected_manifests / manifest_name
            try:
                expected_manifest_bytes = expected_manifest.read_bytes()
            except OSError as exc:
                raise BaselineError(
                    f"expected schema-v3 manifest for {cohort['target']} is unavailable"
                ) from exc
            if downloaded[manifest_name] != expected_manifest_bytes:
                raise BaselineError(
                    f"published Release {release_id} manifest conflicts with the selected candidate"
                )
        try:
            manifest = json.loads(downloaded[manifest_name])
        except json.JSONDecodeError as exc:
            raise BaselineError(f"snapshot target {cohort['target']} Release manifest is malformed") from exc
        if (
            not isinstance(manifest, dict)
            or manifest.get("target") != cohort["target"]
            or manifest.get("version") != cohort["version"]
            or (manifest_pin.get("schema_version") is not None and manifest.get("schema_version") != manifest_pin["schema_version"])
        ):
            raise BaselineError(f"snapshot target {cohort['target']} Release manifest disagrees with its cohort")
        if (
            manifest.get("tag") != release_pin["tag"]
            or manifest.get("source_sha") != cohort.get("source_sha")
            or str(manifest.get("candidate_run_id")) != str(cohort.get("candidate_run_id"))
        ):
            raise BaselineError(f"snapshot target {cohort['target']} manifest provenance disagrees with its cohort")
        if manifest.get("schema_version") == 3:
            shared = downloaded.get("baseline-snapshot.json")
            if shared is None or hash_bytes(shared) != _asset_digest(assets["baseline-snapshot.json"]):
                raise BaselineError(f"snapshot cohort Release {release_id} lacks its shared snapshot bytes")
            try:
                shared_value = json.loads(shared)
            except json.JSONDecodeError as exc:
                raise BaselineError("shared Release snapshot asset is malformed") from exc
            if not isinstance(shared_value, dict):
                raise BaselineError("shared Release snapshot asset is not an object")
            validate_snapshot(shared_value, contract)
            _validate_v3_manifest_authority(
                manifest, shared_value, cohort, release, release_id
            )
            if cohort["target"] in snapshot["selected_targets"]:
                if shared_value != value:
                    raise BaselineError("selected cohort Releases do not carry identical final snapshot bytes")
            else:
                inherited = next(
                    item for item in shared_value["snapshot"]["targets"]
                    if item["target"] == cohort["target"]
                )
                if inherited != cohort:
                    raise BaselineError(f"snapshot target {cohort['target']} was not inherited byte-for-byte")
        elif manifest.get("schema_version") != 2:
            raise BaselineError(f"snapshot target {cohort['target']} manifest schema is unsupported")
        expected_names = [item.get("name") for item in manifest.get("assets", []) if isinstance(item, dict)]
        if sorted(assets) != sorted([*expected_names, manifest_name]):
            raise BaselineError(f"snapshot target {cohort['target']} Release member set is incomplete")
        for item in manifest["assets"]:
            data = downloaded.get(item.get("name"))
            if data is None or len(data) != item.get("size") or hash_bytes(data) != item.get("sha256"):
                raise BaselineError(f"snapshot target {cohort['target']} manifest member drifted")
        verified_releases[release_id] = (release, downloaded)

    for cohort in snapshot["targets"]:
        if target_filter is not None and cohort["target"] not in target_filter:
            continue
        for artifact in cohort["artifacts"]:
            _verify_registry_identity(backend, artifact)
            if artifact["kind"] == "forge":
                release_pin = cohort.get("release")
                if not isinstance(release_pin, dict) or release_pin.get("id") not in verified_releases:
                    raise BaselineError("published Forge cohort has no verified exact Release")
                downloaded = verified_releases[int(release_pin["id"])][1]
                for variant in artifact["variants"]:
                    data = downloaded.get(variant["filename"])
                    if data is None or len(data) != variant["size"] or hash_bytes(data) != variant["sha256"]:
                        raise BaselineError(f"published Forge variant {variant['platform']} drifted")
    if target_filter is None:
        for fixture in snapshot["fixtures"]:
            _verify_registry_identity(backend, fixture)


def _resolve_v3(backend: GitHubBackend, latest: dict[str, Any]) -> dict[str, Any]:
    if latest.get("draft") is not False or latest.get("prerelease") is not False or latest.get("immutable") is not True:
        raise BaselineError("GitHub Latest is not a published immutable full Release")
    assets = _assets_by_name(latest)
    snapshot_asset = assets.get("baseline-snapshot.json")
    if snapshot_asset is None:
        raise BaselineError("GitHub Latest has no complete baseline-snapshot.json")
    data = backend.download_asset(int(snapshot_asset["id"]))
    if len(data) != snapshot_asset.get("size") or hash_bytes(data) != _asset_digest(snapshot_asset):
        raise BaselineError("GitHub Latest baseline snapshot bytes do not match the captured asset")
    try:
        value = json.loads(data)
    except json.JSONDecodeError as exc:
        raise BaselineError("GitHub Latest baseline snapshot is malformed") from exc
    if not isinstance(value, dict):
        raise BaselineError("GitHub Latest baseline snapshot is not an object")
    # The repository contract is checked by the caller. Anchor identity is checked
    # here before any target access so a non-anchor Release cannot select a cohort.
    snapshot = value.get("snapshot")
    anchor = snapshot.get("anchor") if isinstance(snapshot, dict) else None
    if not isinstance(anchor, dict) or anchor.get("release_id") != latest.get("id") or anchor.get("tag") != latest.get("tag_name"):
        raise BaselineError("GitHub Latest is not the deterministic anchor declared by its snapshot")
    return value


def resolve_latest(
    contract: dict[str, Any], *, expected_anchor_id: int | None = None, backend: GitHubBackend | None = None
) -> dict[str, Any]:
    backend = backend or GitHubBackend()
    latest = backend.latest()
    if expected_anchor_id is not None and latest.get("id") != expected_anchor_id:
        raise BaselineError(
            f"current Latest Release ID {latest.get('id')!r} does not match required baseline_anchor_release_id {expected_anchor_id}"
        )
    value = bootstrap_snapshot(backend, latest) if latest.get("id") == BOOTSTRAP_ANCHOR_ID else _resolve_v3(backend, latest)
    validate_snapshot(value, contract)
    if latest.get("id") != BOOTSTRAP_ANCHOR_ID:
        _verify_published_snapshot(backend, value, latest, contract)
    return value


def _test_chart(name: str, target: str, version: str, chart: str) -> dict[str, Any]:
    return {
        "name": name, "kind": "chart-companion" if name.endswith("substrate-chart") else "chart",
        "target": target, "version": version, "custody": "published-baseline", "provenance": "promoted",
        "producer_recipe_sha256": "1" * 64, "chart": chart,
        "reference": f"oci://ghcr.io/nunocgoncalves/iterabase-charts/{chart}:{version}",
        "oci_digest": "sha256:" + "2" * 64, "filename": f"{chart}-{version}.tgz", "size": 1, "sha256": "3" * 64,
    }


def test_snapshot(contract: dict[str, Any]) -> dict[str, Any]:
    """Return a deterministic, non-authoritative snapshot for unit tests."""
    targets: list[dict[str, Any]] = []
    versions = {
        "control-plane": "1.0.0", "inference-gateway": "1.0.1", "forge": "1.0.2",
        "control-plane-chart": "1.0.3", "inference-gateway-chart": "1.0.4", "iterabase-platform-chart": "1.0.5",
    }
    for target in TARGET_ORDER:
        artifacts = []
        for name in contract["targets"][target]["artifacts"]:
            recipe = contract["artifact_recipes"][name]
            if recipe["kind"] == "image":
                digest = "sha256:" + hashlib.sha256(name.encode()).hexdigest()
                artifacts.append({
                    "name": name, "kind": "image", "target": target, "version": versions[target],
                    "custody": "published-baseline", "provenance": "promoted", "producer_recipe_sha256": "1" * 64,
                    "repository": recipe["repository"],
                    "reference": f"{recipe['repository']}:{versions[target]}@{digest}", "digest": digest,
                })
            elif recipe["kind"] in {"chart", "chart-companion"}:
                artifacts.append(_test_chart(name, target, versions[target], recipe["chart"]))
            else:
                variants = [
                    {"platform": platform, "filename": f"forge_{versions[target]}_{platform}.tar.gz", "size": 1, "sha256": hashlib.sha256(platform.encode()).hexdigest(), "url": f"https://github.com/{REPOSITORY}/releases/download/{target}-{versions[target]}/forge_{versions[target]}_{platform}.tar.gz"}
                    for platform in FORGE_PLATFORMS
                ]
                artifacts.append({
                    "name": name, "kind": "forge", "target": target, "version": versions[target],
                    "custody": "published-baseline", "provenance": "promoted", "producer_recipe_sha256": "1" * 64,
                    "goreleaser_version": "v2.12.7", "goreleaser_config_sha256": "4" * 64, "variants": variants,
                })
        targets.append({
            "target": target, "version": versions[target], "source_sha": "a" * 40,
            "candidate_run_id": "1", "candidate_run_attempt": "1", "provenance": "promoted",
            "release": {"id": TARGET_ORDER.index(target) + 1, "tag": f"{target}-{versions[target]}", "immutable": True, "tag_object": {"sha": hashlib.sha1(target.encode()).hexdigest(), "target_sha": "a" * 40}, "manifest": {"name": f"release-manifest-{target}.json"}},
            "artifacts": artifacts,
        })
    fixtures = []
    fixture_versions = {
        "certificate-migration-chart": "0.2.2",
        "supported-platform-predecessor": "0.3.12",
        "supported-substrate-predecessor": "0.3.12",
        "metallb-platform-predecessor": "0.3.19",
        "metallb-substrate-predecessor": "0.3.19",
    }
    for name in FIXTURE_ORDER:
        chart = "cert-manager-substrate" if "substrate" in name else "iterabase-platform"
        fixture = _test_chart(name, "", fixture_versions[name], chart)
        fixture["kind"] = "published-chart"
        fixture.pop("target")
        fixture.pop("custody")
        fixtures.append(fixture)
    snapshot = {
        "schema_version": 1, "repository": REPOSITORY,
        "generator": {"name": "iterabase-release-baseline", "version": 1}, "mode": "published",
        "cohort_id": "test-1", "parent_anchor": None,
        "candidate": {"run_id": "1", "run_attempt": "1", "source_sha": "a" * 40, "control_sha": "a" * 40},
        "selected_targets": list(TARGET_ORDER), "anchor_target": TARGET_ORDER[-1],
        "anchor": {"target": TARGET_ORDER[-1], "release_id": len(TARGET_ORDER), "tag": f"{TARGET_ORDER[-1]}-{versions[TARGET_ORDER[-1]]}", "source_sha": "a" * 40},
        "targets": targets, "fixtures": fixtures,
    }
    value = envelope(snapshot)
    validate_snapshot(value, contract)
    return value


def parent_pin(value: dict[str, Any]) -> dict[str, Any]:
    snapshot = value["snapshot"]
    anchor = snapshot["anchor"]
    return {"release_id": anchor["release_id"], "tag": anchor["tag"], "snapshot_sha256": value["snapshot_sha256"]}


def build_candidate_snapshot(
    plan: dict[str, Any], assets: Path, contract: dict[str, Any]
) -> dict[str, Any]:
    baseline = plan.get("resolved_baseline")
    if not isinstance(baseline, dict):
        raise BaselineError("candidate plan has no pinned resolved baseline")
    validate_snapshot(baseline, contract)
    selected = list(plan["targets"])
    by_target = {item["target"]: copy.deepcopy(item) for item in baseline["snapshot"]["targets"]}
    releases = {item["target"]: item for item in plan["releases"]}
    image_plans = {item["artifact"]: item for item in plan["image_matrix"]}
    chart_plans = {item["target"]: item for item in plan["chart_matrix"]}
    for target in selected:
        release = releases[target]
        target_artifacts: list[dict[str, Any]] = []
        for name in contract["targets"][target]["artifacts"]:
            recipe = contract["artifact_recipes"][name]
            kind = recipe["kind"]
            common = {
                "name": name, "kind": kind, "target": target, "version": release["version"],
                "custody": "selected-candidate", "provenance": "selected-candidate",
                "producer_recipe_sha256": None,
            }
            if kind == "image":
                image_plan = image_plans[name]
                metadata_path = assets / "images" / f"candidate-{image_plan['name']}.json"
                metadata = json.loads(metadata_path.read_text(encoding="utf-8"))
                digest = metadata.get("digest")
                common.update({
                    "producer_recipe_sha256": image_plan["recipe_sha256"], "repository": image_plan["repository"],
                    "reference": f"{image_plan['repository']}:{image_plan['candidate_tag']}@{digest}", "digest": digest,
                    "planned_reference": f"{image_plan['repository']}:{release['version']}",
                })
            elif kind in {"chart", "chart-companion"}:
                chart_plan = chart_plans[target]
                chart = recipe["chart"]
                path = assets / "charts" / f"{chart}-{release['version']}.tgz"
                producer = chart_plan["recipe_sha256"]
                if kind == "chart-companion":
                    producer = next(item["recipe_sha256"] for item in chart_plan["companion_recipes"] if item["artifact"] == name)
                common.update({
                    "producer_recipe_sha256": producer, "chart": chart,
                    "planned_reference": f"oci://ghcr.io/nunocgoncalves/iterabase-charts/{chart}:{release['version']}",
                    "oci_digest": None, "filename": path.name, "size": path.stat().st_size, "sha256": hash_file(path),
                })
            elif kind == "forge":
                variants = []
                for platform in FORGE_PLATFORMS:
                    matches = sorted((assets / "forge").glob(f"forge_*_{platform}.tar.gz"))
                    if len(matches) != 1:
                        raise BaselineError(f"candidate Forge variant {platform} is missing or duplicated")
                    path = matches[0]
                    variants.append({"platform": platform, "filename": path.name, "size": path.stat().st_size, "sha256": hash_file(path), "candidate_path": str(path.relative_to(assets.parent))})
                common.update({
                    "producer_recipe_sha256": plan["forge_recipe_sha256"],
                    "goreleaser_version": contract["artifact_recipes"][name]["goreleaser_version"],
                    "goreleaser_config_sha256": plan["forge_goreleaser_config_sha256"],
                    "variants": variants,
                })
            target_artifacts.append(common)
        by_target[target] = {
            "target": target, "version": release["version"], "source_sha": plan["source_sha"],
            "candidate_run_id": plan["run_id"], "candidate_run_attempt": plan["run_attempt"],
            "provenance": "selected-candidate",
            "release": {"id": None, "tag": release["production_tag"], "immutable": False, "manifest": {"name": f"release-manifest-{target}.json"}},
            "artifacts": target_artifacts,
        }
    snapshot = {
        "schema_version": 1, "repository": REPOSITORY,
        "generator": {"name": "iterabase-release-baseline", "version": 1}, "mode": "candidate",
        "cohort_id": f"candidate-{plan['run_id']}-{plan['run_attempt']}", "parent_anchor": parent_pin(baseline),
        "candidate": {"run_id": plan["run_id"], "run_attempt": plan["run_attempt"], "source_sha": plan["source_sha"], "control_sha": plan["candidate_control_sha"]},
        "selected_targets": selected, "anchor_target": deterministic_anchor(selected),
        "anchor": {"target": deterministic_anchor(selected), "release_id": None, "tag": releases[deterministic_anchor(selected)]["production_tag"], "source_sha": plan["source_sha"]},
        "targets": [by_target[target] for target in TARGET_ORDER],
        "fixtures": copy.deepcopy(baseline["snapshot"]["fixtures"]),
    }
    value = envelope(snapshot)
    validate_snapshot(value, contract)
    return value


def planned_final_snapshot(candidate_snapshot: dict[str, Any], contract: dict[str, Any]) -> dict[str, Any]:
    validate_snapshot(candidate_snapshot, contract)
    snapshot = copy.deepcopy(candidate_snapshot["snapshot"])
    snapshot["mode"] = "planned-final"
    for cohort in snapshot["targets"]:
        if cohort["target"] not in snapshot["selected_targets"]:
            continue
        cohort["provenance"] = "planned-final"
        for artifact in cohort["artifacts"]:
            artifact["custody"] = "planned-final"
            artifact["provenance"] = "planned-final"
            if artifact["kind"] == "image":
                artifact["reference"] = artifact["planned_reference"] + "@" + artifact["digest"]
            elif artifact["kind"] in {"chart", "chart-companion"}:
                artifact["reference"] = artifact["planned_reference"]
    value = envelope(snapshot)
    validate_snapshot(value, contract)
    return value


def final_snapshot(
    candidate_snapshot: dict[str, Any],
    contract: dict[str, Any],
    release_ids: dict[str, int],
    chart_digests: dict[str, str],
    tag_objects: dict[str, dict[str, str]],
) -> dict[str, Any]:
    validate_snapshot(candidate_snapshot, contract)
    if candidate_snapshot["snapshot"]["mode"] != "candidate":
        raise BaselineError("final snapshot input is not a candidate snapshot")
    snapshot = copy.deepcopy(candidate_snapshot["snapshot"])
    snapshot["mode"] = "published"
    selected = snapshot["selected_targets"]
    if set(release_ids) != set(selected):
        raise BaselineError("final snapshot Release ID set does not match selected targets")
    for target in selected:
        if not POSITIVE_INTEGER.fullmatch(str(release_ids[target])):
            raise BaselineError(f"final snapshot target {target} has no exact Release ID")
    for cohort in snapshot["targets"]:
        target = cohort["target"]
        if target not in selected:
            continue
        release_id = release_ids[target]
        tag = cohort["release"]["tag"]
        tag_object = tag_objects.get(target)
        if (
            not isinstance(tag_object, dict)
            or not SHA.fullmatch(str(tag_object.get("sha", "")))
            or tag_object.get("target_sha") != snapshot["candidate"]["source_sha"]
        ):
            raise BaselineError(f"final snapshot target {target} has no exact annotated tag object")
        cohort["provenance"] = "promoted"
        cohort["release"] = {
            "id": release_id,
            "tag": tag,
            "immutable": True,
            "tag_object": tag_object,
            "manifest": {"name": f"release-manifest-{target}.json", "schema_version": 3},
        }
        for artifact in cohort["artifacts"]:
            artifact["custody"] = "published-baseline"
            artifact["provenance"] = "promoted"
            if artifact["kind"] == "image":
                artifact["reference"] = artifact["planned_reference"] + "@" + artifact["digest"]
            elif artifact["kind"] in {"chart", "chart-companion"}:
                digest = chart_digests.get(artifact["name"])
                if not isinstance(digest, str) or not QUALIFIED_SHA256.fullmatch(digest):
                    raise BaselineError(f"final snapshot chart {artifact['name']} has no exact published OCI digest")
                artifact["reference"] = artifact["planned_reference"]
                artifact["oci_digest"] = digest
                artifact["release_asset"] = {
                    "release_id": release_id,
                    "url": f"https://github.com/{REPOSITORY}/releases/download/{tag}/{artifact['filename']}",
                }
            elif artifact["kind"] == "forge":
                for variant in artifact["variants"]:
                    variant["url"] = f"https://github.com/{REPOSITORY}/releases/download/{tag}/{variant['filename']}"
                    variant.pop("candidate_path", None)
    anchor_target = snapshot["anchor_target"]
    anchor_cohort = next(item for item in snapshot["targets"] if item["target"] == anchor_target)
    snapshot["anchor"] = {
        "target": anchor_target,
        "release_id": release_ids[anchor_target],
        "tag": anchor_cohort["release"]["tag"],
        "source_sha": snapshot["candidate"]["source_sha"],
    }
    value = envelope(snapshot)
    validate_snapshot(value, contract)
    return value


def verify_published_members(
    contract: dict[str, Any],
    value: dict[str, Any],
    targets: list[str],
    expected_manifests: Path,
    *,
    backend: GitHubBackend | None = None,
) -> None:
    validate_snapshot(value, contract)
    snapshot = value["snapshot"]
    if snapshot["mode"] != "published":
        raise BaselineError("published-member preflight requires a final published snapshot")
    if not targets or len(targets) != len(set(targets)):
        raise BaselineError("published-member preflight target set is empty or duplicated")
    selected = set(snapshot["selected_targets"])
    if not set(targets).issubset(selected):
        raise BaselineError("published-member preflight target is not selected by the candidate")
    expected_names = {
        f"release-manifest-{target}.json" for target in snapshot["selected_targets"]
    }
    discovered = {
        path.name for path in expected_manifests.glob("release-manifest-*.json")
    }
    if discovered != expected_names:
        raise BaselineError("expected schema-v3 manifest set is incomplete or ambiguous")
    _verify_published_snapshot(
        backend or GitHubBackend(),
        value,
        {},
        contract,
        target_filter=set(targets),
        expected_manifests=expected_manifests,
    )


def _resolve_captured(
    contract: dict[str, Any], backend: GitHubBackend, release: dict[str, Any]
) -> dict[str, Any]:
    if release.get("id") == BOOTSTRAP_ANCHOR_ID:
        value = bootstrap_snapshot(backend, release)
    else:
        value = _resolve_v3(backend, release)
        validate_snapshot(value, contract)
        _verify_published_snapshot(backend, value, release, contract)
    return value


def rollback(
    contract: dict[str, Any],
    *,
    current_anchor_id: int,
    destination_anchor_id: int,
    linear_identifier: str,
    reason: str,
    backend: GitHubBackend | None = None,
) -> dict[str, Any]:
    if not re.fullmatch(r"HOR-[1-9][0-9]*", linear_identifier):
        raise BaselineError("rollback requires one HOR Linear identifier")
    if not reason.strip():
        raise BaselineError("rollback requires a non-empty reason")
    if current_anchor_id == destination_anchor_id:
        raise BaselineError("rollback current and destination anchors must differ")
    backend = backend or GitHubBackend()
    latest = backend.latest()
    if latest.get("id") != current_anchor_id:
        raise BaselineError("rollback current_anchor_release_id is stale")
    current = _resolve_captured(contract, backend, latest)
    visited: set[int] = set()
    destination: dict[str, Any] | None = None
    while True:
        anchor = current["snapshot"]["anchor"]
        anchor_id = int(anchor["release_id"])
        if anchor_id in visited:
            raise BaselineError("baseline snapshot parent chain contains a cycle")
        visited.add(anchor_id)
        if anchor_id == destination_anchor_id:
            destination = current
            break
        parent = current["snapshot"].get("parent_anchor")
        if parent is None:
            break
        parent_id = int(parent["release_id"])
        parent_release = backend.release(parent_id)
        parent_value = _resolve_captured(contract, backend, parent_release)
        if (
            parent_value["snapshot_sha256"] != parent["snapshot_sha256"]
            or parent_value["snapshot"]["anchor"]["tag"] != parent["tag"]
        ):
            raise BaselineError("baseline snapshot parent link does not match the exact ancestor")
        current = parent_value
    if destination is None:
        raise BaselineError("rollback destination is not a whole-snapshot ancestor")
    before_handoff = backend.reread_latest_before_mutation()
    if before_handoff.get("id") != current_anchor_id:
        raise BaselineError("rollback current anchor moved during ancestor verification")
    backend.set_latest(destination_anchor_id)
    reread = backend.reread_latest_after_mutation()
    if reread.get("id") != destination_anchor_id:
        raise BaselineError("rollback Latest handoff did not select the exact destination anchor")
    verified = _resolve_captured(contract, backend, reread)
    if verified != destination:
        raise BaselineError("rollback destination changed during the Latest handoff")
    return {
        "schema_version": 1,
        "linear_identifier": linear_identifier,
        "reason": reason,
        "previous_anchor_release_id": current_anchor_id,
        "destination_anchor_release_id": destination_anchor_id,
        "destination_snapshot_sha256": verified["snapshot_sha256"],
        "verified": True,
    }


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest="command", required=True)
    resolve = sub.add_parser("resolve")
    resolve.add_argument("--contract", type=Path, default=Path("release/targets.json"))
    resolve.add_argument("--expected-anchor-id", type=int)
    resolve.add_argument("--output", type=Path, required=True)
    validate = sub.add_parser("validate")
    validate.add_argument("--contract", type=Path, default=Path("release/targets.json"))
    validate.add_argument("--snapshot", type=Path, required=True)
    verify_members = sub.add_parser("verify-published-members")
    verify_members.add_argument("--contract", type=Path, default=Path("release/targets.json"))
    verify_members.add_argument("--snapshot", type=Path, required=True)
    verify_members.add_argument("--targets", type=Path, required=True)
    verify_members.add_argument("--manifests", type=Path, required=True)
    rollback_parser = sub.add_parser("rollback")
    rollback_parser.add_argument("--contract", type=Path, default=Path("release/targets.json"))
    rollback_parser.add_argument("--current-anchor-id", type=int, required=True)
    rollback_parser.add_argument("--destination-anchor-id", type=int, required=True)
    rollback_parser.add_argument("--linear-identifier", required=True)
    rollback_parser.add_argument("--reason", required=True)
    rollback_parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    try:
        contract = json.loads(args.contract.read_text(encoding="utf-8"))
        if args.command == "resolve":
            value = resolve_latest(contract, expected_anchor_id=args.expected_anchor_id)
            args.output.write_text(compact(value) + "\n", encoding="utf-8")
            print(compact({"anchor_release_id": value["snapshot"]["anchor"]["release_id"], "snapshot_sha256": value["snapshot_sha256"]}))
        elif args.command == "validate":
            validate_snapshot(load_envelope(args.snapshot), contract)
            print("baseline snapshot valid")
        elif args.command == "verify-published-members":
            targets = json.loads(args.targets.read_text(encoding="utf-8"))
            if not isinstance(targets, list) or any(
                not isinstance(target, str) for target in targets
            ):
                raise BaselineError("published-member preflight targets are malformed")
            verify_published_members(
                contract,
                load_envelope(args.snapshot),
                targets,
                args.manifests,
            )
            print("published Release members match the selected candidate")
        else:
            workflow_ref = os.environ.get("GITHUB_WORKFLOW_REF", "")
            if (
                os.environ.get("GITHUB_REF") != "refs/heads/master"
                or "/.github/workflows/release-rollback.yml@" not in workflow_ref
            ):
                raise BaselineError("rollback mutation is allowed only from the protected master rollback workflow")
            result = rollback(
                contract,
                current_anchor_id=args.current_anchor_id,
                destination_anchor_id=args.destination_anchor_id,
                linear_identifier=args.linear_identifier,
                reason=args.reason,
            )
            args.output.write_text(compact(result) + "\n", encoding="utf-8")
            print(compact(result))
    except (BaselineError, OSError, json.JSONDecodeError) as exc:
        print(f"release baseline error: {exc}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
