#!/usr/bin/env python3
from __future__ import annotations

import importlib.util
import json
from pathlib import Path
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
SPEC = importlib.util.spec_from_file_location("release_contract", Path(__file__).with_name("release.py"))
assert SPEC and SPEC.loader
release = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(release)


class ReleaseContractTests(unittest.TestCase):
    def setUp(self) -> None:
        self.targets = release.load_json(ROOT / "release" / "targets.json")
        self.sha = "a" * 40

    def plan(self, target: str) -> dict:
        return release.make_plan(self.targets, target, self.sha, "123", ROOT)

    def test_repository_contract_is_valid(self) -> None:
        release.validate_contract(self.targets, ROOT)

    def test_component_versions_are_local_authorities(self) -> None:
        self.assertEqual(release.read_version(ROOT / "control-plane" / "VERSION"), "0.0.25")
        self.assertEqual(release.read_version(ROOT / "inference-gateway" / "VERSION"), "0.2.5")
        self.assertEqual(release.read_version(ROOT / "forge" / "VERSION"), "0.8.1")
        self.assertFalse((ROOT / "release" / "compatibility.json").exists())

    def test_candidate_selects_exactly_one_target(self) -> None:
        plan = self.plan("control-plane")
        self.assertEqual(plan["target"], "control-plane")
        self.assertEqual(plan["version"], "0.0.25")
        self.assertEqual(plan["production_tag"], "control-plane-v0.0.25")
        self.assertEqual(
            [item["name"] for item in plan["image_matrix"]],
            ["control-plane", "control-plane-harness", "control-plane-tool-runner"],
        )
        self.assertTrue(all(item["candidate_tag"] == self.sha for item in plan["image_matrix"]))
        self.assertEqual(plan["chart_matrix"], [])
        self.assertFalse(plan["real_machine"])

    def test_forge_is_one_target_with_real_machine_validation(self) -> None:
        plan = self.plan("forge")
        self.assertTrue(plan["forge"])
        self.assertTrue(plan["real_machine"])
        self.assertEqual(plan["production_tag"], "forge-v0.8.1")
        self.assertEqual(plan["image_matrix"], [])
        self.assertEqual(plan["chart_matrix"], [])

    def test_chart_version_and_dependencies_come_from_chart_source(self) -> None:
        plan = self.plan("iterabase-platform-chart")
        self.assertEqual(plan["version"], "0.3.9")
        self.assertEqual(plan["chart_matrix"][0]["companions"], ["cert-manager-substrate"])
        dependencies = {
            item["name"]: item["version"]
            for item in plan["tested_with"]["selected_chart_dependencies"]
        }
        self.assertEqual(dependencies["control-plane"], "0.4.7")
        self.assertEqual(dependencies["inference-gateway"], "0.2.9")
        self.assertEqual(
            plan["tested_with"]["chart_metadata"]["control-plane"]["appVersion"],
            "0.0.25",
        )

    def test_invalid_source_and_target_are_rejected(self) -> None:
        with self.assertRaises(release.ReleaseError):
            release.make_plan(self.targets, "everything", self.sha, "1", ROOT)
        with self.assertRaises(release.ReleaseError):
            release.make_plan(self.targets, "forge", "short", "1", ROOT)
        with self.assertRaises(release.ReleaseError):
            release.make_plan(self.targets, "forge", self.sha, "run", ROOT)

    def test_prerelease_and_v_prefix_are_rejected(self) -> None:
        for value in ("v1.2.3", "1.2.3-rc.1", "01.2.3", "latest"):
            with self.subTest(value=value), self.assertRaises(release.ReleaseError):
                release.require_semver(value, "version")

    def test_generated_evidence_binds_plan_and_exact_image_assets(self) -> None:
        plan = self.plan("inference-gateway")
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            assets = root / "assets"
            images = assets / "images"
            images.mkdir(parents=True)
            metadata = {
                "schema_version": 2,
                "artifact_type": "image",
                "name": "inference-gateway",
                "target": "inference-gateway",
                "repository": "ghcr.io/nunocgoncalves/inference-gateway",
                "candidate_tag": self.sha,
                "version": "0.2.5",
                "digest": "sha256:" + "1" * 64,
                "source_sha": self.sha,
            }
            (images / "candidate-inference-gateway.json").write_text(
                release.compact(metadata) + "\n", encoding="utf-8"
            )
            (images / "candidate-inference-gateway.spdx.json").write_text(
                '{"spdxVersion":"SPDX-2.3"}\n', encoding="utf-8"
            )
            (root / "candidate-plan.json").write_text(
                release.compact(plan) + "\n", encoding="utf-8"
            )
            evidence = release.assemble_evidence(plan, assets)
            (root / "candidate-evidence.json").write_text(
                release.compact(evidence) + "\n", encoding="utf-8"
            )
            verified = release.verify_candidate(root)
            self.assertEqual(verified["source_sha"], self.sha)

            metadata["source_sha"] = "b" * 40
            (images / "candidate-inference-gateway.json").write_text(
                release.compact(metadata) + "\n", encoding="utf-8"
            )
            with self.assertRaisesRegex(release.ReleaseError, "checksums"):
                release.verify_candidate(root)

    def test_forge_candidate_requires_four_archives_and_no_unpacked_binaries(self) -> None:
        plan = self.plan("forge")
        with tempfile.TemporaryDirectory() as directory:
            assets = Path(directory) / "forge"
            assets.mkdir(parents=True)
            for platform in ("linux_amd64", "linux_arm64", "darwin_amd64", "darwin_arm64"):
                (assets / f"forge_0.8.1_{platform}.tar.gz").write_bytes(platform.encode())
            (assets / "checksums.txt").write_text("fixture\n", encoding="utf-8")
            release.validate_candidate_assets(plan, Path(directory))
            self.assertFalse(any(path.name == "forge" for path in assets.rglob("*")))

    def test_workflows_are_split_and_promotion_keeps_environment_gate(self) -> None:
        candidate = (ROOT / ".github" / "workflows" / "release-candidate.yml").read_text(
            encoding="utf-8"
        )
        promotion = (ROOT / ".github" / "workflows" / "release-promote.yml").read_text(
            encoding="utf-8"
        )
        self.assertIn("target:", candidate)
        self.assertNotIn("environment: release", candidate)
        self.assertIn("candidate_run_id:", promotion)
        self.assertIn("environment: release", promotion)
        self.assertNotIn("candidate_namespace", candidate)
        self.assertNotIn("iterabase-release-candidates", candidate)
        self.assertNotIn("compatibility.json", candidate + promotion)

    def test_release_workflows_never_publish_from_push_or_tag_events(self) -> None:
        for name in ("release-candidate.yml", "release-promote.yml", "release-rehearsal.yml"):
            workflow = (ROOT / ".github" / "workflows" / name).read_text(encoding="utf-8")
            self.assertIn("workflow_dispatch:", workflow)
            self.assertNotIn("push:", workflow)


if __name__ == "__main__":
    unittest.main()
