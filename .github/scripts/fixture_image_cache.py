#!/usr/bin/env python3
"""Pinned-image cache contract for the permanent CPU/GPU fixtures (HOR-588).

The fixture image cache is seeded on the host through the
`.github/workflows/fixture-image-cache.yml` workflow and consumed by the Forge
E2E harness. This module is the single source of truth for which pinned runtime
images each capacity caches, the deterministic generation hash, and the
per-image archive names. The seed workflow and the real-machine jobs both
derive their expectations from it, so the host cache cannot drift from
`.github/inputs/remote-content.json`.
"""

from __future__ import annotations

import argparse
import hashlib
import json
import re
import shlex
import subprocess
import sys
from pathlib import Path
from typing import Any

SCHEMA_VERSION = 1
# Bump when the on-host archive format or naming changes so every fixture
# re-seeds instead of trusting an incompatible generation.
SEED_FORMAT_VERSION = 3
CACHE_ROOT = "/var/lib/iterabase-e2e/image-cache"
CAPACITIES = ("cpu", "gpu")
SHA256_RE = re.compile(r"^sha256:[0-9a-f]{64}$")
ARCHIVE_RE = re.compile(r"^[A-Za-z0-9._-]+\.tar$")


class FixtureImageCacheError(Exception):
    """Raised when the pinned-image cache contract is violated."""


def repository(reference: str) -> str:
    """Return the registry/repository path of an image reference."""
    name = reference.split("@", 1)[0]
    slash = name.rfind("/")
    colon = name.rfind(":")
    if colon > slash:
        name = name[:colon]
    return name


def is_gpu_only(reference: str) -> bool:
    """Return true when the pinned image is only needed by the GPU fixture."""
    path = repository(reference)
    if path.startswith("nvcr.io/"):
        return True
    if path.startswith("nvidia/"):
        return True
    return "vllm" in path


def load_runtime_images(root: Path) -> list[dict[str, str]]:
    """Load and validate the pinned runtime-image authority."""
    manifest_path = root / ".github/inputs/remote-content.json"
    try:
        data = json.loads(manifest_path.read_text(encoding="utf-8"))
    except OSError as error:
        raise FixtureImageCacheError(f"read {manifest_path}: {error}") from error
    except json.JSONDecodeError as error:
        raise FixtureImageCacheError(f"parse {manifest_path}: {error}") from error

    images = data.get("runtime_images") if isinstance(data, dict) else None
    if not isinstance(images, list) or not images:
        raise FixtureImageCacheError("remote-content.json must pin runtime_images")

    selected: list[dict[str, str]] = []
    seen: set[str] = set()
    for entry in images:
        if not isinstance(entry, dict):
            raise FixtureImageCacheError("runtime image entry must be an object")
        reference = entry.get("reference")
        digest = entry.get("digest")
        if not isinstance(reference, str) or not reference:
            raise FixtureImageCacheError("runtime image entry requires a reference")
        if not isinstance(digest, str) or not SHA256_RE.match(digest):
            raise FixtureImageCacheError(
                f"runtime image {reference} requires a sha256 digest"
            )
        if reference in seen:
            raise FixtureImageCacheError(f"duplicate runtime image {reference}")
        seen.add(reference)
        selected.append({"reference": reference, "digest": digest})
    return selected


def select_images(
    images: list[dict[str, str]], capacity: str
) -> list[dict[str, str]]:
    """Return the pinned images a fixture capacity must cache."""
    if capacity not in CAPACITIES:
        raise FixtureImageCacheError(
            f"unsupported capacity {capacity!r}; expected one of {CAPACITIES}"
        )
    if capacity == "gpu":
        return list(images)
    return [image for image in images if not is_gpu_only(image["reference"])]


def archive_name(reference: str, digest: str) -> str:
    """Return the deterministic archive file name for one pinned image."""
    sanitized = re.sub(r"[^A-Za-z0-9._-]+", "_", repository(reference))
    name = f"{sanitized}-{digest.split(':', 1)[1][:12]}.tar"
    if not ARCHIVE_RE.match(name):
        raise FixtureImageCacheError(f"unsafe archive name {name!r}")
    return name


def generation(capacity: str, images: list[dict[str, str]]) -> str:
    """Return the deterministic cache generation for a capacity image set."""
    canonical: dict[str, Any] = {
        "schema_version": SCHEMA_VERSION,
        "seed_format": SEED_FORMAT_VERSION,
        "capacity": capacity,
        "images": [
            {"reference": image["reference"], "digest": image["digest"]}
            for image in sorted(images, key=lambda item: item["reference"])
        ],
    }
    payload = json.dumps(canonical, sort_keys=True, separators=(",", ":")).encode()
    return hashlib.sha256(payload).hexdigest()


