#!/usr/bin/env python3

from __future__ import annotations

import copy
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

import release
import retained_release

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
        self.assertEqual(4, plan["schema_version"])
        self.assertEqual(release.CANDIDATE_REPOSITORY, plan["candidate_repository"])
        self.assertEqual(release.CANDIDATE_WORKFLOW, plan["candidate_workflow"])
        self.assertEqual(release.CANDIDATE_EVENT, plan["candidate_event"])
        self.assertEqual(SOURCE_SHA, plan["candidate_control_sha"])
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

    def test_candidate_run_authority_rejects_repository_workflow_event_and_control_drift(self) -> None:
        mutations = {
            "repository": "other/repository",
            "workflow": ".github/workflows/other.yml",
            "event": "push",
            "control_sha": "short",
        }
        for field, value in mutations.items():
            with self.subTest(field=field), self.assertRaises(release.ReleaseError):
                release.make_plan(
                    self.contract,
                    ["forge"],
                    SOURCE_SHA,
                    "123",
                    ROOT,
                    self.catalogue,
                    **{field: value},
                )

    def test_live_candidate_run_must_match_every_retained_authority_field(self) -> None:
        plan = {
            "candidate_repository": release.CANDIDATE_REPOSITORY,
            "candidate_workflow": release.CANDIDATE_WORKFLOW,
            "candidate_event": release.CANDIDATE_EVENT,
            "candidate_control_sha": SOURCE_SHA,
            "run_id": "123",
            "run_attempt": "2",
        }
        run = {
            "name": "Release candidate",
            "repository": {"full_name": release.CANDIDATE_REPOSITORY},
            "head_repository": {"full_name": release.CANDIDATE_REPOSITORY},
            "path": release.CANDIDATE_WORKFLOW,
            "event": release.CANDIDATE_EVENT,
            "head_sha": SOURCE_SHA,
            "id": 123,
            "run_attempt": 2,
            "head_branch": "master",
            "conclusion": "success",
        }
        with tempfile.TemporaryDirectory() as value:
            directory = Path(value)
            plan_path = directory / "plan.json"
            plan_path.write_text(json.dumps(plan))
            fake_gh = directory / "gh"
            fake_gh.write_text("#!/bin/sh\nprintf '%s' \"$RUN_JSON\"\n")
            fake_gh.chmod(0o755)
            env = {**os.environ, "PATH": f"{directory}:{os.environ['PATH']}"}

            def verify(candidate_run: dict) -> subprocess.CompletedProcess:
                return subprocess.run(
                    [str(ROOT / ".github/scripts/verify_candidate_run.sh"), "123", str(plan_path), release.CANDIDATE_REPOSITORY],
                    env={**env, "RUN_JSON": json.dumps(candidate_run)},
                    text=True,
                    stdout=subprocess.PIPE,
                    stderr=subprocess.PIPE,
                )

            self.assertEqual(0, verify(run).returncode)
            mutations = (
                ("name", None),
                ("repository", "full_name"),
                ("head_repository", "full_name"),
                ("path", None),
                ("event", None),
                ("head_sha", None),
                ("run_attempt", None),
                ("head_branch", None),
                ("conclusion", None),
            )
            for field, nested in mutations:
                changed = copy.deepcopy(run)
                if nested:
                    changed[field][nested] = "changed"
                else:
                    changed[field] = "changed"
                with self.subTest(field=field):
                    self.assertNotEqual(0, verify(changed).returncode)

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

    def test_validate_jobs_cli_reads_bounded_file_inputs(self) -> None:
        with tempfile.TemporaryDirectory() as value:
            directory = Path(value)
            plan = directory / "plan.json"
            needs = directory / "needs.json"
            plan.write_text(json.dumps(self.plan), encoding="utf-8")
            needs.write_text(json.dumps(self.needs()), encoding="utf-8")
            completed = subprocess.run(
                [
                    sys.executable,
                    str(ROOT / ".github/scripts/release.py"),
                    "validate-jobs",
                    "--plan",
                    str(plan),
                    "--needs",
                    str(needs),
                ],
                check=True,
                text=True,
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
            )
            self.assertIn("candidate validation results:", completed.stdout)

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


