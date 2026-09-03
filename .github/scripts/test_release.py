#!/usr/bin/env python3

from __future__ import annotations

import copy
import json
import os
from pathlib import Path
import tempfile
import unittest

import release

ROOT = Path(__file__).resolve().parents[2]
SOURCE_SHA = "a" * 40


class ReleaseContractTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.contract = release.load_json(ROOT / "release" / "targets.json")
        cls.catalogue = release.load_scenario_catalogue(ROOT)

    def test_target_and_recipe_contract_is_valid(self) -> None:
        release.validate_contract(self.contract, ROOT, self.catalogue)
        self.assertEqual(4, self.contract["schema_version"])
        self.assertEqual(release.TARGET_NAMES, tuple(self.contract["targets"]))
        recipes = self.contract["artifact_recipes"]
        for target, definition in self.contract["targets"].items():
            self.assertTrue(definition["artifacts"])
            for artifact in definition["artifacts"]:
                self.assertEqual(target, recipes[artifact]["target"])
        self.assertEqual(
            "v2.12.7", recipes["forge-binary"]["goreleaser_version"]
        )

    def test_unknown_duplicate_and_empty_release_intent_fails(self) -> None:
        for value in ("", "control-plane,control-plane", "unknown"):
            with self.subTest(value=value), self.assertRaises(release.ReleaseError):
                release.parse_targets(value)

    def test_target_order_is_canonical(self) -> None:
        self.assertEqual(
            ["control-plane", "forge", "iterabase-platform-chart"],
            release.parse_targets(
                "iterabase-platform-chart,control-plane,forge"
            ),
        )

    def test_candidate_plan_uses_recipe_backed_artifacts_and_compiled_execution(self) -> None:
        plan = release.make_plan(
            self.contract,
            ["control-plane", "iterabase-platform-chart"],
            SOURCE_SHA,
            "123",
            ROOT,
            self.catalogue,
            run_attempt="2",
        )
        self.assertEqual("source-run-attempt-v1", plan["candidate_alias_scheme"])
        self.assertEqual(f"{SOURCE_SHA}-123-2", plan["image_matrix"][0]["candidate_tag"])
        self.assertEqual(
            plan["selected_scenarios"],
            plan["execution_plan"]["selected_scenario_ids"],
        )
        self.assertEqual(2, plan["execution_plan"]["schema_version"])
        self.assertTrue(plan["kind_matrix"])
        self.assertTrue(plan["real_machine_matrix"])
        self.assertTrue(
            all(item["recipe_sha256"] for item in plan["image_matrix"])
        )
        platform = next(
            item
            for item in plan["chart_matrix"]
            if item["chart"] == "iterabase-platform"
        )
        self.assertEqual(["cert-manager-substrate"], platform["companions"])
        self.assertEqual(1, len(platform["companion_recipes"]))
        self.assertEqual(
            ["runtime-fixture-image"],
            [item["artifact"] for item in plan["execution_plan"]["artifact_build_matrix"]],
        )

    def test_every_release_target_selects_a_nonempty_conservative_union(self) -> None:
        for target in release.TARGET_NAMES:
            with self.subTest(target=target):
                plan = release.make_plan(
                    self.contract,
                    [target],
                    SOURCE_SHA,
                    "1",
                    ROOT,
                    self.catalogue,
                )
                self.assertTrue(plan["selected_scenarios"])
                self.assertEqual(
                    len(plan["selected_scenarios"]),
                    len(set(plan["selected_scenarios"])),
                )
                for scenario in plan["execution_plan"]["scenario_matrix"]:
                    selected = [
                        artifact
                        for artifact in scenario["artifacts"]
                        if self.contract["artifact_recipes"][artifact["name"]].get(
                            "target"
                        )
                        == target
                    ]
                    if target in next(
                        item["metadata"]["release_targets"]
                        for suite in self.catalogue["suites"]
                        for item in suite["scenarios"]
                        if item["id"] == scenario["id"]
                    ):
                        self.assertTrue(selected)
                        self.assertTrue(
                            any(
                                item["custody"] == "selected-candidate"
                                for item in selected
                            )
                        )

    def test_candidate_alias_is_run_attempt_scoped_and_immutable(self) -> None:
        first = release.candidate_image_alias(SOURCE_SHA, "10", "1")
        second = release.candidate_image_alias(SOURCE_SHA, "10", "2")
        self.assertNotEqual(first, second)
        for values in (("short", "1", "1"), (SOURCE_SHA, "0", "1"), (SOURCE_SHA, "1", "x")):
            with self.assertRaises(release.ReleaseError):
                release.candidate_image_alias(*values)

    def test_recipe_or_catalogue_drift_fails_contract_validation(self) -> None:
        contract = copy.deepcopy(self.contract)
        contract["targets"]["control-plane"]["artifacts"].append("missing")
        with self.assertRaises(release.ReleaseError):
            release.validate_contract(contract, ROOT, self.catalogue)

        catalogue = copy.deepcopy(self.catalogue)
        scenario = next(
            scenario
            for suite in catalogue["suites"]
            for scenario in suite["scenarios"]
            if scenario["metadata"]["tier"] == "F2"
        )
        scenario["metadata"]["required_artifacts"] = []
        with self.assertRaises(release.ReleaseError):
            release.validate_contract(self.contract, ROOT, catalogue)


class CandidateJobTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.contract = release.load_json(ROOT / "release" / "targets.json")
        cls.catalogue = release.load_scenario_catalogue(ROOT)
        cls.plan = release.make_plan(
            cls.contract,
            ["control-plane", "forge", "iterabase-platform-chart"],
            SOURCE_SHA,
            "123",
            ROOT,
            cls.catalogue,
        )

    def needs(self) -> dict[str, dict[str, str]]:
        return {
            name: {"result": "success" if selected else "skipped"}
            for name, selected in release.candidate_job_selection(self.plan).items()
        }

    def test_selected_job_set_is_exact_and_fail_closed(self) -> None:
        selected = release.candidate_job_selection(self.plan)
        self.assertTrue(selected["runtime-artifacts"])
        self.assertTrue(selected["kind-candidates"])
        self.assertTrue(selected["real-machine-candidates"])
        self.assertNotIn("charts-runtime", selected)
        results = release.validate_candidate_job_results(self.plan, self.needs())
        self.assertEqual(set(selected), set(results))

    def test_selected_skip_failure_cancel_and_job_set_drift_fail(self) -> None:
        for status in ("skipped", "failure", "cancelled"):
            needs = self.needs()
            needs["kind-candidates"]["result"] = status
            with self.subTest(status=status), self.assertRaises(release.ReleaseError):
                release.validate_candidate_job_results(self.plan, needs)
        needs = self.needs()
        needs.pop("runtime-artifacts")
        with self.assertRaises(release.ReleaseError):
            release.validate_candidate_job_results(self.plan, needs)
        needs = self.needs()
        needs["unexpected"] = {"result": "success"}
        with self.assertRaises(release.ReleaseError):
            release.validate_candidate_job_results(self.plan, needs)


class CandidateAssetTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.contract = release.load_json(ROOT / "release" / "targets.json")
        cls.catalogue = release.load_scenario_catalogue(ROOT)

    def test_image_metadata_binds_source_alias_recipe_and_digest(self) -> None:
        plan = release.make_plan(
            self.contract,
            ["control-plane"],
            SOURCE_SHA,
            "123",
            ROOT,
            self.catalogue,
        )
        with tempfile.TemporaryDirectory() as value:
            assets = Path(value)
            images = assets / "images"
            images.mkdir()
            for image in plan["image_matrix"]:
                metadata = {
                    "schema_version": 2,
                    "artifact_type": "image",
                    "name": image["name"],
                    "target": image["target"],
                    "repository": image["repository"],
                    "candidate_tag": image["candidate_tag"],
                    "version": image["version"],
                    "digest": "sha256:" + "b" * 64,
                    "source_sha": SOURCE_SHA,
                    "recipe_sha256": image["recipe_sha256"],
                }
                (images / f"candidate-{image['name']}.json").write_text(
                    json.dumps(metadata) + "\n", encoding="utf-8"
                )
            release.validate_candidate_assets(plan, assets)
            metadata_path = images / "candidate-control-plane.json"
            metadata = json.loads(metadata_path.read_text())
            metadata["source_sha"] = "c" * 40
            metadata_path.write_text(json.dumps(metadata) + "\n")
            with self.assertRaises(release.ReleaseError):
                release.validate_candidate_assets(plan, assets)

    def test_selected_platform_companion_is_mandatory(self) -> None:
        plan = release.make_plan(
            self.contract,
            ["iterabase-platform-chart"],
            SOURCE_SHA,
            "123",
            ROOT,
            self.catalogue,
        )
        chart_plan = plan["chart_matrix"][0]
        with tempfile.TemporaryDirectory() as value:
            assets = Path(value)
            charts = assets / "charts"
            charts.mkdir()
            (charts / f"{chart_plan['chart']}-{chart_plan['version']}.tgz").write_bytes(b"platform")
            (charts / f"checksums-{chart_plan['chart']}.txt").write_text("placeholder\n")
            (charts / f"candidate-chart-{chart_plan['chart']}.json").write_text(
                json.dumps(
                    {
                        "schema_version": 2,
                        "artifact_type": "chart",
                        "target": chart_plan["target"],
                        "chart": chart_plan["chart"],
                        "version": chart_plan["version"],
                        "source_sha": SOURCE_SHA,
                        "recipe_sha256": chart_plan["recipe_sha256"],
                    }
                )
                + "\n"
            )
            with self.assertRaisesRegex(release.ReleaseError, "cert-manager-substrate"):
                release.validate_candidate_assets(plan, assets)

    def test_asset_records_detect_exact_bytes(self) -> None:
        with tempfile.TemporaryDirectory() as value:
            directory = Path(value)
            (directory / "one").write_bytes(b"one")
            first = release.asset_records(directory)
            (directory / "one").write_bytes(b"two")
            second = release.asset_records(directory)
            self.assertNotEqual(first, second)