def rewrite_docker_manifest(data: bytes, reference: str) -> bytes:
    """Bind the exact reference into a docker-save `manifest.json`.

    k3s's containerd imports docker-format archives by `RepoTags` and normalizes
    the resulting name (`busybox:1.37.0` -> `docker.io/library/busybox:1.37.0`),
    which is the same form `crictl` and kubelet resolve. crane names
    digest-pulled images `i-was-a-digest`, so the tag must be rewritten before
    the archive is imported.
    """
    try:
        manifest = json.loads(data)
    except (json.JSONDecodeError, UnicodeDecodeError) as error:
        raise FixtureImageCacheError(f"decode docker manifest for {reference}: {error}") from error
    if not isinstance(manifest, list) or len(manifest) != 1 or not isinstance(manifest[0], dict):
        raise FixtureImageCacheError(f"docker manifest for {reference} is ambiguous")
    entry = manifest[0]
    if (
        not isinstance(entry.get("Config"), str)
        or not isinstance(entry.get("Layers"), list)
        or not entry["Layers"]
    ):
        raise FixtureImageCacheError(f"docker manifest for {reference} has no config or layers")
    entry["RepoTags"] = [reference]
    return json.dumps(manifest, separators=(",", ":")).encode("utf-8")


def build_manifest(root: Path, capacity: str) -> dict[str, Any]:
    """Build the deterministic cache manifest a fixture capacity must hold."""
    images = select_images(load_runtime_images(root), capacity)
    archives = [archive_name(image["reference"], image["digest"]) for image in images]
    if len(set(archives)) != len(archives):
        raise FixtureImageCacheError("pinned image archive names are not unique")
    entries = [
        {
            "reference": image["reference"],
            "digest": image["digest"],
            "archive": archive,
        }
        for image, archive in zip(images, archives)
    ]
    entries.sort(key=lambda entry: entry["reference"])
    return {
        "schema_version": SCHEMA_VERSION,
        "seed_format": SEED_FORMAT_VERSION,
        "capacity": capacity,
        "generation": generation(capacity, images),
        "cache_root": CACHE_ROOT,
        "images": entries,
    }