class ReleaseManifestTests(unittest.TestCase):
    def fixture(self, directory: Path) -> tuple[dict, Path]:
        candidate = directory / "candidate"
        images = candidate / "assets/images"
        images.mkdir(parents=True)
        plan = {
            "source_sha": SOURCE_SHA,
            "run_id": "123",
            "targets": ["control-plane"],
            "releases": [{"target": "control-plane", "version": "1.2.3", "production_tag": "control-plane-v1.2.3", "artifact_types": ["image"]}],
            "image_matrix": [{"target": "control-plane", "name": "control-plane"}],
            "chart_matrix": [],
        }
        (candidate / "candidate-plan.json").write_text(json.dumps(plan) + "\n")
        (candidate / "candidate-evidence.json").write_text("{}\n")
        (images / "candidate-control-plane.json").write_text('{"digest":"sha256:test"}\n')
        (images / "candidate-control-plane.spdx.json").write_text("{}\n")
        return plan, candidate

    def test_complete_manifest_binds_candidate_run_source_and_every_member(self) -> None:
        with tempfile.TemporaryDirectory() as value:
            plan, candidate = self.fixture(Path(value))
            manifest = release.release_manifest(plan, candidate, "control-plane")
            self.assertEqual(2, manifest["schema_version"])
            self.assertEqual("123", manifest["candidate_run_id"])
            self.assertEqual(SOURCE_SHA, manifest["source_sha"])
            self.assertEqual("control-plane-v1.2.3", manifest["release_metadata"]["title"])
            self.assertIn("candidate run `123`", manifest["release_metadata"]["notes"])
            self.assertEqual(SOURCE_SHA, manifest["release_metadata"]["target_commitish"])
            self.assertFalse(manifest["release_metadata"]["prerelease"])
            self.assertEqual(
                {"candidate-plan.json", "candidate-evidence.json", "candidate-control-plane.json", "candidate-control-plane.spdx.json"},
                {item["name"] for item in manifest["assets"]},
            )
            release.validate_release_manifest(manifest, candidate, plan)

    def test_missing_extra_duplicate_and_conflicting_manifest_members_fail(self) -> None:
        for mutation in ("missing", "extra", "duplicate", "conflicting", "metadata"):
            with self.subTest(mutation=mutation), tempfile.TemporaryDirectory() as value:
                plan, candidate = self.fixture(Path(value))
                manifest = release.release_manifest(plan, candidate, "control-plane")
                if mutation == "missing":
                    manifest["assets"].pop()
                elif mutation == "extra":
                    manifest["assets"].append({"name": "extra", "path": "extra", "size": 1, "sha256": "a" * 64})
                elif mutation == "duplicate":
                    manifest["assets"].append(copy.deepcopy(manifest["assets"][0]))
                elif mutation == "conflicting":
                    (candidate / "assets/images/candidate-control-plane.json").write_text("changed\n")
                else:
                    manifest["release_metadata"]["title"] = "substituted"
                with self.assertRaises(release.ReleaseError):
                    release.validate_release_manifest(manifest, candidate, plan)


