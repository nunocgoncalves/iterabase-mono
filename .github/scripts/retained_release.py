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


_HORIZONTAL = r"[ \t]+"
_DETERMINER = rf"(?:(?:an?|the|this|that|its|their){_HORIZONTAL})?"
_REFUSAL = (
    rf"(?:cannot|can't|may{_HORIZONTAL}not|must{_HORIZONTAL}not|"
    rf"(?:is|are|was|were){_HORIZONTAL}not{_HORIZONTAL}allowed{_HORIZONTAL}to)"
)
_PREVENTION = (
    rf"(?:does{_HORIZONTAL}not{_HORIZONTAL}allow|"
    rf"prevents?|prohibits?)"
)
_CONNECTOR = (
    rf"(?:to|from|on|for|by|because(?:{_HORIZONTAL}of)?|"
    rf"due{_HORIZONTAL}to)"
)
_IMMUTABLE_RELEASE = (
    rf"(?:(?:an?|the){_HORIZONTAL})?immutable(?:[ _-]+)releases?"
)
_RELEASE_IS_IMMUTABLE = (
    rf"(?:the{_HORIZONTAL})?releases?{_HORIZONTAL}(?:is|are)"
    rf"{_HORIZONTAL}immutable"
)
_IMMUTABLE_CAUSE = rf"(?:{_IMMUTABLE_RELEASE}|{_RELEASE_IS_IMMUTABLE})"
_CAUSE_END = (
    rf"(?=[ \t]*[.!]?(?:[ \t]*\((?:HTTP{_HORIZONTAL})?[45][0-9]{{2}}\))?"
    rf"[ \t]*\r?$)"
)

_DENIAL_OPERATIONS = {
    "asset upload": (
        r"(?:uploads?|uploaded|uploading|adds?|added|adding|additions?)",
        r"assets?",
    ),
    "asset deletion": (
        r"(?:deletes?|deleted|deleting|deletions?|"
        r"removes?|removed|removing|removals?)",
        r"assets?",
    ),
    "release tag update": (
        r"(?:force(?:[ _-]+)updates?|force(?:[ _-]+)updated|"
        r"force(?:[ _-]+)updating|updates?|updated|updating|"
        r"moves?|moved|moving|changes?|changed|changing)",
        r"(?:tags?|refs?|references?)",
    ),
    "release tag deletion": (
        r"(?:deletes?|deleted|deleting|deletions?|"
        r"removes?|removed|removing|removals?)",
        r"(?:tags?|refs?|references?)",
    ),
}

_IMMUTABLE_MENTION = re.compile(
    rf"\b(?:immutable(?:[ _-]+)releases?|releases?{_HORIZONTAL}"
    rf"(?:is|are|was|were){_HORIZONTAL}(?:not{_HORIZONTAL})?immutable)\b",
    re.IGNORECASE,
)
_NEGATED_IMMUTABILITY = re.compile(
    rf"\b(?:releases?{_HORIZONTAL}(?:(?:is|are|was|were){_HORIZONTAL}"
    rf"(?:not|never|no{_HORIZONTAL}longer)|isn't|aren't|wasn't|weren't)"
    rf"{_HORIZONTAL}immutable|not{_HORIZONTAL}"
    rf"(?:(?:an?|the){_HORIZONTAL})?immutable(?:[ _-]+)releases?)\b",
    re.IGNORECASE,
)
_AFFIRMATIVE_REFUSAL = re.compile(
    rf"\b(?:{_REFUSAL}|{_PREVENTION}|denied|rejected|refused|forbidden)\b",
    re.IGNORECASE,
)
_CANONICAL_IMMUTABLE_API_PREDICATE = re.compile(
    rf"(?:gh:{_HORIZONTAL})?(?:(?:HTTP{_HORIZONTAL})?[45][0-9]{{2}}:"
    rf"{_HORIZONTAL})?release{_HORIZONTAL}is{_HORIZONTAL}immutable[.!]?"
    rf"(?:[ \t]*\((?:HTTP{_HORIZONTAL})?[45][0-9]{{2}}\))?",
    re.IGNORECASE,
)


def _operation_patterns(mutation: str, subject: str) -> tuple[re.Pattern[str], ...]:
    return tuple(
        re.compile(pattern, re.IGNORECASE | re.MULTILINE)
        for pattern in (
            # GitHub's observed form: "Cannot upload assets to an immutable release".
            rf"\b{_REFUSAL}{_HORIZONTAL}(?:be{_HORIZONTAL})?{mutation}\b"
            rf"{_HORIZONTAL}{_DETERMINER}{subject}\b{_HORIZONTAL}{_CONNECTOR}"
            rf"{_HORIZONTAL}{_IMMUTABLE_CAUSE}\b{_CAUSE_END}",
            # The operation object may precede the refusal in a passive clause.
            rf"\b{subject}\b{_HORIZONTAL}{_REFUSAL}{_HORIZONTAL}"
            rf"(?:be{_HORIZONTAL})?{mutation}\b{_HORIZONTAL}{_CONNECTOR}"
            rf"{_HORIZONTAL}{_IMMUTABLE_CAUSE}\b{_CAUSE_END}",
            # The immutable cause may precede the operation and refusal.
            rf"\b{_IMMUTABLE_CAUSE}\b{_HORIZONTAL}"
            rf"(?:(?:and|therefore|so){_HORIZONTAL})?{_DETERMINER}{subject}\b"
            rf"{_HORIZONTAL}{_REFUSAL}{_HORIZONTAL}(?:be{_HORIZONTAL})?"
            rf"{mutation}\b",
            # Or the immutable cause may explicitly prevent the operation.
            rf"\b{_IMMUTABLE_CAUSE}\b{_HORIZONTAL}{_PREVENTION}{_HORIZONTAL}"
            rf"{_DETERMINER}{subject}\b{_HORIZONTAL}"
            rf"(?:(?:from|to){_HORIZONTAL})?(?:(?:be|being){_HORIZONTAL})?"
            rf"{mutation}\b",
        )
    )