def seed_fixture_image_cache(
    *,
    manifest: dict[str, Any],
    address: str,
    user: str,
    key: Path,
    host_key: Path,
    crane: Path,
    platform: str = "linux/amd64",
    dry_run: bool = False,
    runner: Any = subprocess.run,
) -> list[str]:
    """Seed one cache generation on a fixture and return the planned commands.

    Pulls every pinned image with the reviewed crane binary directly on the
    fixture, stages the generation under a temporary directory, and swaps it in
    atomically. A matching generation is left untouched.
    """
    capacity = manifest["capacity"]
    generation = manifest["generation"]
    remote_root = f"{manifest['cache_root']}/{capacity}"
    remote_dir = f"{remote_root}/{generation}"
    staging_dir = f"{remote_root}/.staging-{generation}"
    crane_remote = "/tmp/iterabase-fixture-crane"
    commands: list[str] = []

    ssh_base = [
        "ssh",
        "-i",
        str(key),
        "-o",
        "BatchMode=yes",
        "-o",
        "IdentitiesOnly=yes",
        "-o",
        "StrictHostKeyChecking=yes",
        "-o",
        f"UserKnownHostsFile={host_key}",
        f"{user}@{address}",
    ]
    scp_base = [
        "scp",
        "-i",
        str(key),
        "-o",
        "BatchMode=yes",
        "-o",
        "IdentitiesOnly=yes",
        "-o",
        "StrictHostKeyChecking=yes",
        "-o",
        f"UserKnownHostsFile={host_key}",
    ]

    def ssh_command(remote_command: str) -> list[str]:
        return [*ssh_base, remote_command]

    def execute(
        command: list[str], stdin_text: str | None = None
    ) -> subprocess.CompletedProcess[str]:
        rendered = " ".join(shlex.quote(part) for part in command)
        commands.append(rendered)
        if dry_run:
            print(f"+ {rendered}")
            return subprocess.CompletedProcess(command, 0, "", "")
        return runner(command, check=True, input=stdin_text, text=stdin_text is not None)

    def capture(command: list[str]) -> str:
        rendered = " ".join(shlex.quote(part) for part in command)
        commands.append(rendered)
        if dry_run:
            print(f"+ {rendered}")
            return ""
        result = runner(command, check=True, capture_output=True, text=True)
        return result.stdout

    if not dry_run:
        probe = subprocess.run(
            ssh_command(
                f"sudo test -f {remote_dir}/generation.json && "
                f"sudo cat {remote_dir}/generation.json"
            ),
            capture_output=True,
            text=True,
        )
        if probe.returncode == 0:
            try:
                existing = json.loads(probe.stdout)
            except json.JSONDecodeError:
                existing = {}
            if (
                isinstance(existing, dict)
                and existing.get("generation") == generation
                and existing.get("capacity") == capacity
            ):
                print(
                    f"fixture {address}: cache generation {generation} already seeded"
                )
                return commands

    execute(ssh_command(f"sudo mkdir -p {remote_root}"))
    # Prune every other generation and stale staging directory first: an
    # incompatible or superseded cache can otherwise fill the fixture disk
    # before the new generation is staged.
    execute(
        ssh_command(
            f"sudo find {remote_root} -mindepth 1 -maxdepth 1 -type d "
            f"! -name {shlex.quote(generation)} -exec rm -rf {{}} +"
        )
    )
    execute(ssh_command(f"df -h {remote_root}"))
    execute(ssh_command(f"sudo rm -rf {staging_dir} && sudo mkdir -p {staging_dir}/images"))
    execute(
        [
            *scp_base,
            str(crane),
            f"{user}@{address}:{crane_remote}",
        ]
    )
    execute(ssh_command(f"chmod 0755 {crane_remote}"))
    for image in manifest["images"]:
        reference = f"{image['reference']}@{image['digest']}"
        archive = f"{staging_dir}/images/{image['archive']}"
        workdir = f"{staging_dir}/.work"
        execute(ssh_command(f"sudo rm -rf {workdir} && sudo mkdir -p {workdir}"))
        execute(
            ssh_command(
                f"sudo {crane_remote} pull --platform {platform} "
                f"{shlex.quote(reference)} {workdir}/image.tar"
            )
        )
        execute(ssh_command(f"sudo tar -xf {workdir}/image.tar -C {workdir}"))
        manifest_bytes = capture(ssh_command(f"sudo cat {workdir}/manifest.json"))
        if dry_run:
            execute(ssh_command(f"sudo tee {workdir}/manifest.json >/dev/null"))
        else:
            rewritten = rewrite_docker_manifest(
                manifest_bytes.encode("utf-8"), image["reference"]
            )
            execute(
                ssh_command(f"sudo tee {workdir}/manifest.json >/dev/null"),
                stdin_text=rewritten.decode("utf-8") + "\n",
            )
        execute(ssh_command(f"sudo tar -C {workdir} -cf {shlex.quote(archive)} ."))
        execute(ssh_command(f"sudo rm -rf {workdir}"))

    rendered_manifest = json.dumps(manifest, indent=2) + "\n"
    execute(
        ssh_command(f"sudo tee {staging_dir}/generation.json >/dev/null"),
        stdin_text=rendered_manifest,
    )
    execute(ssh_command(f"sudo rm -rf {remote_dir} && sudo mv {staging_dir} {remote_dir}"))
    execute(ssh_command(f"rm -f {crane_remote}"))
    return commands


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    subcommands = parser.add_subparsers(dest="command", required=True)
    manifest = subcommands.add_parser(
        "manifest", help="emit the deterministic pinned-image cache manifest"
    )
    manifest.add_argument("--capacity", choices=CAPACITIES, required=True)
    manifest.add_argument(
        "--root",
        type=Path,
        default=Path(__file__).resolve().parents[2],
        help="repository root containing .github/inputs/remote-content.json",
    )
    manifest.add_argument(
        "--output", default="-", help="output path, or - for stdout"
    )
    seed = subcommands.add_parser(
        "seed", help="seed one capacity's pinned-image cache on a fixture"
    )
    seed.add_argument("--capacity", choices=CAPACITIES, required=True)
    seed.add_argument("--address", required=True)
    seed.add_argument("--user", required=True)
    seed.add_argument("--key", type=Path, required=True)
    seed.add_argument("--host-key", type=Path, required=True)
    seed.add_argument("--crane", type=Path, required=True)
    seed.add_argument(
        "--root",
        type=Path,
        default=Path(__file__).resolve().parents[2],
        help="repository root containing .github/inputs/remote-content.json",
    )
    seed.add_argument("--dry-run", action="store_true")
    args = parser.parse_args(argv)

    try:
        if args.command == "manifest":
            payload = build_manifest(args.root, args.capacity)
        else:
            seed_fixture_image_cache(
                manifest=build_manifest(args.root, args.capacity),
                address=args.address,
                user=args.user,
                key=args.key,
                host_key=args.host_key,
                crane=args.crane,
                dry_run=args.dry_run,
            )
            return 0
    except FixtureImageCacheError as error:
        print(f"fixture image cache: {error}", file=sys.stderr)
        return 1

    rendered = json.dumps(payload, indent=2, sort_keys=False) + "\n"
    if args.output == "-":
        sys.stdout.write(rendered)
    else:
        Path(args.output).write_text(rendered, encoding="utf-8")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
