#!/usr/bin/env python3

from pathlib import Path
import re
import tempfile
import unittest

from content_digest import content_digest


ROOT = Path(__file__).resolve().parents[2]


class CacheContractTests(unittest.TestCase):
    def test_lock_mutation_changes_content_key(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            lock = Path(directory) / "package-lock.json"
            lock.write_text('{"lockfileVersion":3}\n')
            before = content_digest([lock])
            lock.write_text('{"lockfileVersion":3,"packages":{"x":{}}}\n')
            self.assertNotEqual(before, content_digest([lock]))

    def test_source_mutation_changes_content_identity(self) -> None:
        with tempfile.TemporaryDirectory() as directory:
            dockerfile = Path(directory) / "Dockerfile"
            source = Path(directory) / "main.go"
            dockerfile.write_text("FROM scratch\nCOPY main /main\n")
            source.write_text("package main\n")
            before = content_digest([dockerfile, source])
            source.write_text('package main\nconst version = "changed"\n')
            self.assertNotEqual(before, content_digest([dockerfile, source]))

    def test_cache_actions_have_no_fallback_restore_keys(self) -> None:
        actions = list((ROOT / ".github/actions").glob("*/action.yml"))
        self.assertTrue(actions)
        for action in actions:
            with self.subTest(action=action):
                self.assertNotIn("restore-keys:", action.read_text())

    def test_all_workspace_sums_define_go_cache(self) -> None:
        workflow = (ROOT / ".github/workflows/ci.yml").read_text()
        for go_sum in (
            "control-plane/go.sum",
            "inference-gateway/go.sum",
            "forge/go.sum",
            "forge/test/e2e/go.sum",
        ):
            self.assertIn(go_sum, workflow)

    def test_external_actions_are_commit_pinned(self) -> None:
        workflow_files = list((ROOT / ".github").glob("**/*.yml"))
        references = []
        for workflow_file in workflow_files:
            references.extend(
                re.findall(r"uses:\s+([^\s]+)", workflow_file.read_text())
            )
        external = [reference for reference in references if not reference.startswith("./")]
        self.assertTrue(external)
        for reference in external:
            with self.subTest(reference=reference):
                self.assertRegex(reference, r"^[^@]+@[0-9a-f]{40}$")

    def test_forbidden_mutable_state_is_not_cached(self) -> None:
        cache_actions = "\n".join(
            action.read_text()
            for action in (ROOT / ".github/actions").glob("*/action.yml")
        )
        for forbidden in (
            ".kube",
            "node_modules",
            "charts/*/charts",
            "postgres",
            "test-results",
            "credentials",
        ):
            with self.subTest(forbidden=forbidden):
                self.assertNotIn(forbidden, cache_actions)


if __name__ == "__main__":
    unittest.main()
