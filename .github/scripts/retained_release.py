#!/usr/bin/env python3

from __future__ import annotations

import argparse
import copy
import hashlib
import json
from pathlib import Path
import re
import sys
from typing import Any

EXPECTED_REPOSITORY = "nunocgoncalves/iterabase-mono"
EXPECTED_RELEASE_ID = 382723775
EXPECTED_TAG = "dry-run/immutable-release-gate-v1"
EXPECTED_TITLE = EXPECTED_TAG
REPAIR_TITLE = "forbidden"
EXPECTED_BODY = "Retained non-semantic immutable-release validation evidence."
EXPECTED_TARGET = "42604a60764816a66d147a89d8d0772c9e0d2491"
EXPECTED_TAG_OBJECT = "9f529662036d70348379c6c71a13c9242c7155a5"
EXPECTED_PURL = (
    "pkg:github/nunocgoncalves/iterabase-mono@"
    "dry-run%2Fimmutable-release-gate-v1"
)
EXPECTED_ASSETS = {
    "probe.txt": {
        "id": 544335752,
        "size": 36,
        "digest": "sha256:450a38bb5ae772469148be208a9c794dd5da78bc0a142d026fa3fbc2def354c6",
    },
    "release-manifest.json": {
        "id": 544335753,
        "size": 314,
        "digest": "sha256:2198f7e067910092a58e94b4613d75e122a52fa866dcb943ef91fa5102eab909",
    },
}


class GateError(ValueError):
    pass


def load_json(path: Path) -> dict[str, Any]:
    try:
        with path.open(encoding="utf-8") as source:
            value = json.load(source)
    except (OSError, json.JSONDecodeError) as exc:
        raise GateError(f"could not read one JSON document from {path}: {exc}") from exc
    if not isinstance(value, dict):
        raise GateError(f"{path} must contain one JSON object")
    return value


def require_exact(actual: Any, expected: Any, field: str) -> None:
    if type(actual) is not type(expected) or actual != expected:
        raise GateError(f"{field} does not match retained authority")


def validate_release(
    release: dict[str, Any], *, allow_repair_title: bool = False
) -> None:
    expected_fields = {
        "id": EXPECTED_RELEASE_ID,
        "tag_name": EXPECTED_TAG,
        "target_commitish": EXPECTED_TARGET,
        "body": EXPECTED_BODY,
        "draft": False,
        "prerelease": True,
        "immutable": True,
    }
    for field, expected in expected_fields.items():
        require_exact(release.get(field), expected, f"release.{field}")

    allowed_titles = {EXPECTED_TITLE}
    if allow_repair_title:
        allowed_titles.add(REPAIR_TITLE)
    title = release.get("name")
    if not isinstance(title, str) or title not in allowed_titles:
        raise GateError("release.name is neither governed state nor the one repair value")

    assets = release.get("assets")
    if not isinstance(assets, list) or len(assets) != len(EXPECTED_ASSETS):
        raise GateError("release.assets is not the exact retained member set")
    actual_by_name: dict[str, dict[str, Any]] = {}
    for asset in assets:
        if not isinstance(asset, dict) or not isinstance(asset.get("name"), str):
            raise GateError("release.assets contains a malformed member")
        name = asset["name"]
        if name in actual_by_name:
            raise GateError(f"release.assets contains duplicate member {name}")
        actual_by_name[name] = asset
    if set(actual_by_name) != set(EXPECTED_ASSETS):
        raise GateError("release.assets has a missing, extra, or replaced member")
    for name, expected in EXPECTED_ASSETS.items():
        asset = actual_by_name[name]
        for field, value in expected.items():
            require_exact(asset.get(field), value, f"release.assets[{name}].{field}")


def expected_subjects() -> list[dict[str, Any]]:
    return [
        {"uri": EXPECTED_PURL, "digest": {"sha1": EXPECTED_TAG_OBJECT}},
        *[
            {
                "name": name,
                "digest": {"sha256": definition["digest"].removeprefix("sha256:")},
            }
            for name, definition in EXPECTED_ASSETS.items()
        ],
    ]


