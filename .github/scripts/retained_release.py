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


def require_immutable_denial(status: int, output: str, operation: str) -> None:
    if status == 0:
        raise GateError(f"{operation} unexpectedly succeeded")

    immutable_release = r"immutable(?:[ _-]+)releases?"
    mutation = (
        r"(?:upload(?:ed|ing)?|add(?:ed|ing)?|delet(?:e|ed|ing)|"
        r"remov(?:e|ed|ing)|updat(?:e|ed|ing)|modif(?:y|ied|ying)|"
        r"mov(?:e|ed|ing)|mutat(?:e|ed|ing)|chang(?:e|ed|ing)|"
        r"overwrit(?:e|ten|ing)|force(?:[ _-]+)updat(?:e|ed|ing))"
    )
    affirmative_denials = (
        # GitHub's API reports immutable asset mutations with this predicate.
        re.compile(r"\brelease\s+is\s+immutable\b", re.IGNORECASE),
        # Git transport can put the refusal before the immutable-release reason.
        re.compile(
            rf"\b(?:cannot|can't|may not|must not|not allowed to)\s+"
            rf"(?:be\s+)?{mutation}\s+"
            rf"(?:an?\s+|the\s+)?{immutable_release}\b",
            re.IGNORECASE,
        ),
        # Accept the same causal statement when GitHub puts the reason first.
        re.compile(
            rf"\b{immutable_release}\b\s+(?:(?:assets?|tags?)\s+)?"
            rf"(?:cannot|can't|may not|must not|does not allow|prevents?|prohibits?)\s+"
            rf"(?:the\s+)?(?:release\s+)?(?:asset\s+|tag\s+)?(?:from\s+)?"
            rf"(?:(?:be|being)\s+)?{mutation}\b",
            re.IGNORECASE,
        ),
    )
    if not any(denial.search(output) for denial in affirmative_denials):
        raise GateError(f"{operation} failed without an immutable-release denial")


def make_state(
    release: dict[str, Any],
    attestation: dict[str, Any],
    directory: Path,
    tag_object: str,
    tag_target: str,
) -> dict[str, Any]:
    validate_release(release)
    statement = copy.deepcopy(validate_attestation(attestation))
    statement["subject"] = sorted(
        statement["subject"],
        key=lambda subject: json.dumps(
            subject, sort_keys=True, separators=(",", ":")
        ),
    )
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
            "attestation_statement": statement,
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

    denial = commands.add_parser("require-denial")
    denial.add_argument("--status", type=int, required=True)
    denial.add_argument("--output", type=Path, required=True)
    denial.add_argument("--operation", required=True)

    state = commands.add_parser("state")
    state.add_argument("--release", type=Path, required=True)
    state.add_argument("--attestation", type=Path, required=True)
    state.add_argument("--directory", type=Path, required=True)
    state.add_argument("--tag-object", required=True)
    state.add_argument("--tag-target", required=True)

    compare = commands.add_parser("compare-state")
    compare.add_argument("--before", type=Path, required=True)
    compare.add_argument("--after", type=Path, required=True)
    return root


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
        elif args.command == "require-denial":
            try:
                output = args.output.read_text(encoding="utf-8", errors="replace")
            except OSError as exc:
                raise GateError(f"could not read denial output: {exc}") from exc
            require_immutable_denial(args.status, output, args.operation)
        elif args.command == "state":
            state = make_state(
                load_json(args.release),
                load_json(args.attestation),
                args.directory,
                args.tag_object,
                args.tag_target,
            )
            json.dump(state, sys.stdout, sort_keys=True, separators=(",", ":"))
            sys.stdout.write("\n")
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
