#!/usr/bin/env python3

import json
from pathlib import Path
import subprocess
import tempfile
import unittest

from collect_changed_paths import collect_changed_paths
from select_ci import OUTPUTS, selection


ROOT = Path(__file__).resolve().parents[2]
FIXTURES = ROOT / ".github/ci/path-selection-fixtures.json"


class PathSelectionFixtures(unittest.TestCase):
    def test_fixture_matrix(self) -> None:
        fixtures = json.loads(FIXTURES.read_text())
        for fixture in fixtures:
            with self.subTest(fixture["name"]):
                result = selection(fixture["paths"])
                actual_true = {name for name in OUTPUTS if result[name]}
                self.assertEqual(set(fixture["true"]), actual_true)
                self.assertEqual(
                    fixture["images"],
                    [image["name"] for image in result["image_matrix"]],
                )
                self.assertEqual(bool(fixture["true"]), result["any"])

    def test_select_all_is_explicit_and_complete(self) -> None:
        result = selection([], select_all=True)
        self.assertTrue(all(result[name] for name in OUTPUTS))
        self.assertEqual(5, len(result["image_matrix"]))


class ChangedPathCollectionFixtures(unittest.TestCase):
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
        source.write_text("package api\n")
        self.git(repo, "add", "-A")
        self.git(repo, "commit", "--quiet", "-m", "base")
        return repo, self.git(repo, "rev-parse", "HEAD")

    def test_deletion_only_change_retains_source_owner(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            repo, base_sha = self.create_repo(directory)
            (repo / "control-plane/internal/api/moved.go").unlink()
            self.git(repo, "add", "-A")
            self.git(repo, "commit", "--quiet", "-m", "delete source")
            head_sha = self.git(repo, "rev-parse", "HEAD")

            select_all, paths = collect_changed_paths(
                repo, "push", base_sha, head_sha
            )

            self.assertFalse(select_all)
            self.assertEqual(["control-plane/internal/api/moved.go"], paths)
            self.assertTrue(selection(paths)["control_plane"])

    def test_cross_owner_move_reports_source_and_destination(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            repo, base_sha = self.create_repo(directory)
            (repo / "docs").mkdir()
            self.git(
                repo,
                "mv",
                "control-plane/internal/api/moved.go",
                "docs/moved.md",
            )
            self.git(repo, "commit", "--quiet", "-m", "move source to docs")
            head_sha = self.git(repo, "rev-parse", "HEAD")

            select_all, paths = collect_changed_paths(
                repo, "pull_request", base_sha, head_sha
            )

            self.assertFalse(select_all)
            self.assertEqual(
                {"control-plane/internal/api/moved.go", "docs/moved.md"},
                set(paths),
            )
            self.assertTrue(selection(paths)["control_plane"])

    def test_workflows_share_collector_and_publish_exact_required_names(self) -> None:
        workflows = {
            "CI": (ROOT / ".github/workflows/ci.yml").read_text(),
            "E2E": (ROOT / ".github/workflows/e2e.yml").read_text(),
        }
        for workflow_name, workflow in workflows.items():
            with self.subTest(workflow=workflow_name):
                self.assertIn(
                    "python3 .github/scripts/collect_changed_paths.py", workflow
                )
                self.assertNotIn("git diff --name-only", workflow)
                self.assertIn(f"name: {workflow_name} / required", workflow)

    def test_pr_has_required_exact_head_workspace_job(self) -> None:
        workflow = (ROOT / ".github/workflows/e2e.yml").read_text()
        job = workflow.split("  digitalocean-workspace:\n", 1)[1].split(
            "\n  digitalocean-gpu:\n", 1
        )[0]
        for contract in (
            "needs.changes.outputs.charts == 'true' ||",
            "needs.changes.outputs.forge_real_e2e == 'true' ||",
            "needs.changes.outputs.control_plane == 'true') &&",
            "ref: ${{ github.event.pull_request.head.sha || github.sha }}",
            "timeout-minutes: 130",
            "group: e2e-digitalocean-workspace",
            ".github/scripts/prepare_pr_workspace_runtime.sh",
            "run: make test-e2e-workspace",
            'FORGE_E2E_REQUIRE_CAPACITY: "true"',
            "ITERABASE_E2E_SOURCE_SHA: ${{ github.event.pull_request.head.sha || github.sha }}",
        ):
            self.assertIn(contract, job)
        for path in (
            "forge/test/e2e/overlay_test.go",
            "forge/internal/lifecycle/storage.go",
            "control-plane/harness/src/storage-health.ts",
        ):
            with self.subTest(workspace_path=path):
                result = selection([path])
                self.assertTrue(result["forge_real_e2e"])
        required = workflow.split("  required:\n", 1)[1]
        self.assertIn("- digitalocean-workspace", required)
        self.assertNotIn("digitalocean-rwx", workflow)
        helper = (ROOT / ".github/scripts/prepare_pr_workspace_runtime.sh").read_text()
        for chart in ("iterabase-platform", "cert-manager-substrate"):
            self.assertIn(chart, helper)
        self.assertNotIn("rwx-storage-substrate", helper)
        for variable in (
            "FORGE_E2E_PLATFORM_CHART_ARCHIVE",
            "FORGE_E2E_SUBSTRATE_CHART_ARCHIVE",
            "FORGE_E2E_RUNTIME_IMAGE_ARCHIVE",
            "control-plane-runtime-fixture",
        ):
            self.assertIn(variable, helper)

    def test_control_plane_deployed_scenarios_are_required_when_selected(self) -> None:
        workflow = (ROOT / ".github/workflows/e2e.yml").read_text()
        job = workflow.split("  control-plane-kind:\n", 1)[1].split(
            "\n  published-compatibility:\n", 1
        )[0]
        for target in (
            "test-e2e-identity",
            "test-e2e-work",
            "test-e2e-artifact",
            "test-e2e-browser",
        ):
            self.assertIn(f"target: {target}", job)
        self.assertIn("playwright/package-lock.json", job)
        self.assertIn("--with-deps chromium", job)
        execution_job = workflow.split("  control-plane-execution-kind:\n", 1)[1].split(
            "\n  published-compatibility:\n", 1
        )[0]
        self.assertIn("run: make test-e2e-execution", execution_job)
        required = workflow.split("  required:\n", 1)[1]
        self.assertIn("- control-plane-kind", required)
        self.assertIn("- control-plane-execution-kind", required)

    def test_control_plane_gates_are_fresh_and_isolation_is_required(self) -> None:
        component_makefile = (ROOT / "control-plane/Makefile").read_text()
        root_makefile = (ROOT / "Makefile").read_text()
        workflow = (ROOT / ".github/workflows/ci.yml").read_text()
        harness_job = workflow.split("  harness:\n", 1)[1].split(
            "\n  tool-runner:\n", 1
        )[0]

        self.assertIn(
            "go test -count=1 -coverprofile cover.out ./...", component_makefile
        )
        self.assertIn("harness-isolation-test", root_makefile)
        self.assertIn("run: make harness-isolation-test", harness_job)
        self.assertNotIn("continue-on-error", harness_job)


if __name__ == "__main__":
    unittest.main()
