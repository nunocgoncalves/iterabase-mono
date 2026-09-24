#!/usr/bin/env python3

from __future__ import annotations

import contextlib
import io
import json
from pathlib import Path
import random
import subprocess
import sys
import tempfile
import unittest

import fixture_image_cache

ROOT = Path(__file__).resolve().parents[2]


class FixtureImageCacheTests(unittest.TestCase):
    def test_capacities_cache_the_shared_platform_authority(self) -> None:
        images = fixture_image_cache.load_runtime_images(ROOT)
        cpu = fixture_image_cache.select_images(images, "cpu")
        gpu = fixture_image_cache.select_images(images, "gpu")

        self.assertEqual(len(gpu), len(cpu))
        cpu_refs = {image["reference"] for image in cpu}
        gpu_refs = {image["reference"] for image in gpu}
        self.assertEqual(cpu_refs, gpu_refs)
        self.assertFalse(any(fixture_image_cache.is_gpu_only(ref) for ref in gpu_refs))
        gpu_only = [image["reference"] for image in images if fixture_image_cache.is_gpu_only(image["reference"])]
        self.assertTrue(gpu_only)
        for reference in gpu_only:
            self.assertNotIn(reference, gpu_refs)
        for image in images:
            if not fixture_image_cache.is_gpu_only(image["reference"]):
                self.assertIn(image["reference"], gpu_refs)

    def test_gpu_only_classification_matches_reviewed_authority(self) -> None:
        images = fixture_image_cache.load_runtime_images(ROOT)
        gpu_only = [
            image["reference"]
            for image in images
            if fixture_image_cache.is_gpu_only(image["reference"])
        ]
        shared = [
            image["reference"]
            for image in images
            if not fixture_image_cache.is_gpu_only(image["reference"])
        ]
        self.assertTrue(any(ref.startswith("nvcr.io/") for ref in gpu_only))
        self.assertTrue(any(ref.startswith("nvidia/") for ref in gpu_only))
        self.assertTrue(any("vllm" in ref for ref in gpu_only))
        self.assertTrue(any(ref.startswith("ghcr.io/nunocgoncalves/iterabase-third-party/minio") for ref in shared))

    def test_generation_is_order_independent_and_capacity_bound(self) -> None:
        images = fixture_image_cache.load_runtime_images(ROOT)
        cpu = fixture_image_cache.select_images(images, "cpu")
        shuffled = list(cpu)
        random.Random(501).shuffle(shuffled)

        self.assertEqual(
            fixture_image_cache.generation("cpu", cpu),
            fixture_image_cache.generation("cpu", shuffled),
        )
        self.assertNotEqual(
            fixture_image_cache.generation("cpu", cpu),
            fixture_image_cache.generation("gpu", fixture_image_cache.select_images(images, "gpu")),
        )

    def test_generation_changes_when_a_digest_changes(self) -> None:
        images = fixture_image_cache.load_runtime_images(ROOT)
        cpu = fixture_image_cache.select_images(images, "cpu")
        mutated = [dict(image) for image in cpu]
        mutated[0]["digest"] = "sha256:" + "0" * 64

        self.assertNotEqual(
            fixture_image_cache.generation("cpu", cpu),
            fixture_image_cache.generation("cpu", mutated),
        )

    def test_archive_names_are_unique_safe_and_digest_bound(self) -> None:
        for capacity in fixture_image_cache.CAPACITIES:
            manifest = fixture_image_cache.build_manifest(ROOT, capacity)
            archives = [entry["archive"] for entry in manifest["images"]]
            self.assertEqual(len(archives), len(set(archives)))
            for entry in manifest["images"]:
                self.assertRegex(entry["archive"], fixture_image_cache.ARCHIVE_RE)
                self.assertTrue(entry["archive"].endswith(".tar"))
                self.assertNotIn("/", entry["archive"])

    def test_manifest_binds_generation_authority_and_cache_root(self) -> None:
        manifest = fixture_image_cache.build_manifest(ROOT, "gpu")
        self.assertEqual(manifest["schema_version"], fixture_image_cache.SCHEMA_VERSION)
        self.assertEqual(manifest["capacity"], "gpu")
        self.assertEqual(manifest["cache_root"], fixture_image_cache.CACHE_ROOT)
        self.assertRegex(manifest["generation"], r"^[0-9a-f]{64}$")
        references = [entry["reference"] for entry in manifest["images"]]
        self.assertEqual(references, sorted(references))
        for entry in manifest["images"]:
            self.assertRegex(entry["digest"], fixture_image_cache.SHA256_RE)

    def test_seed_dry_run_plans_every_digest_bound_pull(self) -> None:
        manifest = fixture_image_cache.build_manifest(ROOT, "gpu")
        output = io.StringIO()
        with contextlib.redirect_stdout(output):
            commands = fixture_image_cache.seed_fixture_image_cache(
                manifest=manifest,
                address="192.0.2.10",
                user="forge-ci",
                key=Path("/tmp/fixture-key"),
                host_key=Path("/tmp/fixture-host.pub"),
                crane=Path("/tmp/crane"),
                dry_run=True,
            )
        self.assertEqual(len(commands), len(manifest["images"]) * 7 + 9)
        printed = output.getvalue()
        for image in manifest["images"]:
            self.assertIn(f"{image['reference']}@{image['digest']}", printed)
            self.assertIn(image["archive"], printed)
        self.assertIn(f"{manifest['cache_root']}/gpu/{manifest['generation']}", printed)
        self.assertIn("StrictHostKeyChecking=yes", printed)

    def test_seed_replays_without_a_runner(self) -> None:
        manifest = fixture_image_cache.build_manifest(ROOT, "cpu")

        def failing_runner(*_args: object, **_kwargs: object) -> None:
            raise AssertionError("dry-run must not execute commands")

        commands = fixture_image_cache.seed_fixture_image_cache(
            manifest=manifest,
            address="192.0.2.10",
            user="forge-ci",
            key=Path("/tmp/fixture-key"),
            host_key=Path("/tmp/fixture-host.pub"),
            crane=Path("/tmp/crane"),
            dry_run=True,
            runner=failing_runner,
        )
        self.assertEqual(len(commands), len(manifest["images"]) * 7 + 9)

    def test_rewrite_docker_manifest_binds_the_exact_reference(self) -> None:
        manifest = json.dumps(
            [
                {
                    "Config": "sha256:" + "b" * 64,
                    "RepoTags": ["index.docker.io/library/busybox:i-was-a-digest"],
                    "Layers": ["436a1b1f.tar.gz"],
                }
            ]
        ).encode()
        rewritten = json.loads(
            fixture_image_cache.rewrite_docker_manifest(manifest, "busybox:1.37.0")
        )
        self.assertEqual(rewritten[0]["RepoTags"], ["busybox:1.37.0"])
        self.assertEqual(rewritten[0]["Config"], "sha256:" + "b" * 64)
        self.assertEqual(rewritten[0]["Layers"], ["436a1b1f.tar.gz"])

    def test_rewrite_docker_manifest_rejects_ambiguous_manifests(self) -> None:
        valid_entry = {"Config": "sha256:" + "b" * 64, "Layers": ["436a1b1f.tar.gz"]}
        for payload in (
            b"{not json",
            json.dumps([valid_entry, valid_entry]).encode(),
            json.dumps([{"RepoTags": ["busybox:1.37.0"], "Layers": ["436a1b1f.tar.gz"]}]).encode(),
            json.dumps([{"Config": "sha256:" + "b" * 64, "Layers": []}]).encode(),
        ):
            with self.assertRaises(fixture_image_cache.FixtureImageCacheError):
                fixture_image_cache.rewrite_docker_manifest(payload, "busybox:1.37.0")

    def test_unknown_capacity_is_rejected(self) -> None:
        images = fixture_image_cache.load_runtime_images(ROOT)
        with self.assertRaisesRegex(
            fixture_image_cache.FixtureImageCacheError, "unsupported capacity"
        ):
            fixture_image_cache.select_images(images, "quantum")

    def test_invalid_authority_entries_are_rejected(self) -> None:
        with tempfile.TemporaryDirectory() as value:
            root = Path(value)
            manifest = root / ".github/inputs/remote-content.json"
            manifest.parent.mkdir(parents=True)
            cases = (
                {"runtime_images": []},
                {"runtime_images": [{"reference": "x", "digest": "sha256:short"}]},
                {
                    "runtime_images": [
                        {"reference": "x", "digest": "sha256:" + "a" * 64},
                        {"reference": "x", "digest": "sha256:" + "b" * 64},
                    ]
                },
            )
            for case in cases:
                manifest.write_text(json.dumps(case), encoding="utf-8")
                with self.assertRaises(fixture_image_cache.FixtureImageCacheError):
                    fixture_image_cache.load_runtime_images(root)
            manifest.write_text("{not json", encoding="utf-8")
            with self.assertRaises(fixture_image_cache.FixtureImageCacheError):
                fixture_image_cache.load_runtime_images(root)

    def test_cli_emits_the_same_manifest(self) -> None:
        script = Path(__file__).with_name("fixture_image_cache.py")
        completed = subprocess.run(
            [sys.executable, str(script), "manifest", "--capacity", "cpu"],
            cwd=ROOT,
            check=True,
            capture_output=True,
            text=True,
        )
        emitted = json.loads(completed.stdout)
        self.assertEqual(
            emitted, fixture_image_cache.build_manifest(ROOT, "cpu")
        )


if __name__ == "__main__":
    unittest.main()
