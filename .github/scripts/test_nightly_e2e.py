#!/usr/bin/env python3

from __future__ import annotations

from collections import Counter
import copy
import json
from pathlib import Path
import unittest

from nightly_e2e import (
    NightlyError,
    catalogue_scenarios,
    load_scenario_catalogue,
    make_plan,
    validate_results,
)

ROOT = Path(__file__).resolve().parents[2]
FIXTURE = ROOT / ".github/ci/nightly-selection-fixture.json"
SOURCE_SHA = "a" * 40


def successful_needs() -> dict[str, dict[str, str]]:
    return {
        "changes": {"result": "success"},
        "harness": {"result": "success"},
        "digitalocean-cpu": {"result": "skipped"},
        "digitalocean-gpu": {"result": "skipped"},
        "control-plane-kind": {"result": "skipped"},
        "control-plane-execution-kind": {"result": "skipped"},
        "charts-runtime": {"result": "skipped"},
        "nightly-kind": {"result": "success"},
        "nightly-real-machine": {"result": "success"},
    }


class NightlySelectionFixtureTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.fixture = json.loads(FIXTURE.read_text(encoding="utf-8"))

    def test_scheduled_selection_golden_fixture(self) -> None:
        plan = make_plan(self.fixture["catalogue"], SOURCE_SHA)
        expected = self.fixture["expected"]
        for field, value in expected.items():
            with self.subTest(field=field):
                self.assertEqual(value, plan[field])

    def test_every_compiled_required_scenario_is_selected_once_by_owner(self) -> None:
        catalogue = load_scenario_catalogue(ROOT)
        required = [
            scenario
            for scenario in catalogue_scenarios(catalogue)
            if scenario["metadata"].get("tier") in {"F2", "F3"}
        ]
        plan = make_plan(catalogue, SOURCE_SHA)
        selected = plan["kind_matrix"] + plan["real_machine_matrix"]

        expected_ids = [scenario["id"] for scenario in required]
        actual_ids = [scenario["id"] for scenario in selected]
        self.assertEqual(expected_ids, actual_ids)
        self.assertEqual(len(actual_ids), len(set(actual_ids)))
        self.assertEqual(
            Counter(scenario["suite"]["owner"] for scenario in required),
            Counter(plan["owner_totals"]),
        )
        for selected_scenario in selected:
            compiled = next(
                scenario
                for scenario in required
                if scenario["id"] == selected_scenario["id"]
            )
            self.assertEqual(
                compiled["suite"]["owner"], selected_scenario["owner"]
            )
            self.assertEqual("source", selected_scenario["fixture_mode"])

        selected_by_id = {scenario["id"]: scenario for scenario in selected}
        self.assertIn("control-plane/deployed-browser-journeys", selected_by_id)
        self.assertIn("forge/digitalocean-cpu", selected_by_id)
        self.assertIn("forge/digitalocean-gpu", selected_by_id)
        gpu = next(
            scenario for scenario in required if scenario["id"] == "forge/digitalocean-gpu"
        )
        self.assertIn("HOR-485", gpu["metadata"]["references"])
        self.assertTrue(selected_by_id["forge/digitalocean-gpu"]["mandatory"])

    def test_required_scenario_must_support_source_mode(self) -> None:
        catalogue = copy.deepcopy(self.fixture["catalogue"])
        catalogue["suites"][0]["scenarios"][1]["metadata"]["fixture_modes"] = [
            "candidate"
        ]
        with self.assertRaisesRegex(NightlyError, "does not support source"):
            make_plan(catalogue, SOURCE_SHA)

    def test_real_machine_capacity_must_be_mandatory(self) -> None:
        catalogue = copy.deepcopy(self.fixture["catalogue"])
        catalogue["suites"][2]["scenarios"][0]["metadata"][
            "mandatory_capacity"
        ] = False
        with self.assertRaisesRegex(NightlyError, "is not mandatory"):
            make_plan(catalogue, SOURCE_SHA)

    def test_duplicate_compiled_scenario_is_rejected(self) -> None:
        catalogue = copy.deepcopy(self.fixture["catalogue"])
        duplicate = copy.deepcopy(catalogue["suites"][0]["scenarios"][1])
        catalogue["suites"][1]["scenarios"].append(duplicate)
        with self.assertRaisesRegex(NightlyError, "repeats scenario"):
            make_plan(catalogue, SOURCE_SHA)


class NightlyAggregateTests(unittest.TestCase):
    def test_complete_schedule_passes(self) -> None:
        validate_results("schedule", successful_needs())

    def test_required_schedule_skip_is_incomplete(self) -> None:
        needs = successful_needs()
        needs["nightly-kind"]["result"] = "skipped"
        with self.assertRaisesRegex(NightlyError, '"nightly-kind":"skipped"'):
            validate_results("schedule", needs)

    def test_required_schedule_cancellation_is_incomplete(self) -> None:
        needs = successful_needs()
        needs["nightly-real-machine"]["result"] = "cancelled"
        with self.assertRaisesRegex(
            NightlyError, '"nightly-real-machine":"cancelled"'
        ):
            validate_results("schedule", needs)

    def test_required_schedule_failure_is_incomplete(self) -> None:
        needs = successful_needs()
        needs["harness"]["result"] = "failure"
        with self.assertRaisesRegex(NightlyError, '"harness":"failure"'):
            validate_results("schedule", needs)

    def test_non_schedule_keeps_unselected_jobs_optional(self) -> None:
        needs = successful_needs()
        needs["nightly-kind"]["result"] = "skipped"
        needs["nightly-real-machine"]["result"] = "skipped"
        validate_results("pull_request", needs)

    def test_complete_manual_rehearsal_uses_schedule_requirements(self) -> None:
        needs = successful_needs()
        validate_results("workflow_dispatch", needs, complete_catalogue=True)
        needs["nightly-kind"]["result"] = "skipped"
        with self.assertRaisesRegex(NightlyError, '"nightly-kind":"skipped"'):
            validate_results("workflow_dispatch", needs, complete_catalogue=True)


class NightlyWorkflowContractTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.workflow = (ROOT / ".github/workflows/e2e.yml").read_text(
            encoding="utf-8"
        )

    def test_schedule_uses_compiled_dynamic_matrices(self) -> None:
        self.assertIn("complete_catalogue:", self.workflow)
        self.assertIn("python3 .github/scripts/nightly_e2e.py plan", self.workflow)
        self.assertIn(
            "fromJSON(needs.changes.outputs.nightly_kind_matrix)", self.workflow
        )
        self.assertIn(
            "fromJSON(needs.changes.outputs.nightly_real_machine_matrix)",
            self.workflow,
        )
        self.assertIn("name: nightly-e2e-plan", self.workflow)
        self.assertIn("inputs.complete_catalogue == true", self.workflow)

    def test_mandatory_capacity_and_aggregate_are_fail_closed(self) -> None:
        real_machine = self.workflow.split("  nightly-real-machine:\n", 1)[1].split(
            "\n  required:\n", 1
        )[0]
        self.assertIn('FORGE_E2E_REQUIRE_CAPACITY: "true"', real_machine)
        self.assertIn("test -n \"$DIGITALOCEAN_TOKEN\"", real_machine)
        self.assertIn("if: failure() || cancelled()", real_machine)
        required = self.workflow.split("  required:\n", 1)[1]
        self.assertIn("- nightly-kind", required)
        self.assertIn("- nightly-real-machine", required)
        self.assertIn("nightly_e2e.py validate-results", required)


if __name__ == "__main__":
    unittest.main()