class ReleaseSecurityAuditTests(unittest.TestCase):
    def ruleset(self) -> dict:
        return {
            "id": 123,
            "name": "protected release tags",
            "target": "tag",
            "enforcement": "active",
            "bypass_actors": [
                {"actor_type": "DeployKey", "bypass_mode": "always"}
            ],
            "rules": [
                {"type": value}
                for value in ("creation", "deletion", "non_fast_forward", "update")
            ],
            "conditions": {
                "ref_name": {
                    "include": [
                        "refs/tags/control-plane-v*",
                        "refs/tags/inference-gateway-v*",
                        "refs/tags/forge-v*",
                        "refs/tags/control-plane-*",
                        "refs/tags/inference-gateway-*",
                        "refs/tags/iterabase-platform-*",
                        "refs/tags/dry-run/**",
                    ],
                    "exclude": [],
                }
            },
        }

    def run_audit(
        self,
        ruleset: dict,
        *,
        admin: bool,
        immutable_response: str | None = '{"enabled": true, "enforced_by_owner": false}',
    ) -> subprocess.CompletedProcess[str]:
        with tempfile.TemporaryDirectory() as value:
            directory = Path(value)
            fake_gh = directory / "gh"
            fake_gh.write_text(
                """#!/usr/bin/env python3
import json
import os
import sys

repository = "example/iterabase-mono"
endpoint = next(value for value in sys.argv[2:] if not value.startswith("-"))
ruleset = json.loads(os.environ["RULESET_JSON"])
immutable_endpoint = f"repos/{repository}/immutable-releases"
if endpoint == immutable_endpoint:
    if os.environ["AUDIT_ADMIN_ENDPOINTS"] != "true":
        print("non-admin audit requested the admin-only immutable release setting", file=sys.stderr)
        sys.exit(3)
    response = os.environ.get("IMMUTABLE_RESPONSE")
    if response is None:
        print("immutable release setting unavailable", file=sys.stderr)
        sys.exit(4)
    print(response, end="")
    sys.exit(0)
responses = {
    f"repos/{repository}/keys": [{
        "read_only": False,
        "title": "iterabase protected release tags (validated)",
        "key": "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIBGpAToV5oV2LesN/Kqsim3Nn0OBUItH9TocZOzRd/rz",
    }],
    f"repos/{repository}/environments/release": {
        "name": "release",
        "deployment_branch_policy": {
            "protected_branches": False,
            "custom_branch_policies": True,
        },
        "protection_rules": [{
            "type": "required_reviewers",
            "prevent_self_review": False,
            "reviewers": [{
                "type": "User",
                "reviewer": {"login": "nunocgoncalves"},
            }],
        }],
    },
    f"repos/{repository}/environments/release/deployment-branch-policies": {
        "branch_policies": [{"name": "master", "type": "branch"}],
    },
    f"repos/{repository}/rulesets": [{
        "id": 123,
        "name": "protected release tags",
        "target": "tag",
        "enforcement": "active",
    }],
    f"repos/{repository}/rulesets/123": ruleset,
    f"repos/{repository}/actions/permissions/workflow": {
        "default_workflow_permissions": "read",
        "can_approve_pull_request_reviews": False,
    },
    f"repos/{repository}/collaborators?affiliation=all&per_page=100": [{
        "login": "nunocgoncalves",
        "permissions": {"admin": True, "maintain": True, "push": True},
    }],
}
if endpoint not in responses:
    print(f"unexpected endpoint: {endpoint}", file=sys.stderr)
    sys.exit(2)
json.dump(responses[endpoint], sys.stdout)
""",
                encoding="utf-8",
            )
            fake_gh.chmod(0o755)
            env = {
                **os.environ,
                "PATH": f"{directory}:{os.environ['PATH']}",
                "RULESET_JSON": json.dumps(ruleset),
                "AUDIT_ADMIN_ENDPOINTS": str(admin).lower(),
                "AUDIT_REPOSITORY_SECRETS": "false",
                "RELEASE_REVIEWER": "nunocgoncalves",
            }
            env.pop("RELEASE_TAG_KEY_FILE", None)
            if immutable_response is None:
                env.pop("IMMUTABLE_RESPONSE", None)
            else:
                env["IMMUTABLE_RESPONSE"] = immutable_response
            return subprocess.run(
                [
                    str(ROOT / ".github/scripts/audit_release_security.sh"),
                    "example/iterabase-mono",
                ],
                cwd=ROOT,
                env=env,
                text=True,
                stdout=subprocess.PIPE,
                stderr=subprocess.PIPE,
            )

    def test_non_admin_audit_accepts_absent_or_null_bypass_data(self) -> None:
        for value in ("absent", "null"):
            ruleset = self.ruleset()
            if value == "absent":
                ruleset.pop("bypass_actors")
            else:
                ruleset["bypass_actors"] = None
            with self.subTest(value=value):
                completed = self.run_audit(ruleset, admin=False)
                self.assertEqual(0, completed.returncode, completed.stderr)
                self.assertIn(
                    "immutable releases setting: not verified (admin-only)",
                    completed.stdout,
                )

    def test_admin_audit_requires_enabled_immutable_release_setting(self) -> None:
        completed = self.run_audit(self.ruleset(), admin=True)
        self.assertEqual(0, completed.returncode, completed.stderr)
        self.assertIn("immutable releases setting: enabled", completed.stdout)

        invalid = {
            "disabled": '{"enabled": false}',
            "missing": '{}',
            "malformed": '{"enabled": "true"}',
            "multiple-documents": '{"enabled": false}\n{"enabled": true}',
            "prefixed-document": '[]\n{"enabled": true}',
            "unavailable": None,
        }
        for case, response in invalid.items():
            with self.subTest(case=case):
                completed = self.run_audit(
                    self.ruleset(), admin=True, immutable_response=response
                )
                self.assertNotEqual(0, completed.returncode)
                self.assertIn("immutable release", completed.stderr)

    def test_audit_rejects_malformed_common_authority_in_both_modes(self) -> None:
        mutations = {
            "id": 456,
            "name": "other ruleset",
            "target": "branch",
            "enforcement": "disabled",
            "rules": [{"type": "creation"}],
            "include": ["refs/tags/other-*"],
            "exclude": ["refs/tags/control-plane-v*"],
        }
        for field, value in mutations.items():
            for admin in (False, True):
                ruleset = self.ruleset()
                if field in ("include", "exclude"):
                    ruleset["conditions"]["ref_name"][field] = value
                else:
                    ruleset[field] = value
                with self.subTest(field=field, admin=admin):
                    completed = self.run_audit(ruleset, admin=admin)
                    self.assertNotEqual(0, completed.returncode)
                    self.assertIn("common contract", completed.stderr)

    def test_admin_audit_requires_exact_bypass_authority(self) -> None:
        approved = {"actor_type": "DeployKey", "bypass_mode": "always"}
        invalid = {
            "absent": "absent",
            "null": None,
            "wrong-type": [
                {"actor_type": "RepositoryRole", "bypass_mode": "always"}
            ],
            "wrong-mode": [
                {"actor_type": "DeployKey", "bypass_mode": "pull_request"}
            ],
            "duplicate": [approved, approved],
            "extra": [
                approved,
                {"actor_type": "RepositoryRole", "bypass_mode": "always"},
            ],
        }
        self.assertEqual(0, self.run_audit(self.ruleset(), admin=True).returncode)
        for case, actors in invalid.items():
            ruleset = self.ruleset()
            if actors == "absent":
                ruleset.pop("bypass_actors")
            else:
                ruleset["bypass_actors"] = actors
            with self.subTest(case=case):
                completed = self.run_audit(ruleset, admin=True)
                self.assertNotEqual(0, completed.returncode)
                self.assertIn("bypass authority", completed.stderr)


