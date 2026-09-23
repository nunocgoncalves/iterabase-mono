#!/usr/bin/env python3

from __future__ import annotations

import copy
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import e2e
import release_baseline as baseline

ROOT = Path(__file__).resolve().parents[2]
SOURCE_SHA = "a" * 40


class SnapshotContractTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.contract = e2e.load_contract(ROOT)

    def snapshot(self) -> dict:
        return baseline.test_snapshot(self.contract)

    def assert_invalid(self, mutate, message: str) -> None:
        value = self.snapshot()
        mutate(value["snapshot"])
        value = baseline.envelope(value["snapshot"])
        with self.assertRaisesRegex(baseline.BaselineError, message):
            baseline.validate_snapshot(value, self.contract)

    def test_exact_bootstrap_is_hard_bounded_to_approved_root_and_lvm_identity(self) -> None:
        self.assertEqual(386705918, baseline.BOOTSTRAP_ANCHOR_ID)
        self.assertEqual("b4b32b14d6ab89a85db24fc198879fdfe9621d2a", baseline.BOOTSTRAP_SOURCE)
        self.assertEqual("34541902001", baseline.BOOTSTRAP_RUN_ID)
        self.assertEqual(
            ("lvm-storage-substrate", "0.4.0", 14569, "6b1233c2c27cab8597f0a1b77de1d445f5a77ea696df75d30de7bc1747eda7dd"),
            baseline.BOOTSTRAP_CHART_IDENTITIES["lvm-storage-substrate-chart"],
        )
        self.assertEqual(
            {386705918, 386705747, 386705816, 386705691},
            {item["id"] for item in baseline.BOOTSTRAP_RELEASES.values()},
        )

    def test_complete_snapshot_has_all_targets_artifacts_variants_and_fixtures(self) -> None:
        value = self.snapshot()
        baseline.validate_snapshot(value, self.contract)
        body = value["snapshot"]
        self.assertEqual(list(baseline.TARGET_ORDER), [item["target"] for item in body["targets"]])
        self.assertEqual(list(baseline.FIXTURE_ORDER), [item["name"] for item in body["fixtures"]])
        forge = baseline.artifact_map(value, self.contract)["forge-binary"]
        self.assertEqual(list(baseline.FORGE_PLATFORMS), [item["platform"] for item in forge["variants"]])
        self.assertEqual(
            {
                name
                for name, recipe in self.contract["artifact_recipes"].items()
                if recipe.get("temporary_only") is not True
            },
            set(baseline.artifact_map(value, self.contract)),
        )

    def test_canonical_hash_and_pointer_identity_are_fail_closed(self) -> None:
        value = self.snapshot()
        value["snapshot"]["cohort_id"] = "moved"
        with self.assertRaisesRegex(baseline.BaselineError, "canonical hash"):
            baseline.validate_snapshot(value, self.contract)
        self.assert_invalid(
            lambda body: body["anchor"].update({"release_id": None}),
            "exact anchor Release ID",
        )

    def test_missing_extra_duplicate_malformed_and_cross_cohort_records_fail(self) -> None:
        cases = (
            (lambda body: body["targets"].pop(), "all six targets"),
            (lambda body: body["targets"][0]["artifacts"].pop(), "artifact membership"),
            (lambda body: body["targets"][0]["artifacts"].append(copy.deepcopy(body["targets"][0]["artifacts"][0])), "artifact membership"),
            (lambda body: body["fixtures"].pop(), "fixture membership"),
            (lambda body: body["targets"][0]["artifacts"][0].update({"digest": "mutable"}), "immutable identity"),
            (lambda body: body["targets"][0]["artifacts"][0].update({"reference": "ghcr.io/other/control-plane:1@sha256:" + "1" * 64}), "unauthorized or mutable"),
            (lambda body: body["targets"][0]["artifacts"][0].update({"version": "9.9.9"}), "target cohort"),
        )
        for mutate, message in cases:
            with self.subTest(message=message):
                self.assert_invalid(mutate, message)

    def test_published_forge_variants_require_exact_release_urls_and_no_candidate_paths(self) -> None:
        for mutation in ("url", "candidate_path"):
            value = self.snapshot()
            forge = next(
                artifact
                for cohort in value["snapshot"]["targets"]
                for artifact in cohort["artifacts"]
                if artifact["name"] == "forge-binary"
            )
            if mutation == "url":
                forge["variants"][0]["url"] = "https://example.invalid/forge.tar.gz"
            else:
                forge["variants"][0]["candidate_path"] = "assets/forge/forged.tar.gz"
            value = baseline.envelope(value["snapshot"])
            with self.subTest(mutation=mutation), self.assertRaisesRegex(
                baseline.BaselineError, "unauthorized Release URL or candidate path"
            ):
                baseline.validate_snapshot(value, self.contract)

    def test_selected_targets_are_canonical_and_anchor_is_deterministic(self) -> None:
        self.assertEqual(
            "iterabase-platform-chart",
            baseline.deterministic_anchor(["control-plane", "forge", "iterabase-platform-chart"]),
        )
        for selected in (
            ["forge", "control-plane"],
            ["forge", "forge"],
            [],
            ["unknown"],
        ):
            with self.subTest(selected=selected), self.assertRaises(baseline.BaselineError):
                baseline.deterministic_anchor(selected)

    def test_pinned_plan_is_unchanged_when_the_caller_pointer_moves(self) -> None:
        value = self.snapshot()
        plan = e2e.make_plan(
            ROOT,
            e2e.load_catalogue(ROOT),
            self.contract,
            intent="candidate",
            source_sha=SOURCE_SHA,
            targets=["forge"],
            resolved_baseline=value,
        )
        pinned = plan["resolved_baseline"]["snapshot_sha256"]
        value["snapshot"]["anchor"]["release_id"] = 999
        value["snapshot_sha256"] = "0" * 64
        self.assertEqual(pinned, plan["resolved_baseline"]["snapshot_sha256"])
        self.assertNotEqual(value["snapshot_sha256"], pinned)


class PublishedManifestAuthorityTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.contract = e2e.load_contract(ROOT)

    def fixture(self) -> tuple[dict, dict, dict, dict]:
        shared = baseline.test_snapshot(self.contract)
        cohort = shared["snapshot"]["targets"][0]
        release = {
            "id": cohort["release"]["id"],
            "name": cohort["release"]["tag"],
            "body": "governed release notes",
            "target_commitish": cohort["source_sha"],
            "prerelease": False,
        }
        manifest = {
            "release_id": release["id"],
            "candidate_run_attempt": cohort["candidate_run_attempt"],
            "cohort_id": shared["snapshot"]["cohort_id"],
            "parent_anchor": shared["snapshot"]["parent_anchor"],
            "anchor_target": shared["snapshot"]["anchor_target"],
            "baseline_snapshot_sha256": shared["snapshot_sha256"],
            "release_metadata": {
                "title": release["name"],
                "notes": release["body"],
                "target_commitish": release["target_commitish"],
                "prerelease": False,
                "make_latest": False,
            },
        }
        return manifest, shared, cohort, release

    def test_v3_manifest_rejects_parent_anchor_and_deterministic_anchor_disagreement(self) -> None:
        for field, value in (
            ("parent_anchor", {"release_id": 999, "tag": "other", "snapshot_sha256": "0" * 64}),
            ("anchor_target", "control-plane"),
        ):
            manifest, shared, cohort, release = self.fixture()
            manifest[field] = value
            with self.subTest(field=field), self.assertRaisesRegex(
                baseline.BaselineError, "schema-v3 manifest authority drifted"
            ):
                baseline._validate_v3_manifest_authority(
                    manifest, shared, cohort, release, release["id"]
                )

    def test_v3_manifest_rejects_governed_release_title_and_notes_disagreement(self) -> None:
        for field in ("title", "notes"):
            manifest, shared, cohort, release = self.fixture()
            manifest["release_metadata"][field] = "conflicting metadata"
            with self.subTest(field=field), self.assertRaisesRegex(
                baseline.BaselineError, "governed Release metadata drifted"
            ):
                baseline._validate_v3_manifest_authority(
                    manifest, shared, cohort, release, release["id"]
                )

    def test_published_member_preflight_uses_exact_expected_manifest_set(self) -> None:
        shared = baseline.test_snapshot(self.contract)
        with tempfile.TemporaryDirectory() as raw:
            manifests = Path(raw)
            for target in shared["snapshot"]["selected_targets"]:
                (manifests / f"release-manifest-{target}.json").write_text("{}\n")
            backend = object()
            with patch("release_baseline._verify_published_snapshot") as verify:
                baseline.verify_published_members(
                    self.contract,
                    shared,
                    ["forge"],
                    manifests,
                    backend=backend,  # type: ignore[arg-type]
                )
            verify.assert_called_once_with(
                backend,
                shared,
                {},
                self.contract,
                target_filter={"forge"},
                expected_manifests=manifests,
            )
            (manifests / "release-manifest-forge.json").unlink()
            with self.assertRaisesRegex(
                baseline.BaselineError, "manifest set is incomplete"
            ):
                baseline.verify_published_members(
                    self.contract,
                    shared,
                    ["forge"],
                    manifests,
                    backend=backend,  # type: ignore[arg-type]
                )


class CandidateSnapshotTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.contract = e2e.load_contract(ROOT)
        cls.base = baseline.test_snapshot(cls.contract)

    def test_candidate_and_final_snapshots_replace_only_selected_cohorts(self) -> None:
        target = "iterabase-platform-chart"
        version = "9.8.7"
        recipes = self.contract["artifact_recipes"]
        with tempfile.TemporaryDirectory() as raw:
            candidate = Path(raw) / "candidate"
            assets = candidate / "assets"
            charts = assets / "charts"
            charts.mkdir(parents=True)
            for chart in ("iterabase-platform", "cert-manager-substrate", "lvm-storage-substrate"):
                (charts / f"{chart}-{version}.tgz").write_bytes(chart.encode())
            plan = {
                "resolved_baseline": copy.deepcopy(self.base),
                "targets": [target],
                "releases": [{
                    "target": target,
                    "version": version,
                    "production_tag": f"iterabase-platform-{version}",
                    "artifact_types": ["chart"],
                }],
                "source_sha": SOURCE_SHA,
                "candidate_control_sha": SOURCE_SHA,
                "run_id": "123",
                "run_attempt": "2",
                "image_matrix": [],
                "chart_matrix": [{
                    "target": target,
                    "chart": "iterabase-platform",
                    "version": version,
                    "recipe_sha256": e2e.recipe_hash(recipes["iterabase-platform-chart"]),
                    "companion_recipes": [
                        {"artifact": name, "recipe_sha256": e2e.recipe_hash(recipes[name])}
                        for name in ("cert-manager-substrate-chart", "lvm-storage-substrate-chart")
                    ],
                }],
                "forge_recipe_sha256": e2e.recipe_hash(recipes["forge-binary"]),
            }
            before = {
                item["target"]: copy.deepcopy(item)
                for item in self.base["snapshot"]["targets"]
                if item["target"] != target
            }
            candidate_snapshot = baseline.build_candidate_snapshot(plan, assets, self.contract)
            body = candidate_snapshot["snapshot"]
            self.assertEqual("candidate", body["mode"])
            self.assertEqual(target, body["anchor_target"])
            self.assertEqual(baseline.parent_pin(self.base), body["parent_anchor"])
            for cohort in body["targets"]:
                if cohort["target"] != target:
                    self.assertEqual(before[cohort["target"]], cohort)
            selected = next(item for item in body["targets"] if item["target"] == target)
            self.assertTrue(all(item["custody"] == "selected-candidate" for item in selected["artifacts"]))
            self.assertTrue(all(item["filename"].endswith(f"-{version}.tgz") for item in selected["artifacts"]))

            planned = baseline.planned_final_snapshot(candidate_snapshot, self.contract)
            self.assertEqual("planned-final", planned["snapshot"]["mode"])
            digests = {
                name: "sha256:" + str(index + 1) * 64
                for index, name in enumerate(
                    ("iterabase-platform-chart", "cert-manager-substrate-chart", "lvm-storage-substrate-chart")
                )
            }
            final = baseline.final_snapshot(
                candidate_snapshot,
                self.contract,
                {target: 999},
                digests,
                {target: {"sha": "b" * 40, "target_sha": SOURCE_SHA}},
            )
            self.assertEqual("published", final["snapshot"]["mode"])
            self.assertEqual(999, final["snapshot"]["anchor"]["release_id"])
            final_selected = next(item for item in final["snapshot"]["targets"] if item["target"] == target)
            self.assertTrue(all(item["custody"] == "published-baseline" for item in final_selected["artifacts"]))
            self.assertEqual(digests["lvm-storage-substrate-chart"], final_selected["artifacts"][2]["oci_digest"])


class RollbackContractTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.contract = e2e.load_contract(ROOT)

    class Backend:
        def __init__(
            self, current: int, destination: int, *, before_mutation: int | None = None
        ) -> None:
            self.current = current
            self.destination = destination
            self.before_mutation = current if before_mutation is None else before_mutation
            self.updated: int | None = None

        def latest(self) -> dict:
            return {"id": self.current}

        def release(self, release_id: int) -> dict:
            return {"id": release_id}

        def reread_latest_before_mutation(self) -> dict:
            return {"id": self.before_mutation}

        def set_latest(self, release_id: int) -> None:
            self.updated = release_id

        def reread_latest_after_mutation(self) -> dict:
            return {"id": self.updated}

    def chain(self) -> tuple[dict, dict]:
        destination = baseline.test_snapshot(self.contract)
        destination_id = destination["snapshot"]["anchor"]["release_id"]
        current = copy.deepcopy(destination)
        body = current["snapshot"]
        body["parent_anchor"] = baseline.parent_pin(destination)
        body["anchor"]["release_id"] = 99
        body["anchor"]["tag"] = "current-anchor"
        current = baseline.envelope(body)
        self.assertNotEqual(destination_id, current["snapshot"]["anchor"]["release_id"])
        return current, destination

    def test_whole_snapshot_ancestor_rollback_moves_one_pointer_and_reverifies(self) -> None:
        current, destination = self.chain()
        current_id = current["snapshot"]["anchor"]["release_id"]
        destination_id = destination["snapshot"]["anchor"]["release_id"]
        backend = self.Backend(current_id, destination_id)
        values = {current_id: current, destination_id: destination}
        with patch("release_baseline._resolve_captured", side_effect=lambda _contract, _backend, release: values[release["id"]]):
            result = baseline.rollback(
                self.contract,
                current_anchor_id=current_id,
                destination_anchor_id=destination_id,
                linear_identifier="HOR-554",
                reason="restore previous complete cohort",
                backend=backend,  # type: ignore[arg-type]
            )
        self.assertEqual(destination_id, backend.updated)
        self.assertTrue(result["verified"])

    def test_pointer_movement_during_ancestor_verification_fails_before_handoff(self) -> None:
        current, destination = self.chain()
        current_id = current["snapshot"]["anchor"]["release_id"]
        destination_id = destination["snapshot"]["anchor"]["release_id"]
        backend = self.Backend(
            current_id, destination_id, before_mutation=current_id + 1
        )
        values = {current_id: current, destination_id: destination}
        with patch(
            "release_baseline._resolve_captured",
            side_effect=lambda _contract, _backend, release: values[release["id"]],
        ):
            with self.assertRaisesRegex(
                baseline.BaselineError, "moved during ancestor verification"
            ):
                baseline.rollback(
                    self.contract,
                    current_anchor_id=current_id,
                    destination_anchor_id=destination_id,
                    linear_identifier="HOR-554",
                    reason="reject a concurrent pointer move",
                    backend=backend,  # type: ignore[arg-type]
                )
        self.assertIsNone(backend.updated)

    def test_stale_current_nonancestor_and_partial_requests_fail_closed(self) -> None:
        current, destination = self.chain()
        current_id = current["snapshot"]["anchor"]["release_id"]
        destination_id = destination["snapshot"]["anchor"]["release_id"]
        with self.assertRaisesRegex(baseline.BaselineError, "stale"):
            baseline.rollback(
                self.contract,
                current_anchor_id=current_id + 1,
                destination_anchor_id=destination_id,
                linear_identifier="HOR-554",
                reason="stale",
                backend=self.Backend(current_id, destination_id),  # type: ignore[arg-type]
            )
        no_parent = copy.deepcopy(current)
        no_parent["snapshot"]["parent_anchor"] = None
        no_parent = baseline.envelope(no_parent["snapshot"])
        backend = self.Backend(current_id, destination_id)
        with patch("release_baseline._resolve_captured", return_value=no_parent):
            with self.assertRaisesRegex(baseline.BaselineError, "not a whole-snapshot ancestor"):
                baseline.rollback(
                    self.contract,
                    current_anchor_id=current_id,
                    destination_anchor_id=destination_id,
                    linear_identifier="HOR-554",
                    reason="partial target rollback is forbidden",
                    backend=backend,  # type: ignore[arg-type]
                )
        for identifier, reason in (("not-linear", "reason"), ("HOR-554", "")):
            with self.subTest(identifier=identifier, reason=reason), self.assertRaises(baseline.BaselineError):
                baseline.rollback(
                    self.contract,
                    current_anchor_id=current_id,
                    destination_anchor_id=destination_id,
                    linear_identifier=identifier,
                    reason=reason,
                    backend=backend,  # type: ignore[arg-type]
                )


class ResolverSelectionTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.contract = e2e.load_contract(ROOT)

    class Backend:
        def __init__(self, release_id: int) -> None:
            self.release_id = release_id
            self.calls = 0

        def latest(self) -> dict:
            self.calls += 1
            return {"id": self.release_id}

    def test_required_anchor_mismatch_fails_before_bootstrap_or_v3_access(self) -> None:
        backend = self.Backend(123)
        with patch("release_baseline._resolve_v3") as resolve_v3:
            with self.assertRaisesRegex(baseline.BaselineError, "baseline_anchor_release_id"):
                baseline.resolve_latest(self.contract, expected_anchor_id=456, backend=backend)  # type: ignore[arg-type]
            resolve_v3.assert_not_called()
        self.assertEqual(1, backend.calls)

    def test_exact_bootstrap_rejects_every_other_legacy_latest(self) -> None:
        backend = self.Backend(123)
        with patch("release_baseline._resolve_v3", side_effect=baseline.BaselineError("no complete baseline-snapshot.json")):
            with self.assertRaisesRegex(baseline.BaselineError, "baseline-snapshot"):
                baseline.resolve_latest(self.contract, backend=backend)  # type: ignore[arg-type]
        self.assertEqual(1, backend.calls)


if __name__ == "__main__":
    unittest.main()
