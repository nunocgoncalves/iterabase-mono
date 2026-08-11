#!/usr/bin/env python3
from __future__ import annotations

import copy
import importlib.util
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
        self.manifest = release.load_json(ROOT / "release" / "compatibility.json")
        self.targets = release.load_json(ROOT / "release" / "targets.json")
        self.sha = "a" * 40

    def test_repository_contract_is_valid(self) -> None:
        release.validate_contract(self.manifest, self.targets, ROOT)

    def test_single_component_selects_conservative_suite(self) -> None:
        plan = release.make_plan(
            self.manifest,
            self.targets,
            {"control-plane": "0.0.25"},
            self.sha,
            "123",
            False,
        )
        self.assertEqual([item["target"] for item in plan["selected"]], ["control-plane"])
        self.assertEqual(
            [item["name"] for item in plan["image_matrix"]],
            ["control-plane", "control-plane-harness", "control-plane-tool-runner"],
        )
        self.assertEqual(
            [item["name"] for item in plan["kind_matrix"]],
            ["controlplane-identity", "inference-contract", "internal-tls", "tool-runner-contract"],
        )
        self.assertFalse(plan["real_machine"])

    def test_coordinated_request_takes_union_without_duplicates(self) -> None:
        plan = release.make_plan(
            self.manifest,
            self.targets,
            {
                "control-plane": "0.0.25",
                "inference-gateway": "0.2.5",
                "iterabase-platform-chart": "0.3.9",
            },
            self.sha,
            "456",
            False,
        )
        self.assertEqual(len(plan["kind_matrix"]), 5)
        self.assertEqual(plan["source_suites"], ["charts", "control-plane", "inference-gateway"])
        self.assertTrue(plan["real_machine"])
        self.assertEqual(len(plan["chart_matrix"]), 1)

    def test_dry_run_never_uses_production_release_tag(self) -> None:
        plan = release.make_plan(
            self.manifest,
            self.targets,
            {"forge": "0.8.1"},
            self.sha,
            "789",
            True,
        )
        selected = plan["selected"][0]
        self.assertEqual(selected["production_tag"], "forge-v0.8.1")
        self.assertEqual(selected["release_tag"], "dry-run/forge-v0.8.1-789")

    def test_manifest_mismatch_is_rejected(self) -> None:
        with self.assertRaisesRegex(release.ReleaseError, "does not match manifest"):
            release.make_plan(
                self.manifest,
                self.targets,
                {"forge": "0.8.2"},
                self.sha,
                "1",
                False,
            )

    def test_prerelease_and_v_prefix_are_rejected(self) -> None:
        for value in ("v1.2.3", "1.2.3-rc.1", "01.2.3", "latest"):
            with self.subTest(value=value), self.assertRaises(release.ReleaseError):
                release.require_semver(value, "version")

    def test_component_can_release_independently_of_chart_default(self) -> None:
        manifest = copy.deepcopy(self.manifest)
        manifest["components"]["control-plane"]["version"] = "0.0.26"
        release.validate_contract(manifest, self.targets)

    def test_dependency_injection_accepts_source_and_helm_key_order(self) -> None:
        fixtures = (
            """dependencies:\n  - name: inference-gateway\n    version: 0.2.8\n    repository: file://../inference-gateway\n""",
            """dependencies:\n- condition: inference-gateway.enabled\n  name: inference-gateway\n  repository: file://../inference-gateway\n  version: 0.2.8\n""",
        )
        for source in fixtures:
            with self.subTest(source=source), tempfile.TemporaryDirectory() as directory:
                chart = Path(directory) / "Chart.yaml"
                chart.write_text(source, encoding="utf-8")
                release.replace_chart_dependency_version(chart, "inference-gateway", "0.2.9")
                self.assertIn("version: 0.2.9", chart.read_text(encoding="utf-8"))

    def test_release_workflow_has_no_shell_escaped_jq_keys(self) -> None:
        workflow = (ROOT / ".github" / "workflows" / "release.yml").read_text(encoding="utf-8")
        self.assertNotIn(r'[\"', workflow)

    def test_fixture_drift_is_rejected(self) -> None:
        manifest = copy.deepcopy(self.manifest)
        manifest["fixtures"]["platform_chart"] = "9.9.9"
        with self.assertRaisesRegex(release.ReleaseError, "pinnedPlatformChartVersion"):
            release.validate_contract(manifest, self.targets, ROOT)

    def test_promotion_ledger_records_partial_completion(self) -> None:
        plan = release.make_plan(
            self.manifest,
            self.targets,
            {"control-plane": "0.0.25", "inference-gateway": "0.2.5"},
            self.sha,
            "4",
            False,
        )
        ledger = release.new_promotion_ledger(plan)
        release.record_promotion(
            ledger,
            "control-plane",
            "image:control-plane",
            "completed",
            {"digest": "sha256:" + "1" * 64},
        )
        release.record_promotion(ledger, "control-plane", "github-release", "completed")
        release.record_promotion(
            ledger, "inference-gateway", "image:inference-gateway", "failed", message="conflict"
        )
        self.assertEqual(ledger["targets"]["control-plane"]["status"], "completed")
        self.assertEqual(ledger["targets"]["inference-gateway"]["status"], "failed")
        self.assertEqual(
            ledger["targets"]["control-plane"]["events"][0]["identity"]["digest"],
            "sha256:" + "1" * 64,
        )

    def test_evidence_hashes_exact_assets(self) -> None:
        plan = release.make_plan(
            self.manifest,
            self.targets,
            {"forge": "0.8.1"},
            self.sha,
            "3",
            False,
        )
        with tempfile.TemporaryDirectory() as directory:
            assets = Path(directory)
            (assets / "forge.tar.gz").write_bytes(b"candidate")
            evidence = release.assemble_evidence(plan, assets)
        self.assertEqual(evidence["validation"]["status"], "passed")
        self.assertEqual(evidence["assets"][0]["path"], "forge.tar.gz")
        self.assertEqual(
            evidence["assets"][0]["sha256"],
            "dda18a0e21ae47c53b4309434cbc02ae8bf764fa83a6defbb719431242722aa7",
        )


if __name__ == "__main__":
    unittest.main()