class RetainedReleaseGateTests(unittest.TestCase):
    def release(self) -> dict:
        return {
            "id": retained_release.EXPECTED_RELEASE_ID,
            "tag_name": retained_release.EXPECTED_TAG,
            "target_commitish": retained_release.EXPECTED_TARGET,
            "name": retained_release.EXPECTED_TITLE,
            "body": retained_release.EXPECTED_BODY,
            "draft": False,
            "prerelease": True,
            "immutable": True,
            "assets": [
                {"name": name, **definition}
                for name, definition in retained_release.EXPECTED_ASSETS.items()
            ],
        }

    def attestation(self) -> dict:
        return {
            "attestation": {
                "bundle": {"dsseEnvelope": {"signatures": [{"sig": "verified"}]}}
            },
            "verificationResult": {
                "statement": {
                    "_type": "https://in-toto.io/Statement/v1",
                    "predicateType": "https://in-toto.io/attestation/release/v0.2",
                    "predicate": {
                        "databaseId": str(retained_release.EXPECTED_RELEASE_ID),
                        "repository": retained_release.EXPECTED_REPOSITORY,
                        "tag": retained_release.EXPECTED_TAG,
                        "purl": retained_release.EXPECTED_PURL,
                    },
                    "subject": retained_release.expected_subjects(),
                }
            }
        }

    def write_assets(self, directory: Path) -> None:
        (directory / "probe.txt").write_text(
            "iterabase immutable release gate v1\n", encoding="utf-8"
        )
        (directory / "release-manifest.json").write_text(
            '{"assets":[{"name":"probe.txt","sha256":"450a38bb5ae772469148be208a9c794dd5da78bc0a142d026fa3fbc2def354c6","size":36}],"purpose":"non-semantic immutable-release validation","run_id":"33874696044","schema_version":1,"source_sha":"42604a60764816a66d147a89d8d0772c9e0d2491","tag":"dry-run/immutable-release-gate-v1"}\n',
            encoding="utf-8",
        )

    def test_release_requires_exact_retained_identity_and_guarded_title(self) -> None:
        retained_release.validate_release(self.release())
        repair = self.release()
        repair["name"] = retained_release.REPAIR_TITLE
        retained_release.validate_release(repair, allow_repair_title=True)
        with self.assertRaises(retained_release.GateError):
            retained_release.validate_release(repair)

        for field, value in (
            ("id", 1),
            ("tag_name", "other"),
            ("target_commitish", "a" * 40),
            ("name", "unexpected"),
            ("immutable", False),
        ):
            release_json = self.release()
            release_json[field] = value
            with self.subTest(field=field), self.assertRaises(
                retained_release.GateError
            ):
                retained_release.validate_release(
                    release_json, allow_repair_title=True
                )

    def test_release_rejects_missing_extra_duplicate_or_changed_asset(self) -> None:
        variants = {}
        variants["missing"] = self.release()
        variants["missing"]["assets"].pop()
        variants["extra"] = self.release()
        variants["extra"]["assets"].append(
            {"id": 1, "name": "extra", "size": 1, "digest": "sha256:bad"}
        )
        variants["duplicate"] = self.release()
        variants["duplicate"]["assets"][1]["name"] = "probe.txt"
        variants["changed"] = self.release()
        variants["changed"]["assets"][0]["digest"] = "sha256:bad"
        for case, release_json in variants.items():
            with self.subTest(case=case), self.assertRaises(
                retained_release.GateError
            ):
                retained_release.validate_release(release_json)

    def test_downloaded_assets_require_exact_members_and_bytes(self) -> None:
        with tempfile.TemporaryDirectory() as value:
            directory = Path(value)
            self.write_assets(directory)
            retained_release.asset_identities(directory)
            (directory / "probe.txt").write_text("changed", encoding="utf-8")
            with self.assertRaises(retained_release.GateError):
                retained_release.asset_identities(directory)
            self.write_assets(directory)
            (directory / "extra").write_text("extra", encoding="utf-8")
            with self.assertRaises(retained_release.GateError):
                retained_release.asset_identities(directory)
            (directory / "extra").unlink()
            (directory / "probe.txt").unlink()
            with self.assertRaises(retained_release.GateError):
                retained_release.asset_identities(directory)

    def test_attestation_requires_exact_release_tag_object_and_subject_set(self) -> None:
        retained_release.validate_attestation(self.attestation())
        variants = {}
        variants["unsigned"] = self.attestation()
        variants["unsigned"].pop("attestation")
        variants["wrong-release"] = self.attestation()
        variants["wrong-release"]["verificationResult"]["statement"]["predicate"][
            "databaseId"
        ] = "1"
        variants["wrong-tag-object"] = self.attestation()
        variants["wrong-tag-object"]["verificationResult"]["statement"]["subject"][
            0
        ]["digest"]["sha1"] = "a" * 40
        variants["wrong-asset"] = self.attestation()
        variants["wrong-asset"]["verificationResult"]["statement"]["subject"][1][
            "digest"
        ]["sha256"] = "bad"
        variants["missing-subject"] = self.attestation()
        variants["missing-subject"]["verificationResult"]["statement"][
            "subject"
        ].pop()
        variants["extra-subject"] = self.attestation()
        variants["extra-subject"]["verificationResult"]["statement"][
            "subject"
        ].append({"name": "extra", "digest": {"sha256": "bad"}})
        for case, attestation in variants.items():
            with self.subTest(case=case), self.assertRaises(
                retained_release.GateError
            ):
                retained_release.validate_attestation(attestation)

    def test_asset_probe_results_require_one_http_422(self) -> None:
        targets = {
            "asset-upload": "https://uploads.github.com/repos/nunocgoncalves/iterabase-mono/releases/382723775/assets?name=forbidden.txt",
            "asset-deletion": "repos/nunocgoncalves/iterabase-mono/releases/assets/544335752",
        }
        for operation, target in targets.items():
            with self.subTest(operation=operation):
                result = retained_release.require_probe_result(
                    1,
                    "HTTP/2.0 422 Unprocessable Entity\nmutable response prose",
                    operation,
                )
                self.assertEqual("http", result["protocol"])
                self.assertEqual(target, result["target"])
                self.assertEqual(422, result["http_status"])

    def test_asset_probe_results_reject_untrusted_shapes(self) -> None:
        cases = (
            (0, "HTTP/2.0 422 Unprocessable Entity"),
            (1, "HTTP/2.0 200 OK"),
            (1, "HTTP/2.0 401 Unauthorized"),
            (1, "HTTP/2.0 403 Forbidden"),
            (1, "HTTP/2.0 404 Not Found"),
            (1, "HTTP/2.0 429 Too Many Requests"),
            (1, "HTTP/2.0 500 Internal Server Error"),
            (1, "HTTP/2.0 503 Service Unavailable"),
            (1, "transport failure"),
            (1, "HTTP/2.0 nope"),
            (1, "HTTP/1.1 100 Continue\nHTTP/2.0 422 Unprocessable Entity"),
            (1, "Cannot upload assets to an immutable release"),
        )
        for status, output in cases:
            with self.subTest(status=status, output=output), self.assertRaises(
                retained_release.GateError
            ) as raised:
                retained_release.require_probe_result(status, output, "asset-upload")
            self.assertLess(len(str(raised.exception)), 256)
        with self.assertRaises(retained_release.GateError):
            retained_release.require_probe_result(
                1, "HTTP/2.0 422 Unprocessable Entity", "unknown"
            )

    def test_tag_probe_results_require_one_exact_remote_rejection(self) -> None:
        targets = {
            "tag-update": "refs/tags/dry-run/immutable-release-gate-v1:refs/tags/dry-run/immutable-release-gate-v1",
            "tag-deletion": ":refs/tags/dry-run/immutable-release-gate-v1",
        }
        for operation, target in targets.items():
            with self.subTest(operation=operation):
                result = retained_release.require_probe_result(
                    1,
                    f"!\t{target}\t[remote rejected] (mutable free text)",
                    operation,
                )
                self.assertEqual("git-porcelain", result["protocol"])
                self.assertEqual(target, result["target"])
                self.assertEqual("remote-rejected", result["classification"])

    def test_tag_probe_results_reject_untrusted_shapes(self) -> None:
        target = retained_release.PROBES["tag-update"]["target"]
        valid = f"!\t{target}\t[remote rejected] (free text)"
        cases = (
            (0, valid),
            (1, f"!\t{target}\t[rejected] (local)"),
            (1, f"=\t{target}\t[up to date]"),
            (1, f"*\t{target}\t[new tag]"),
            (1, "!\trefs/tags/other:refs/tags/other\t[remote rejected] (reason)"),
            (1, "authentication or transport failure"),
            (1, "! malformed porcelain"),
            (1, valid + "\n" + valid),
            (1, f"!\t{target}\t[remote failure] (reason)"),
        )
        for status, output in cases:
            with self.subTest(status=status, output=output), self.assertRaises(
                retained_release.GateError
            ) as raised:
                retained_release.require_probe_result(status, output, "tag-update")
            self.assertLess(len(str(raised.exception)), 384)

    def test_protocol_diagnostics_are_bounded_and_redacted(self) -> None:
        secret = "ghp_do-not-print-this"
        http_output = "HTTP/2.0 403 Forbidden\n" + secret * 10_000
        with self.assertRaises(retained_release.GateError) as http_raised:
            retained_release.require_probe_result(9, http_output, "asset-upload")
        http_message = str(http_raised.exception)
        self.assertIn(
            "operation=asset-upload status=9 protocol=http target=matching "
            "http_status=403",
            http_message,
        )
        self.assertNotIn(secret, http_message)
        self.assertLess(len(http_message), 256)

        refspec = "refs/tags/other:refs/tags/other"
        git_output = f"!\t{refspec}\t[remote rejected] ({secret * 10_000})"
        with self.assertRaises(retained_release.GateError) as git_raised:
            retained_release.require_probe_result(7, git_output, "tag-update")
        git_message = str(git_raised.exception)
        self.assertIn("status=7 protocol=git-porcelain", git_message)
        self.assertIn("refspec=wrong", git_message)
        self.assertNotIn(secret, git_message)
        self.assertLess(len(git_message), 384)

    def test_state_includes_release_and_each_asset_attestation(self) -> None:
        with tempfile.TemporaryDirectory() as value:
            directory = Path(value)
            self.write_assets(directory)
            asset_attestations = {
                name: self.attestation()
                for name in retained_release.EXPECTED_ASSETS
            }
            state = retained_release.make_state(
                self.release(),
                self.attestation(),
                asset_attestations,
                directory,
                retained_release.EXPECTED_TAG_OBJECT,
                retained_release.EXPECTED_TARGET,
            )
            authority = state["immutable_authority"]
            self.assertIn("release_attestation_statement", authority)
            self.assertEqual(
                set(retained_release.EXPECTED_ASSETS),
                set(authority["asset_attestation_statements"]),
            )

            missing = dict(asset_attestations)
            missing.pop("probe.txt")
            with self.assertRaises(retained_release.GateError):
                retained_release.make_state(
                    self.release(),
                    self.attestation(),
                    missing,
                    directory,
                    retained_release.EXPECTED_TAG_OBJECT,
                    retained_release.EXPECTED_TARGET,
                )

    def test_complete_probe_state_must_match_the_baseline(self) -> None:
        with tempfile.TemporaryDirectory() as value:
            directory = Path(value)
            self.write_assets(directory)
            before = retained_release.make_state(
                self.release(),
                self.attestation(),
                {name: self.attestation() for name in retained_release.EXPECTED_ASSETS},
                directory,
                retained_release.EXPECTED_TAG_OBJECT,
                retained_release.EXPECTED_TARGET,
            )
            retained_release.compare_states(before, copy.deepcopy(before))
            changed = copy.deepcopy(before)
            changed["immutable_authority"]["asset_attestation_statements"][
                "probe.txt"
            ]["predicate"]["tag"] = "other"
            with self.assertRaises(retained_release.GateError):
                retained_release.compare_states(before, changed)


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
            "group: iterabase-permanent-fixture-${{ matrix.capacity }}",
            "cancel-in-progress: false",
            "export FORGE_E2E_REQUIRE_CAPACITY=true",
        ):
            self.assertIn(value, workflow)
        self.assertNotIn("DIGITALOCEAN_TOKEN", workflow)
        for stale in (
            "Exact candidate",
            "Exact candidate capacity",
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
        self.assertNotIn("toJSON(needs)", candidate)
        self.assertNotIn("NEEDS:", candidate)
        self.assertIn("--needs '${{ runner.temp }}/candidate-job-results.json'", candidate)
        self.assertIn("test \"$(jq -r '.head_branch' <<<\"$run\")\" = master", promotion)
        self.assertIn('git merge-base --is-ancestor "$source_sha" origin/master', promotion)

    def test_candidate_recipes_match_production_authority(self) -> None:
        workflow = (ROOT / ".github/workflows/release-candidate.yml").read_text()
        self.assertIn("matrix.labels_text", workflow)
        self.assertIn("matrix.build_args_text", workflow)
        self.assertIn("recipe_sha256", workflow)
        self.assertIn("with: {name: goreleaser, version: 2.12.7}", workflow)
        self.assertIn("with: {name: syft, version: 1.51.0}", workflow)
        self.assertNotIn("goreleaser/goreleaser-action", workflow)
        self.assertNotIn("anchore/sbom-action", workflow)
        self.assertIn("bash charts/scripts/build-chart-dependency.sh", workflow)

    def test_promotion_remains_protected_pinned_reverified_and_never_rebuilds(self) -> None:
        workflow = (ROOT / ".github/workflows/release-promote.yml").read_text(
            encoding="utf-8"
        )
        self.assertIn("environment: release", workflow)
        self.assertEqual(2, workflow.count("ref: ${{ github.sha }}"))
        self.assertNotIn("ref: master", workflow)
        for value in (
            "github.workflow_sha",
            "Re-verify every privileged-boundary invariant after approval",
            "verify_candidate_run.sh",
            "git merge-base --is-ancestor \"$SOURCE_SHA\" origin/master",
            "audit_release_security.sh",
            "verify-release-manifests",
            "publish_github_releases.sh",
            "check_promotion_destinations.sh",
        ):
            self.assertIn(value, workflow)
        self.assertNotIn("Create or complete target GitHub Releases", workflow)
        self.assertNotIn("docker/build-push-action", workflow)
        self.assertNotIn("docker build -", workflow)
        self.assertNotIn("goreleaser/goreleaser-action", workflow.lower())
        self.assertNotIn("helm package", workflow)
        run_verifier = (ROOT / ".github/scripts/verify_candidate_run.sh").read_text()
        for value in ("candidate_repository", "candidate_workflow", "candidate_event", "candidate_control_sha", "run_attempt", ".head_repository.full_name"):
            self.assertIn(value, run_verifier)

    def test_fixture_callers_gate_actor_writer_set_and_secretless_forks(self) -> None:
        for name in ("e2e.yml", "release-candidate.yml"):
            workflow = (ROOT / ".github/workflows" / name).read_text()
            gate = workflow.index("verify_fixture_trust.sh")
            fixture = workflow.index("setup-permanent-fixture", gate)
            secret = workflow.index("FORGE_E2E_CPU_SSH_KEY", gate)
            self.assertLess(gate, fixture)
            self.assertLess(gate, secret)
        e2e = (ROOT / ".github/workflows/e2e.yml").read_text()
        self.assertIn("pull_request:", e2e)
        self.assertNotIn("pull_request_target:", e2e)
        audit = (ROOT / ".github/scripts/audit_release_security.sh").read_text()
        for value in ("collaborators?affiliation=all", "FORGE_E2E_CPU_SSH_KEY", "FORGE_E2E_GPU_SSH_KEY", "immutable-releases", "expected_write_key_public", "ssh-keygen -y"):
            self.assertIn(value, audit)
        self.assertIn("awk '{print $1 \" \" $2}'", audit)

    def test_release_publication_is_complete_draft_first_and_published_verification_only(self) -> None:
        script = (ROOT / ".github/scripts/publish_github_releases.sh").read_text()
        self.assertNotIn("gh release upload", script)
        for field in (
            "targetCommitish",
            ".name == $expected[0].release_metadata.title",
            ".body == $expected[0].release_metadata.notes",
            ".isPrerelease == $expected[0].release_metadata.prerelease",
            ".immutable",
        ):
            self.assertIn(field, script)
        self.assertIn("jq -j '.release_metadata.notes'", script)
        self.assertIn("published release $tag already matches exactly; verification-only", script)
        create = script.index("gh release create")
        verify_draft = script.index("verify_release \"$tag\" \"$manifest\" \"$stage\"", create)
        publish = script.index("--draft=false", verify_draft)
        verify_published = script.index("verify_release \"$tag\" \"$manifest\" \"$stage\"", publish)
        self.assertLess(create, verify_draft)
        self.assertLess(verify_draft, publish)
        self.assertLess(publish, verify_published)

    def test_destination_preflight_verifies_governed_release_metadata_and_immutability(self) -> None:
        script = (ROOT / ".github/scripts/check_promotion_destinations.sh").read_text()
        for value in (
            "targetCommitish,name,body,isDraft,isPrerelease,assets",
            ".targetCommitish == $expected[0].release_metadata.target_commitish",
            ".name == $expected[0].release_metadata.title",
            ".body == $expected[0].release_metadata.notes",
            ".isPrerelease == $expected[0].release_metadata.prerelease",
            "releases/tags/$tag\" --jq '.immutable'",
            "actual_size=",
        ):
            self.assertIn(value, script)

    def test_protected_release_callers_use_only_non_admin_audits(self) -> None:
        for name in ("release-rehearsal.yml", "release-promote.yml"):
            workflow = (ROOT / ".github/workflows" / name).read_text()
            with self.subTest(workflow=name):
                self.assertEqual(2, workflow.count("audit_release_security.sh"))
                self.assertEqual(2, workflow.count('AUDIT_ADMIN_ENDPOINTS: "false"'))
                self.assertNotIn("REQUIRE_IMMUTABLE_RELEASES", workflow)
                self.assertNotIn(
                    'repos/${{ github.repository }}/immutable-releases', workflow
                )

    def test_retained_immutability_gate_uses_operation_bound_protocol_authority(self) -> None:
        workflow = (ROOT / ".github/workflows/release-rehearsal.yml").read_text()
        script = (ROOT / ".github/scripts/run_retained_release_gate.sh").read_text()
        validator = (ROOT / ".github/scripts/retained_release.py").read_text()
        runbook = (ROOT / "docs/release.md").read_text()
        self.assertIn("run_retained_release_gate.sh", workflow)
        for value in (
            "RELEASE_ID=382723775",
            "GATE_TAG=dry-run/immutable-release-gate-v1",
            "EXPECTED_SOURCE=42604a60764816a66d147a89d8d0772c9e0d2491",
            "EXPECTED_TAG_OBJECT=9f529662036d70348379c6c71a13c9242c7155a5",
            "gh api --include --silent --method POST",
            "gh api --include --silent --method DELETE",
            "git push --porcelain --force origin",
            "git push --porcelain origin",
            "probe-spec",
            "require-probe-result",
            "--probe-attestation",
            "--manifest-attestation",
            "compare-state",
            "release_attestation_verified:true",
            "per_asset_attestations_verified:true",
            "all_probe_states_unchanged:true",
            "probe_results",
        ):
            self.assertIn(value, script)
        for value in (
            "PROBES",
            "require_probe_result",
            "asset_attestation_statements",
            "per-asset attestations do not cover the exact retained assets",
        ):
            self.assertIn(value, validator)
        for forbidden in (
            "require-denial",
            "require_immutable_denial",
            "_DENIAL_OPERATIONS",
            "canonical_immutable",
            "gh release upload",
            "git push --delete",
            "gh release create",
            "gh release delete",
            "release deletion",
        ):
            self.assertNotIn(forbidden, script + validator)
        self.assertEqual(1, script.count("gh release edit"))
        self.assertEqual(1, script.count("require-probe-result"))
        self.assertEqual(4, script.count("run_probe "))

        capture_body = script[
            script.index("capture_state() {") : script.index("run_probe() {")
        ]
        for value in (
            'rm -rf "$directory"',
            'fetch_release "$work/$stem-release"',
            "verify_remote_tag",
            "gh release download",
            'verify_downloaded_assets "$directory"',
            'verify_attestations "$work/$stem" "$directory"',
            "--probe-attestation",
            "--manifest-attestation",
        ):
            self.assertIn(value, capture_body)
        probe_body = script[
            script.index("run_probe() {") : script.index("# Establish immutable identity")
        ]
        self.assertLess(
            probe_body.index("require-probe-result"),
            probe_body.index('capture_state "$stem"'),
        )
        self.assertLess(
            probe_body.index('capture_state "$stem"'),
            probe_body.index('if [[ "$protocol_valid" != true'),
        )
        calls = [
            script.index("run_probe asset-upload"),
            script.index("run_probe asset-deletion"),
            script.index("run_probe tag-update"),
            script.index("run_probe tag-deletion"),
        ]
        self.assertEqual(calls, sorted(calls))
        self.assertLess(calls[-1], script.index("mkdir -p \"$(dirname \"$evidence\")\""))
        self.assertLess(
            script.index('verify_attestations "$work/initial"'),
            script.index("gh release edit"),
        )
        self.assertLess(script.index("gh release edit"), script.index("capture_state baseline"))
        self.assertIn("exactly one HTTP 422", runbook)
        self.assertIn("`git push --porcelain`", runbook)
        self.assertIn("After each probe", runbook)
        self.assertNotIn("bounded, affirmative denial", runbook)
        self.assertNotIn("immutable_releases:true", workflow)

    def test_release_only_manual_dispatch_and_no_push_publication(self) -> None:
        for workflow in ("release-candidate.yml", "release-promote.yml"):
            content = (ROOT / ".github/workflows" / workflow).read_text()
            self.assertIn("workflow_dispatch:", content)
            self.assertNotIn("push:\n", content)


if __name__ == "__main__":
    unittest.main()
