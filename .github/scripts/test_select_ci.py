#!/usr/bin/env python3

from __future__ import annotations

import json
from pathlib import Path
import subprocess
import tempfile
import unittest

from collect_changed_paths import collect_changed_paths
from select_ci import OUTPUTS, selection

ROOT = Path(__file__).resolve().parents[2]
FIXTURES = ROOT / ".github/ci/path-selection-fixtures.json"


class StaticCIPathSelectionTests(unittest.TestCase):
    def test_fixture_matrix(self) -> None:
        fixtures = json.loads(FIXTURES.read_text(encoding="utf-8"))
        for fixture in fixtures:
            with self.subTest(fixture=fixture["name"]):
                result = selection(fixture["paths"])
                self.assertEqual(
                    set(fixture["true"]),
                    {name for name in OUTPUTS if result[name]},
                )
                self.assertEqual(
                    fixture["images"],
                    [image["name"] for image in result["image_matrix"]],
                )
                self.assertEqual(bool(fixture["true"]), result["any"])

    def test_select_all_is_explicit_and_complete(self) -> None:
        result = selection([], select_all=True)
        self.assertTrue(all(result[name] for name in OUTPUTS))
        self.assertEqual(5, len(result["image_matrix"]))

    def test_unified_e2e_contract_changes_fan_out_static_owners(self) -> None:
        for path in (
            ".github/scripts/e2e.py",
            ".github/scripts/test_e2e.py",
            ".github/workflows/e2e.yml",
            "testkit/e2e/suite.go",
            "release/targets.json",
        ):
            with self.subTest(path=path):
                result = selection([path])
                if path == "release/targets.json":
                    # Release target authority remains covered by focused release
                    # checks; execution recipes are validated by test_e2e.py.
                    self.assertFalse(result["any"])
                else:
                    self.assertTrue(all(result[name] for name in OUTPUTS))


class ChangedPathCollectionTests(unittest.TestCase):
    def git(self, repo: Path, *args: str) -> str:
        completed = subprocess.run(
            ["git", "-C", str(repo), *args],
            check=True,
            text=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
        )
        return completed.stdout.strip()

    def create_repo(self, directory: str) -> tuple[Path, str]:
        repo = Path(directory)
        self.git(repo, "init", "--quiet")
        self.git(repo, "config", "user.email", "ci@example.com")
        self.git(repo, "config", "user.name", "CI Fixture")
        source = repo / "control-plane/internal/api/moved.go"
        source.parent.mkdir(parents=True)
        source.write_text("package api\n", encoding="utf-8")
        self.git(repo, "add", "-A")
        self.git(repo, "commit", "--quiet", "-m", "base")
        return repo, self.git(repo, "rev-parse", "HEAD")

    def test_deletion_only_change_retains_source_owner(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            repo, base_sha = self.create_repo(directory)
            (repo / "control-plane/internal/api/moved.go").unlink()
            self.git(repo, "add", "-A")
            self.git(repo, "commit", "--quiet", "-m", "delete")
            select_all, paths = collect_changed_paths(
                repo, "push", base_sha, self.git(repo, "rev-parse", "HEAD")
            )
            self.assertFalse(select_all)
            self.assertEqual(["control-plane/internal/api/moved.go"], paths)
            self.assertTrue(selection(paths)["control_plane"])

    def test_cross_owner_move_reports_source_and_destination(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            repo, base_sha = self.create_repo(directory)
            destination = repo / "forge/internal/moved.go"
            destination.parent.mkdir(parents=True)
            self.git(
                repo,
                "mv",
                "control-plane/internal/api/moved.go",
                "forge/internal/moved.go",
            )
            self.git(repo, "commit", "--quiet", "-m", "move")
            select_all, paths = collect_changed_paths(
                repo,
                "pull_request",
                base_sha,
                self.git(repo, "rev-parse", "HEAD"),
            )
            self.assertFalse(select_all)
            self.assertEqual(
                {
                    "control-plane/internal/api/moved.go",
                    "forge/internal/moved.go",
                },
                set(paths),
            )
            result = selection(paths)
            self.assertTrue(result["control_plane"])
            self.assertTrue(result["forge"])


class WorkflowContractTests(unittest.TestCase):
    def test_ci_and_e2e_share_changed_path_collector_and_stable_contexts(self) -> None:
        workflows = {
            "CI": (ROOT / ".github/workflows/ci.yml").read_text(),
            "E2E": (ROOT / ".github/workflows/e2e.yml").read_text(),
        }
        for name, workflow in workflows.items():
            with self.subTest(workflow=name):
                self.assertIn(
                    "python3 .github/scripts/collect_changed_paths.py", workflow
                )
                self.assertNotIn("git diff --name-only", workflow)
                self.assertIn(f"name: {name} / required", workflow)

    def test_e2e_uses_exact_head_compiled_matrices_and_result_reconciliation(self) -> None:
        workflow = (ROOT / ".github/workflows/e2e.yml").read_text()
        for value in (
            "ref: ${{ github.event.pull_request.head.sha || github.sha }}",
            "test \"$(git rev-parse HEAD)\" = \"$SOURCE_SHA\"",
            "fromJSON(needs.plan.outputs.kind_matrix)",
            "fromJSON(needs.plan.outputs.real_machine_matrix)",
            "python3 .github/scripts/e2e.py compose",
            "python3 .github/scripts/e2e.py validate-results",
            "export FORGE_E2E_REQUIRE_CAPACITY=true",
            "uses: ./.github/actions/setup-permanent-fixture",
            "group: iterabase-permanent-fixtures",
        ):
            self.assertIn(value, workflow)
        self.assertNotIn("DIGITALOCEAN_TOKEN", workflow)
        for stale in (
            "control-plane-kind:",
            "control-plane-execution-kind:",
            "charts-runtime:",
            "nightly-kind:",
            "nightly-real-machine:",
            "prepare_pr_workspace_runtime.sh",
        ):
            self.assertNotIn(stale, workflow)

    def test_fixture_cleanup_red_proof_is_serialized_and_retains_diagnostics(self) -> None:
        workflow = (ROOT / ".github/workflows/e2e.yml").read_text()
        cleanup = workflow.split("  cleanup-red-proof:\n", 1)[1].split(
            "\n  required:\n", 1
        )[0]
        for value in (
            "fixture_cleanup_red_proof:",
            "make test-e2e-gpu-broken-cleanup",
            "name: e2e-red-proof-cleanup",
            "group: iterabase-permanent-fixtures",
            "cancel-in-progress: false",
            "uses: ./.github/actions/setup-permanent-fixture",
        ):
            self.assertIn(value, workflow if value == "fixture_cleanup_red_proof:" else cleanup)
        self.assertNotIn("DIGITALOCEAN_TOKEN", cleanup)

    def test_harness_isolation_static_gate_remains_required(self) -> None:
        root_makefile = (ROOT / "Makefile").read_text()
        workflow = (ROOT / ".github/workflows/ci.yml").read_text()
        harness = workflow.split("  harness:\n", 1)[1].split(
            "\n  tool-runner:\n", 1
        )[0]
        self.assertIn("harness-isolation-test", root_makefile)
        self.assertIn("run: make harness-isolation-test", harness)
        self.assertNotIn("continue-on-error", harness)


if __name__ == "__main__":
    unittest.main()