class ReleaseWorkflowContractTests(unittest.TestCase):
    def test_candidate_uses_unified_plan_composer_results_and_capacity_groups(self) -> None:
        workflow = (ROOT / ".github/workflows/release-candidate.yml").read_text(
            encoding="utf-8"
        )
        for value in (
            "python3 .github/scripts/e2e.py resolve-baselines",
            "python3 .github/scripts/e2e.py compose",
            "python3 .github/scripts/e2e.py validate-results",
            "candidate-result-${{ matrix.artifact }}",
            "uses: ./.github/actions/setup-permanent-fixture",
            "group: iterabase-permanent-fixtures",
            "cancel-in-progress: false",
            "export FORGE_E2E_REQUIRE_CAPACITY=true",
        ):
            self.assertIn(value, workflow)
        self.assertNotIn("DIGITALOCEAN_TOKEN", workflow)
        for stale in (
            "prepare_candidate_runtime.sh",
            "charts-runtime.yml",
            "historical mandatory CPU+GPU",
        ):
            self.assertNotIn(stale, workflow)

    def test_branch_rehearsal_is_explicit_exact_and_non_promotable(self) -> None:
        candidate = (ROOT / ".github/workflows/release-candidate.yml").read_text()
        promotion = (ROOT / ".github/workflows/release-promote.yml").read_text()
        for value in (
            "rehearsal:",
            'test "$REQUESTED_SHA" = "$DISPATCH_SHA"',
            "if: inputs.rehearsal != true",
        ):
            self.assertIn(value, candidate)
        self.assertEqual(2, candidate.count("if: inputs.rehearsal != true"))
        self.assertIn("test \"$(jq -r '.head_branch' <<<\"$run\")\" = master", promotion)
        self.assertIn('git merge-base --is-ancestor "$source_sha" origin/master', promotion)

    def test_candidate_recipes_match_production_authority(self) -> None:
        workflow = (ROOT / ".github/workflows/release-candidate.yml").read_text()
        self.assertIn("matrix.labels_text", workflow)
        self.assertIn("matrix.build_args_text", workflow)
        self.assertIn("recipe_sha256", workflow)
        self.assertIn("goreleaser/goreleaser-action", workflow)
        self.assertIn("bash charts/scripts/build-chart-dependency.sh", workflow)

    def test_promotion_remains_protected_and_never_rebuilds(self) -> None:
        workflow = (ROOT / ".github/workflows/release-promote.yml").read_text(
            encoding="utf-8"
        )
        self.assertIn("environment: release", workflow)
        self.assertIn("verify-candidate", workflow)
        self.assertIn("check_promotion_destinations.sh", workflow)
        self.assertNotIn("docker/build-push-action", workflow)
        self.assertNotIn("docker build -", workflow)
        self.assertNotIn("goreleaser/goreleaser-action", workflow.lower())
        self.assertNotIn("helm package", workflow)

    def test_release_only_manual_dispatch_and_no_push_publication(self) -> None:
        for workflow in ("release-candidate.yml", "release-promote.yml"):
            content = (ROOT / ".github/workflows" / workflow).read_text()
            self.assertIn("workflow_dispatch:", content)
            self.assertNotIn("push:\n", content)


if __name__ == "__main__":
    unittest.main()