def canonical_items(values: list[dict[str, Any]]) -> list[str]:
    return sorted(json.dumps(value, sort_keys=True, separators=(",", ":")) for value in values)


def validate_attestation(attestation: dict[str, Any]) -> dict[str, Any]:
    signed_attestation = attestation.get("attestation")
    bundle = (
        signed_attestation.get("bundle")
        if isinstance(signed_attestation, dict)
        else None
    )
    envelope = bundle.get("dsseEnvelope") if isinstance(bundle, dict) else None
    signatures = envelope.get("signatures") if isinstance(envelope, dict) else None
    if not isinstance(signatures, list) or not signatures or not all(
        isinstance(signature, dict)
        and isinstance(signature.get("sig"), str)
        and signature["sig"]
        for signature in signatures
    ):
        raise GateError("attestation has no signed DSSE envelope")
    verification = attestation.get("verificationResult")
    if not isinstance(verification, dict):
        raise GateError("attestation has no verified result")
    statement = verification.get("statement")
    if not isinstance(statement, dict):
        raise GateError("attestation has no verified statement")
    require_exact(
        statement.get("_type"),
        "https://in-toto.io/Statement/v1",
        "attestation.statement._type",
    )
    require_exact(
        statement.get("predicateType"),
        "https://in-toto.io/attestation/release/v0.2",
        "attestation.statement.predicateType",
    )
    predicate = statement.get("predicate")
    if not isinstance(predicate, dict):
        raise GateError("attestation predicate is malformed")
    for field, expected in {
        "databaseId": str(EXPECTED_RELEASE_ID),
        "repository": EXPECTED_REPOSITORY,
        "tag": EXPECTED_TAG,
        "purl": EXPECTED_PURL,
    }.items():
        require_exact(predicate.get(field), expected, f"attestation.predicate.{field}")
    subjects = statement.get("subject")
    if not isinstance(subjects, list) or not all(
        isinstance(subject, dict) for subject in subjects
    ):
        raise GateError("attestation subjects are malformed")
    if canonical_items(subjects) != canonical_items(expected_subjects()):
        raise GateError("attestation subjects do not bind the exact tag object and assets")
    return statement


def asset_identities(directory: Path) -> list[dict[str, Any]]:
    if not directory.is_dir():
        raise GateError(f"asset directory is missing: {directory}")
    actual_names = {entry.name for entry in directory.iterdir()}
    if actual_names != set(EXPECTED_ASSETS):
        raise GateError("downloaded assets have a missing, extra, or replaced member")
    identities = []
    for name, expected in EXPECTED_ASSETS.items():
        path = directory / name
        if not path.is_file():
            raise GateError(f"downloaded asset is not a regular file: {name}")
        data = path.read_bytes()
        actual = {
            "name": name,
            "size": len(data),
            "digest": f"sha256:{hashlib.sha256(data).hexdigest()}",
        }
        if actual["size"] != expected["size"] or actual["digest"] != expected["digest"]:
            raise GateError(f"downloaded asset bytes do not match retained authority: {name}")
        identities.append(actual)
    return sorted(identities, key=lambda item: item["name"])


PROBES = {
    "asset-upload": {
        "protocol": "http",
        "target": (
            f"https://uploads.github.com/repos/{EXPECTED_REPOSITORY}/"
            f"releases/{EXPECTED_RELEASE_ID}/assets?name=forbidden.txt"
        ),
    },
    "asset-deletion": {
        "protocol": "http",
        "target": (
            f"repos/{EXPECTED_REPOSITORY}/releases/assets/"
            f"{EXPECTED_ASSETS['probe.txt']['id']}"
        ),
    },
    "tag-update": {
        "protocol": "git-porcelain",
        "target": f"refs/tags/{EXPECTED_TAG}:refs/tags/{EXPECTED_TAG}",
    },
    "tag-deletion": {
        "protocol": "git-porcelain",
        "target": f":refs/tags/{EXPECTED_TAG}",
    },
}
_HTTP_STATUS = re.compile(
    r"HTTP/(?:1\.[01]|2(?:\.0)?|3) ([1-5][0-9]{2})(?: [^\r\n]{1,128})?"
)
_GIT_SUMMARY = re.compile(r"\[([a-z ]{1,32})\](?: \([^\t\r\n]*\))?")
_PORCELAIN_FLAGS = frozenset((" ", "!", "+", "-", "*", "=", "."))