_DENIAL_PATTERNS = {
    operation: _operation_patterns(mutation, subject)
    for operation, (mutation, subject) in _DENIAL_OPERATIONS.items()
}


def _canonical_immutable_api_predicate(output: str) -> bool:
    lines = [line.strip() for line in output.splitlines() if line.strip()]
    return len(lines) == 1 and _CANONICAL_IMMUTABLE_API_PREDICATE.fullmatch(
        lines[0]
    ) is not None


def _operation_pair(output: str, mutation: str, subject: str) -> bool:
    patterns = (
        rf"\b{mutation}\b{_HORIZONTAL}{_DETERMINER}{subject}\b",
        rf"\b{subject}\b{_HORIZONTAL}{_REFUSAL}{_HORIZONTAL}"
        rf"(?:be{_HORIZONTAL})?{mutation}\b",
        rf"\b{subject}\b{_HORIZONTAL}(?:(?:from|to){_HORIZONTAL})?"
        rf"(?:(?:be|being){_HORIZONTAL})?{mutation}\b",
    )
    return any(re.search(pattern, output, re.IGNORECASE) for pattern in patterns)


def _denial_mismatch(
    status: int,
    output: str,
    operation: str,
    *,
    reason: str,
    canonical_predicate: bool = False,
    bounded_causal_clause: bool = False,
    wrong_operations: tuple[str, ...] = (),
) -> GateError:
    if operation in _DENIAL_OPERATIONS:
        mutation, subject = _DENIAL_OPERATIONS[operation]
        expected_mutation = re.search(
            rf"\b{mutation}\b", output, re.IGNORECASE
        ) is not None
        expected_subject = re.search(
            rf"\b{subject}\b", output, re.IGNORECASE
        ) is not None
        expected_operation = _operation_pair(output, mutation, subject)
        operation_label = operation
    else:
        expected_mutation = False
        expected_subject = False
        expected_operation = False
        operation_label = "unknown"

    conflicts = []
    if _NEGATED_IMMUTABILITY.search(output):
        conflicts.append("negated-immutability")
    conflicts.extend(
        f"wrong-operation:{value.replace(' ', '-')}" for value in wrong_operations
    )
    conflict_summary = ",".join(conflicts) if conflicts else "none"
    recognition = (
        f"affirmative_refusal={str(bool(_AFFIRMATIVE_REFUSAL.search(output))).lower()},"
        f"expected_mutation={str(expected_mutation).lower()},"
        f"expected_subject={str(expected_subject).lower()},"
        f"expected_operation={str(expected_operation).lower()},"
        f"immutable_release_cause={str(bool(_IMMUTABLE_MENTION.search(output))).lower()},"
        f"canonical_predicate={str(canonical_predicate).lower()},"
        f"bounded_causal_clause={str(bounded_causal_clause).lower()},"
        f"conflicts={conflict_summary}"
    )
    # Never include remote output here: it may contain credentials or unbounded data.
    return GateError(
        f"denial classification mismatch: operation={operation_label} status={status} "
        f"reason={reason} redacted-recognition[{recognition}]"
    )


def require_immutable_denial(status: int, output: str, operation: str) -> None:
    if operation not in _DENIAL_OPERATIONS:
        raise _denial_mismatch(status, output, operation, reason="unknown-operation")

    canonical_predicate = _canonical_immutable_api_predicate(output)
    bounded_causal_clause = any(
        pattern.search(output) for pattern in _DENIAL_PATTERNS[operation]
    )
    wrong_operations = tuple(
        candidate
        for candidate, patterns in _DENIAL_PATTERNS.items()
        if candidate != operation and any(pattern.search(output) for pattern in patterns)
    )
    negated = _NEGATED_IMMUTABILITY.search(output) is not None

    if status == 0:
        raise _denial_mismatch(
            status,
            output,
            operation,
            reason="successful-result",
            canonical_predicate=canonical_predicate,
            bounded_causal_clause=bounded_causal_clause,
            wrong_operations=wrong_operations,
        )
    if negated or wrong_operations:
        raise _denial_mismatch(
            status,
            output,
            operation,
            reason="conflicting-evidence",
            canonical_predicate=canonical_predicate,
            bounded_causal_clause=bounded_causal_clause,
            wrong_operations=wrong_operations,
        )
    if not canonical_predicate and not bounded_causal_clause:
        raise _denial_mismatch(
            status,
            output,
            operation,
            reason="missing-bounded-causal-denial",
            canonical_predicate=canonical_predicate,
            bounded_causal_clause=bounded_causal_clause,
            wrong_operations=wrong_operations,
        )


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