def probe_spec(operation: str) -> dict[str, str]:
    spec = PROBES.get(operation)
    if spec is None:
        raise GateError("probe protocol mismatch: operation=unknown result=wrong-operation")
    return spec


def _probe_error(
    operation: str, status: int, protocol: str, details: dict[str, str], result: str
) -> GateError:
    fields = " ".join(f"{key}={value}" for key, value in details.items())
    return GateError(
        f"probe protocol mismatch: operation={operation} status={status} "
        f"protocol={protocol} {fields} result={result}"
    )


def require_probe_result(status: int, output: str, operation: str) -> dict[str, Any]:
    spec = probe_spec(operation)
    if spec["protocol"] == "http":
        records = [
            line.rstrip("\r")
            for line in output.splitlines()
            if line.startswith("HTTP/")
        ]
        match = _HTTP_STATUS.fullmatch(records[0]) if len(records) == 1 else None
        http_status = match.group(1) if match else (
            "missing" if not records else "multiple" if len(records) > 1 else "malformed"
        )
        result = (
            "successful-process"
            if status == 0
            else "unexpected-status"
            if http_status != "422"
            else "accepted"
        )
        details = {"target": "matching", "http_status": http_status}
        if result != "accepted":
            raise _probe_error(operation, status, spec["protocol"], details, result)
        return {
            "operation": operation,
            "process_status": status,
            **spec,
            "http_status": 422,
        }

    records = [
        line.rstrip("\r")
        for line in output.splitlines()
        if len(line) >= 2
        and line[0] in _PORCELAIN_FLAGS
        and line[1] in ("\t", " ")
    ]
    fields = records[0].split("\t", 2) if len(records) == 1 else []
    parsed = len(fields) == 3 and len(fields[0]) == 1
    flag, actual_refspec, summary = fields if parsed else ("malformed", "", "")
    summary_match = _GIT_SUMMARY.fullmatch(summary) if parsed else None
    classification = summary_match.group(1) if summary_match else None
    details = {
        "record": "missing" if not records else "multiple" if len(records) > 1 else "parsed" if parsed else "malformed",
        "flag": flag if flag in _PORCELAIN_FLAGS else "malformed",
        "refspec": "matching" if actual_refspec == spec["target"] else "wrong",
        "classification": (
            "remote-rejected" if classification == "remote rejected"
            else "local-rejected" if classification == "rejected"
            else "malformed" if classification is None else "other"
        ),
    }
    result = (
        "successful-process" if status == 0
        else "missing-record" if not records
        else "multiple-records" if len(records) > 1
        else "malformed-record" if not parsed
        else "unexpected-flag" if flag != "!"
        else "wrong-refspec" if actual_refspec != spec["target"]
        else "unexpected-classification" if classification != "remote rejected"
        else "accepted"
    )
    if result != "accepted":
        raise _probe_error(operation, status, spec["protocol"], details, result)
    return {
        "operation": operation,
        "process_status": status,
        **spec,
        "flag": "!",
        "classification": "remote-rejected",
    }


def canonical_attestation_statement(attestation: dict[str, Any]) -> dict[str, Any]:
    statement = copy.deepcopy(validate_attestation(attestation))
    statement["subject"] = sorted(
        statement["subject"],
        key=lambda subject: json.dumps(
            subject, sort_keys=True, separators=(",", ":")
        ),
    )
    return statement


def make_state(
    release: dict[str, Any],
    attestation: dict[str, Any],
    asset_attestations: dict[str, dict[str, Any]],
    directory: Path,
    tag_object: str,
    tag_target: str,
) -> dict[str, Any]:
    validate_release(release)
    if set(asset_attestations) != set(EXPECTED_ASSETS):
        raise GateError("per-asset attestations do not cover the exact retained assets")
    statement = canonical_attestation_statement(attestation)
    asset_statements = {
        name: canonical_attestation_statement(asset_attestations[name])
        for name in sorted(asset_attestations)
    }
    identities = asset_identities(directory)
    require_exact(tag_object, EXPECTED_TAG_OBJECT, "tag.object")
    require_exact(tag_target, EXPECTED_TARGET, "tag.target")
    assets_by_name = {asset["name"]: asset for asset in release["assets"]}
    return {
        "immutable_authority": {
            "release_id": release["id"],
            "tag": release["tag_name"],
            "tag_object": tag_object,
            "tag_target": tag_target,
            "release_reported_immutable": release["immutable"],
            "assets": [
                {
                    "id": assets_by_name[name]["id"],
                    "name": name,
                    "size": assets_by_name[name]["size"],
                    "digest": assets_by_name[name]["digest"],
                }
                for name in sorted(assets_by_name)
            ],
            "downloaded_assets": identities,
            "release_attestation_statement": statement,
            "asset_attestation_statements": asset_statements,
        },
        "governed_presentation": {
            "title": release["name"],
            "body": release["body"],
            "prerelease": release["prerelease"],
            "draft": release["draft"],
        },
    }


def compare_states(before: dict[str, Any], after: dict[str, Any]) -> None:
    if before != after:
        raise GateError("retained release state changed during safe probes")


def parser() -> argparse.ArgumentParser:
    root = argparse.ArgumentParser()
    commands = root.add_subparsers(dest="command", required=True)

    release = commands.add_parser("validate-release")
    release.add_argument("--release", type=Path, required=True)
    release.add_argument("--allow-repair-title", action="store_true")

    attestation = commands.add_parser("validate-attestation")
    attestation.add_argument("--attestation", type=Path, required=True)

    assets = commands.add_parser("validate-assets")
    assets.add_argument("--directory", type=Path, required=True)

    spec = commands.add_parser("probe-spec")
    spec.add_argument("--operation", required=True)

    probe = commands.add_parser("require-probe-result")
    probe.add_argument("--operation", required=True)
    probe.add_argument("--status", type=int, required=True)
    probe.add_argument("--output", type=Path, required=True)

    state = commands.add_parser("state")
    state.add_argument("--release", type=Path, required=True)
    state.add_argument("--attestation", type=Path, required=True)
    state.add_argument("--probe-attestation", type=Path, required=True)
    state.add_argument("--manifest-attestation", type=Path, required=True)
    state.add_argument("--directory", type=Path, required=True)
    state.add_argument("--tag-object", required=True)
    state.add_argument("--tag-target", required=True)

    compare = commands.add_parser("compare-state")
    compare.add_argument("--before", type=Path, required=True)
    compare.add_argument("--after", type=Path, required=True)
    return root


def _load_protocol_output(path: Path) -> str:
    try:
        return path.read_text(encoding="utf-8", errors="replace")
    except OSError as exc:
        raise GateError(f"could not read protocol output: {exc}") from exc


def _dump_result(result: dict[str, Any]) -> None:
    json.dump(result, sys.stdout, sort_keys=True, separators=(",", ":"))
    sys.stdout.write("\n")


def main() -> int:
    args = parser().parse_args()
    try:
        if args.command == "validate-release":
            validate_release(
                load_json(args.release), allow_repair_title=args.allow_repair_title
            )
        elif args.command == "validate-attestation":
            validate_attestation(load_json(args.attestation))
        elif args.command == "validate-assets":
            asset_identities(args.directory)
        elif args.command == "probe-spec":
            _dump_result(probe_spec(args.operation))
        elif args.command == "require-probe-result":
            _dump_result(
                require_probe_result(
                    args.status, _load_protocol_output(args.output), args.operation
                )
            )
        elif args.command == "state":
            _dump_result(
                make_state(
                    load_json(args.release),
                    load_json(args.attestation),
                    {
                        "probe.txt": load_json(args.probe_attestation),
                        "release-manifest.json": load_json(
                            args.manifest_attestation
                        ),
                    },
                    args.directory,
                    args.tag_object,
                    args.tag_target,
                )
            )
        elif args.command == "compare-state":
            compare_states(load_json(args.before), load_json(args.after))
        else:  # pragma: no cover
            raise GateError(f"unknown command: {args.command}")
    except GateError as exc:
        print(f"retained release gate: {exc}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
