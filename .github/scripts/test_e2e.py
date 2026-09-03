#!/usr/bin/env python3

from __future__ import annotations

import copy
import hashlib
import io
import json
from pathlib import Path
import tarfile
import tempfile
import unittest

from e2e import (
    E2EError,
    archive_image_config,
    extract_chart,
    find_metadata,
    hash_file,
    load_catalogue,
    load_contract,
    make_plan,
    runtime_image_tag,
    validate_catalogue_contract,
    validate_results,
    set_chart_dependency_version,
)

ROOT = Path(__file__).resolve().parents[2]
SOURCE_SHA = "a" * 40


class E2EPlanTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.catalogue = load_catalogue(ROOT)
        cls.contract = load_contract(ROOT)

    def plan(self, paths: list[str]) -> dict:
        return make_plan(
            ROOT,
            self.catalogue,
            self.contract,
            intent="pr",
            source_sha=SOURCE_SHA,
            paths=paths,
        )

    def test_compiled_contract_is_complete(self) -> None:
        validate_catalogue_contract(self.catalogue, self.contract)
        plan = make_plan(
            ROOT,
            self.catalogue,
            self.contract,
            intent="pr",
            source_sha=SOURCE_SHA,
            paths=[".github/workflows/e2e.yml"],
        )
        runnable = [
            scenario
            for suite in self.catalogue["suites"]
            for scenario in suite["scenarios"]
            if scenario["metadata"]["tier"] in {"F2", "F3"}
        ]
        self.assertEqual(len(runnable), plan["scenario_total"])
        self.assertEqual(
            {scenario["id"] for scenario in runnable},
            set(plan["selected_scenario_ids"]),
        )

    def test_representative_path_unions_are_conservative(self) -> None:
        cases = {
            "docs": (["docs/ci.md"], set()),
            "control-plane": (
                ["control-plane/internal/api/handler.go"],
                {"control-plane-image"},
            ),
            "inference": (
                ["inference-gateway/internal/proxy/proxy.go"],
                {"inference-gateway-image"},
            ),
            "chart": (
                ["charts/charts/control-plane/templates/deployment.yaml"],
                {"control-plane-chart", "iterabase-platform-chart"},
            ),
            "forge": (["forge/internal/lifecycle/lifecycle.go"], {"forge-binary"}),
            "shared-testkit": (
                ["testkit/e2e/suite.go"],
                {
                    "control-plane-image",
                    "harness-image",
                    "tool-runner-image",
                    "inference-gateway-image",
                    "runtime-fixture-image",
                    "iterabase-platform-chart",
                    "cert-manager-substrate-chart",
                    "forge-binary",
                },
            ),
            "path-collector": (
                [".github/scripts/collect_changed_paths.py"],
                {
                    "control-plane-image",
                    "harness-image",
                    "tool-runner-image",
                    "inference-gateway-image",
                    "runtime-fixture-image",
                    "iterabase-platform-chart",
                    "cert-manager-substrate-chart",
                    "forge-binary",
                },
            ),
            "workflow": (
                [".github/workflows/e2e.yml"],
                {
                    "control-plane-image",
                    "harness-image",
                    "tool-runner-image",
                    "inference-gateway-image",
                    "runtime-fixture-image",
                    "iterabase-platform-chart",
                    "cert-manager-substrate-chart",
                    "forge-binary",
                },
            ),
        }
        for name, (paths, expected_subset) in cases.items():
            with self.subTest(name=name):
                plan = self.plan(paths)
                self.assertTrue(expected_subset.issubset(set(plan["affected_artifacts"])))
                if name == "docs":
                    self.assertEqual(0, plan["scenario_total"])
                else:
                    self.assertGreater(plan["scenario_total"], 0)

    def test_ambiguous_unknown_and_noncanonical_paths_fail_closed(self) -> None:
        for paths in ([], ["unknown/runtime.input"], ["../outside"], ["./docs/ci.md"]):
            with self.subTest(paths=paths), self.assertRaises(E2EError):
                self.plan(paths)

    def test_deletion_and_move_changes_keep_both_owners(self) -> None:
        moved = self.plan(
            [
                "control-plane/internal/api/moved.go",
                "forge/internal/lifecycle/moved.go",
            ]
        )
        self.assertIn("control-plane-image", moved["affected_artifacts"])
        self.assertIn("forge-binary", moved["affected_artifacts"])

    def test_capacity_groups_are_cross_intent_capacity_scoped_and_serial(self) -> None:
        pull_request = make_plan(
            ROOT, self.catalogue, self.contract,
            intent="pr", source_sha=SOURCE_SHA,
            paths=[".github/workflows/e2e.yml"],
        )
        candidate = make_plan(
            ROOT, self.catalogue, self.contract,
            intent="candidate", source_sha=SOURCE_SHA,
            targets=list(self.contract["targets"]),
        )
        for plan in (pull_request, candidate):
            groups = {item["capacity"]: item for item in plan["real_machine_matrix"]}
            self.assertEqual({"cpu", "gpu"}, set(groups))
            self.assertEqual("iterabase-permanent-fixture-cpu", groups["cpu"]["capacity_group"])
            self.assertEqual("iterabase-permanent-fixture-gpu", groups["gpu"]["capacity_group"])
            self.assertNotEqual(groups["cpu"]["capacity_group"], groups["gpu"]["capacity_group"])
            self.assertEqual(
                ["forge/digitalocean-cpu", "forge/digitalocean-workspace"],
                [item["id"] for item in groups["cpu"]["scenarios"]],
            )

    def test_candidate_union_uses_same_scenario_and_stage_graph(self) -> None:
        candidate = make_plan(
            ROOT,
            self.catalogue,
            self.contract,
            intent="candidate",
            source_sha=SOURCE_SHA,
            targets=["control-plane", "iterabase-platform-chart"],
        )
        pull_request = make_plan(
            ROOT,
            self.catalogue,
            self.contract,
            intent="pr",
            source_sha=SOURCE_SHA,
            paths=[".github/workflows/e2e.yml"],
        )
        pull_request_by_id = {item["id"]: item for item in pull_request["scenario_matrix"]}
        for scenario in candidate["scenario_matrix"]:
            source = pull_request_by_id[scenario["id"]]
            for field in ("id", "owner", "target", "scenario_timeout", "stage_graph_sha256", "stages"):
                self.assertEqual(source[field], scenario[field])
            for artifact in scenario["artifacts"]:
                recipe = self.contract["artifact_recipes"][artifact["name"]]
                if recipe.get("target") in {"control-plane", "iterabase-platform-chart"} and not recipe.get("temporary_only"):
                    self.assertEqual("selected-candidate", artifact["custody"])

    def test_runnable_registration_without_artifacts_or_routes_fails(self) -> None:
        for field in ("required_artifacts", "intents", "make_target", "fixture_modes"):
            catalogue = copy.deepcopy(self.catalogue)
            scenario = next(
                scenario
                for suite in catalogue["suites"]
                for scenario in suite["scenarios"]
                if scenario["metadata"]["tier"] == "F2"
            )
            scenario["metadata"][field] = [] if field != "make_target" else ""
            with self.subTest(field=field), self.assertRaises(E2EError):
                validate_catalogue_contract(catalogue, self.contract)

    def test_kind_install_cannot_bypass_post_create_runtime_import(self) -> None:
        for scenario in (
            scenario
            for suite in self.catalogue["suites"]
            for scenario in suite["scenarios"]
            if scenario["metadata"]["tier"] == "F2"
        ):
            stages = {stage["name"]: stage for stage in scenario["stages"]}
            self.assertEqual(["create-kind"], stages["import-runtime-images"]["depends_on"])

        catalogue = copy.deepcopy(self.catalogue)
        scenario = next(
            scenario
            for suite in catalogue["suites"]
            for scenario in suite["scenarios"]
            if scenario["metadata"]["tier"] == "F2"
        )
        scenario["stages"] = [
            stage for stage in scenario["stages"] if stage["name"] != "import-runtime-images"
        ]
        with self.assertRaisesRegex(E2EError, "import resolved runtime images"):
            validate_catalogue_contract(catalogue, self.contract)

    def test_kind_harness_cannot_bypass_dedicated_agentpool_storage(self) -> None:
        harness_scenarios = [
            scenario
            for suite in self.catalogue["suites"]
            for scenario in suite["scenarios"]
            if scenario["metadata"].get("tier") == "F2"
            and "harness-image" in scenario["metadata"].get("required_artifacts", [])
        ]
        self.assertTrue(harness_scenarios)
        for scenario in harness_scenarios:
            stages = {stage["name"]: stage for stage in scenario["stages"]}
            self.assertIn("configure-agentpool-local-path", stages)
            if scenario["id"].startswith("charts/"):
                self.assertEqual(
                    ["configure-agentpool-local-path"],
                    stages["install-harness-worker"]["depends_on"],
                )

        catalogue = copy.deepcopy(self.catalogue)
        scenario = next(
            scenario
            for suite in catalogue["suites"]
            for scenario in suite["scenarios"]
            if scenario["id"].startswith("charts/")
            and "harness-image" in scenario["metadata"].get("required_artifacts", [])
        )
        scenario["stages"] = [
            stage
            for stage in scenario["stages"]
            if stage["name"] != "configure-agentpool-local-path"
        ]
        next(
            stage
            for stage in scenario["stages"]
            if stage["name"] == "install-harness-worker"
        )["depends_on"] = ["import-runtime-images"]
        with self.assertRaisesRegex(E2EError, "dedicated AgentPool local-path substrate"):
            validate_catalogue_contract(catalogue, self.contract)

    def test_unselected_baseline_is_explicit_not_bumped_repository_version(self) -> None:
        plan = make_plan(
            ROOT, self.catalogue, self.contract,
            intent="candidate", source_sha=SOURCE_SHA,
            targets=["control-plane-chart"],
        )
        control = next(
            artifact
            for scenario in plan["scenario_matrix"]
            for artifact in scenario["artifacts"]
            if artifact["name"] == "control-plane-image"
        )
        self.assertEqual("published-baseline", control["custody"])
        self.assertEqual(
            self.contract["published_baselines"]["control-plane-image"],
            control["reference"],
        )
        self.assertNotIn(
            (ROOT / "control-plane/VERSION").read_text().strip(),
            control["reference"],
        )

    def test_selected_artifact_never_substitutes_a_baseline(self) -> None:
        plan = self.plan(["control-plane/internal/api/handler.go"])
        for scenario in plan["scenario_matrix"]:
            for artifact in scenario["artifacts"]:
                if artifact["name"] == "control-plane-image":
                    self.assertEqual("selected-temporary", artifact["custody"])


class RuntimeCompositionContractTests(unittest.TestCase):
    def test_downloaded_image_archive_retains_config_identity_distinct_from_runtime_manifest(self) -> None:
        with tempfile.TemporaryDirectory() as value:
            archive = Path(value) / "control-plane-image.tar"
            config = json.dumps(
                {
                    "config": {
                        "Labels": {"org.opencontainers.image.revision": SOURCE_SHA}
                    }
                },
                sort_keys=True,
                separators=(",", ":"),
            ).encode()
            config_digest = hashlib.sha256(config).hexdigest()
            manifest = json.dumps(
                [
                    {
                        "Config": f"blobs/sha256/{config_digest}",
                        "RepoTags": [f"iterabase-e2e/control-plane:{SOURCE_SHA}"],
                        "Layers": [],
                    }
                ]
            ).encode()
            with tarfile.open(archive, "w") as bundle:
                for name, data in (
                    ("manifest.json", manifest),
                    (f"blobs/sha256/{config_digest}", config),
                ):
                    member = tarfile.TarInfo(name)
                    member.size = len(data)
                    bundle.addfile(member, io.BytesIO(data))

            digest, decoded = archive_image_config(
                archive, f"iterabase-e2e/control-plane:{SOURCE_SHA}"
            )
            self.assertEqual("sha256:" + config_digest, digest)
            self.assertEqual(
                SOURCE_SHA,
                decoded["config"]["Labels"]["org.opencontainers.image.revision"],
            )
            self.assertNotEqual("sha256:" + "f" * 64, digest)

    def test_image_runtime_tag_always_selects_the_imported_archive(self) -> None:
        digest = "sha256:" + "b" * 64
        self.assertEqual(
            "exact-source-sha",
            runtime_image_tag("selected-temporary", "exact-source-sha", digest),
        )
        self.assertEqual(
            "candidate-run",
            runtime_image_tag("selected-candidate", "candidate-run", digest),
        )
        self.assertEqual(
            "0.0.30",
            runtime_image_tag("published-baseline", "0.0.30", digest),
        )

    def test_exact_downloaded_artifact_shape_updates_helm_canonical_manifest(self) -> None:
        with tempfile.TemporaryDirectory() as value:
            root = Path(value)
            artifacts = root / "artifacts"
            platform_artifact = artifacts / "e2e-runtime-iterabase-platform-chart"
            control_artifact = artifacts / "e2e-runtime-control-plane-chart"
            platform_artifact.mkdir(parents=True)
            control_artifact.mkdir(parents=True)

            def package(directory: Path, chart: str, version: str, manifest: str) -> Path:
                source = root / f"source-{chart}" / chart
                source.mkdir(parents=True)
                (source / "Chart.yaml").write_text(manifest, encoding="utf-8")
                (source / "Chart.lock").write_text("digest: stale\n", encoding="utf-8")
                archive = directory / f"{chart}-{version}.tgz"
                with tarfile.open(archive, "w:gz") as bundle:
                    bundle.add(source, arcname=chart)
                (directory / f"{chart}-chart.json").write_text(
                    json.dumps(
                        {
                            "name": f"{chart}-chart",
                            "chart": chart,
                            "file": archive.name,
                            "version": version,
                        }
                    )
                    + "\n",
                    encoding="utf-8",
                )
                return archive

            package(
                platform_artifact,
                "iterabase-platform",
                "0.3.23",
                "apiVersion: v2\n"
                "dependencies:\n"
                "- condition: control-plane.enabled\n"
                "  name: control-plane\n"
                "  repository: file://../control-plane\n"
                "  version: 0.4.12\n"
                "description: packaged Helm manifest\n"
                "name: iterabase-platform\n"
                "version: 0.3.23\n",
            )
            package(
                control_artifact,
                "control-plane",
                "0.4.13",
                "apiVersion: v2\nname: control-plane\nversion: 0.4.13\n",
            )

            platform_metadata, platform_directory = find_metadata(
                artifacts,
                "iterabase-platform-chart",
                {"chart": "iterabase-platform"},
            ) or ({}, Path())
            control_metadata, control_directory = find_metadata(
                artifacts,
                "control-plane-chart",
                {"chart": "control-plane"},
            ) or ({}, Path())
            self.assertEqual(platform_artifact, platform_directory)
            self.assertEqual(control_artifact, control_directory)
            platform = extract_chart(
                platform_directory / platform_metadata["file"],
                root / "runtime/charts/iterabase-platform-chart",
            )
            control = extract_chart(
                control_directory / control_metadata["file"],
                root / "runtime/charts/control-plane-chart",
            )

            set_chart_dependency_version(platform, control.name, "0.4.13")

            manifest = (platform / "Chart.yaml").read_text(encoding="utf-8")
            self.assertIn("  name: control-plane\n", manifest)
            self.assertIn("  version: 0.4.13\n", manifest)
            self.assertFalse((platform / "Chart.lock").exists())

    def test_selected_nested_chart_updates_outer_dependency_and_invalidates_lock(self) -> None:
        with tempfile.TemporaryDirectory() as value:
            platform = Path(value)
            (platform / "Chart.yaml").write_text(
                "version: 1.0.0\ndependencies:\n  - name: control-plane\n    version: 0.4.12\n    repository: file://../control-plane\n",
                encoding="utf-8",
            )
            (platform / "Chart.lock").write_text("digest: stale\n")
            set_chart_dependency_version(platform, "control-plane", "0.4.13")
            self.assertIn("version: 0.4.13", (platform / "Chart.yaml").read_text())
            self.assertFalse((platform / "Chart.lock").exists())

    def test_missing_nested_dependency_fails_closed(self) -> None:
        with tempfile.TemporaryDirectory() as value:
            platform = Path(value)
            (platform / "Chart.yaml").write_text("version: 1.0.0\n", encoding="utf-8")
            with self.assertRaises(E2EError):
                set_chart_dependency_version(platform, "control-plane", "0.4.13")


class ResultReconciliationTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.catalogue = load_catalogue(ROOT)
        cls.contract = load_contract(ROOT)

    def fixture(self, directory: Path) -> tuple[Path, Path, dict]:
        plan = make_plan(
            ROOT,
            self.catalogue,
            self.contract,
            intent="pr",
            source_sha=SOURCE_SHA,
            paths=["forge/internal/lifecycle/lifecycle.go"],
        )
        # Keep one scenario so result fixtures remain small and exact.
        selected = next(item for item in plan["scenario_matrix"] if item["id"] == "forge/digitalocean-gpu")
        # Model the baseline resolver's immutable identities without network I/O.
        for artifact in selected["artifacts"]:
            if artifact["custody"] != "published-baseline":
                continue
            if artifact["kind"] == "image":
                artifact["digest"] = "sha256:" + "4" * 64
            else:
                artifact["checksum"] = "5" * 64
        plan["scenario_matrix"] = [selected]
        plan["kind_matrix"] = []
        plan["real_machine_matrix"] = [selected]
        plan["selected_scenario_ids"] = [selected["id"]]
        plan["scenario_total"] = 1
        plan_path = directory / "plan.json"
        plan_path.write_text(json.dumps(plan, sort_keys=True, separators=(",", ":")) + "\n", encoding="utf-8")

        bundle_artifacts = []
        observations = {}
        for artifact in selected["artifacts"]:
            record = {
                "name": artifact["name"],
                "kind": artifact["kind"],
                "custody": artifact["custody"],
                "reference": artifact.get("reference", artifact["name"]),
                "recipe_sha256": artifact["recipe_sha256"],
                "path": "/runtime/" + artifact["name"],
            }
            for field in ("reference", "digest", "checksum"):
                if field in artifact:
                    record[f"planned_{field}"] = artifact[field]
            if artifact["name"] in {"iterabase-platform-chart", "cert-manager-substrate-chart"}:
                record["reference"] += "#composed-runtime"
            if artifact["custody"] != "published-baseline":
                record["source_sha"] = SOURCE_SHA
            if artifact["kind"] == "image":
                record["digest"] = artifact.get("digest", "sha256:" + "c" * 64)
                record["config_digest"] = "sha256:" + "e" * 64
                record["checksum"] = "a" * 64
                observations[artifact["name"]] = "sha256:" + "f" * 64
            else:
                record["checksum"] = artifact.get("checksum", "d" * 64)
            bundle_artifacts.append(record)

        results = directory / "results"
        results.mkdir()
        bundle = {
            "schema_version": 1,
            "intent": plan["intent"],
            "source_sha": SOURCE_SHA,
            "plan_sha256": hash_file(plan_path),
            "catalogue_sha256": plan["catalogue_sha256"],
            "artifacts": bundle_artifacts,
        }
        bundle_path = results / "runtime-bundle.json"
        bundle_path.write_text(json.dumps(bundle, indent=2, sort_keys=True) + "\n", encoding="utf-8")
        result = {
            "schema_version": 1,
            "scenario_id": selected["id"],
            "status": "passed",
            "source_sha": SOURCE_SHA,
            "plan_sha256": hash_file(plan_path),
            "catalogue_sha256": plan["catalogue_sha256"],
            "runtime_bundle_sha256": hash_file(bundle_path),
            "stage_graph_sha256": selected["stage_graph_sha256"],
            "fixture_mode": "source",
            "artifacts": copy.deepcopy(bundle_artifacts),
            "fixture_evidence": [
                {
                    "name": "lifecycle",
                    "capacity": "gpu",
                    "host_key_sha256": "1" * 64,
                    "workspace_device": "/dev/disk/by-id/workspace",
                    "boot_id_before": "boot-before",
                    "boot_id_after": "boot-after",
                },
                {
                    "name": "model-cache",
                    "capacity": "gpu",
                    "host_key_sha256": "1" * 64,
                    "workspace_device": "/dev/disk/by-id/workspace",
                    "boot_id_before": "boot-before",
                    "boot_id_after": "boot-after",
                    "model_cache_device": "/dev/disk/by-id/model-cache",
                    "model_cache_mount": "/data/hf-cache",
                    "model_cache_uuid": "model-cache-uuid",
                    "model_id": "Qwen/Qwen3.5-0.8B",
                    "model_revision": "2" * 40,
                    "model_content_sha256": "3" * 64,
                },
            ],
            "stages": [
                {
                    "name": stage["name"],
                    "depends_on": stage.get("depends_on", []),
                    "status": "passed",
                }
                for stage in selected["stages"]
            ],
            "completed_at": "2026-09-01T00:00:00Z",
        }
        for artifact in result["artifacts"]:
            if artifact["kind"] == "image":
                artifact["runtime_digest"] = observations[artifact["name"]]
        result_path = results / "result.json"
        result_path.write_text(json.dumps(result) + "\n", encoding="utf-8")
        Path(str(result_path) + ".runtime-images.json").write_text(
            json.dumps(observations) + "\n", encoding="utf-8"
        )
        return plan_path, results, result

    def test_exact_result_set_and_stages_pass(self) -> None:
        with tempfile.TemporaryDirectory() as value:
            plan, results, _ = self.fixture(Path(value))
            validate_results(plan, results)

    def test_aggregate_rejects_missing_malformed_and_inconsistent_plan_outputs(self) -> None:
        with tempfile.TemporaryDirectory() as value:
            plan_path, results, _ = self.fixture(Path(value))
            plan = json.loads(plan_path.read_text())
            outputs = {
                "artifact_build_matrix": json.dumps(plan["artifact_build_matrix"], sort_keys=True, separators=(",", ":")),
                "scenario_matrix": json.dumps(plan["scenario_matrix"], sort_keys=True, separators=(",", ":")),
                "kind_matrix": "[]",
                "real_machine_matrix": json.dumps(plan["real_machine_matrix"], sort_keys=True, separators=(",", ":")),
                "has_artifacts": str(bool(plan["artifact_build_matrix"])).lower(),
                "has_scenarios": "true",
                "has_kind": "false",
                "has_real_machine": "true",
                "scenario_total": str(plan["scenario_total"]),
            }
            needs = {
                "plan": {"result": "success", "outputs": outputs},
                "runtime-contract": {"result": "success"},
                "artifacts": {"result": "success" if plan["artifact_build_matrix"] else "skipped"},
                "kind": {"result": "skipped"},
                "real-machine": {"result": "success"},
            }
            validate_results(plan_path, results, needs)
            for mutation in ("missing", "matrix", "status"):
                broken = copy.deepcopy(needs)
                if mutation == "missing":
                    del broken["plan"]["outputs"]["has_kind"]
                elif mutation == "matrix":
                    broken["plan"]["outputs"]["real_machine_matrix"] = "[]"
                else:
                    broken["kind"]["result"] = "success"
                with self.subTest(mutation=mutation), self.assertRaises(E2EError):
                    validate_results(plan_path, results, broken)

    def test_result_must_match_retained_runtime_artifact_identities(self) -> None:
        for field in ("reference", "digest", "config_digest", "checksum"):
            with self.subTest(field=field), tempfile.TemporaryDirectory() as value:
                plan, results, result = self.fixture(Path(value))
                artifact = next(
                    item for item in result["artifacts"]
                    if field in item and (field != "checksum" or item["kind"] != "image")
                )
                artifact[field] = "substituted"
                (results / "result.json").write_text(json.dumps(result) + "\n")
                with self.assertRaisesRegex(E2EError, "does not match the retained runtime identity"):
                    validate_results(plan, results)

    def test_result_must_match_retained_runtime_bundle_hash(self) -> None:
        with tempfile.TemporaryDirectory() as value:
            plan, results, result = self.fixture(Path(value))
            result["runtime_bundle_sha256"] = "b" * 64
            (results / "result.json").write_text(json.dumps(result) + "\n")
            with self.assertRaisesRegex(E2EError, "does not match its retained runtime bundle"):
                validate_results(plan, results)

    def test_runtime_bundle_must_retain_every_plan_known_identity(self) -> None:
        for field in ("reference", "digest", "checksum"):
            with self.subTest(field=field), tempfile.TemporaryDirectory() as value:
                plan, results, result = self.fixture(Path(value))
                bundle_path = results / "runtime-bundle.json"
                bundle = json.loads(bundle_path.read_text())
                planned = "planned_" + field
                artifact = next(item for item in bundle["artifacts"] if planned in item)
                artifact[planned] = "substituted"
                result_artifact = next(item for item in result["artifacts"] if item["name"] == artifact["name"])
                result_artifact[planned] = artifact[planned]
                bundle_path.write_text(json.dumps(bundle, indent=2, sort_keys=True) + "\n")
                result["runtime_bundle_sha256"] = hash_file(bundle_path)
                (results / "result.json").write_text(json.dumps(result) + "\n")
                with self.assertRaisesRegex(E2EError, f"does not retain planned {field}"):
                    validate_results(plan, results)

    def test_result_runtime_digest_must_match_retained_observation(self) -> None:
        with tempfile.TemporaryDirectory() as value:
            plan, results, result = self.fixture(Path(value))
            image = next(item for item in result["artifacts"] if item["kind"] == "image")
            observations_path = results / "result.json.runtime-images.json"
            observations = json.loads(observations_path.read_text())
            observations[image["name"]] = "sha256:" + "9" * 64
            observations_path.write_text(json.dumps(observations) + "\n")
            with self.assertRaisesRegex(E2EError, "does not match the observed runtime identity"):
                validate_results(plan, results)

    def test_missing_retained_runtime_bundle_fails(self) -> None:
        with tempfile.TemporaryDirectory() as value:
            plan, results, _ = self.fixture(Path(value))
            (results / "runtime-bundle.json").unlink()
            with self.assertRaisesRegex(E2EError, "cannot read"):
                validate_results(plan, results)

    def test_gpu_result_requires_distinct_exact_model_cache_evidence(self) -> None:
        for mutation in ("missing", "aliased", "floating", "corrupt"):
            with self.subTest(mutation=mutation), tempfile.TemporaryDirectory() as value:
                plan, results, result = self.fixture(Path(value))
                cache = next(item for item in result["fixture_evidence"] if item["name"] == "model-cache")
                if mutation == "missing":
                    result["fixture_evidence"].remove(cache)
                elif mutation == "aliased":
                    cache["model_cache_device"] = cache["workspace_device"]
                elif mutation == "floating":
                    cache["model_revision"] = "main"
                else:
                    cache["model_content_sha256"] = "corrupt"
                (results / "result.json").write_text(json.dumps(result) + "\n")
                with self.assertRaises(E2EError):
                    validate_results(plan, results)

    def test_missing_extra_skipped_and_blocked_results_fail(self) -> None:
        for mutation in ("missing", "extra", "skipped", "blocked"):
            with self.subTest(mutation=mutation), tempfile.TemporaryDirectory() as value:
                plan, results, result = self.fixture(Path(value))
                path = results / "result.json"
                if mutation == "missing":
                    path.unlink()
                elif mutation == "extra":
                    extra = copy.deepcopy(result)
                    extra["scenario_id"] = "extra/scenario"
                    (results / "extra.json").write_text(json.dumps(extra) + "\n")
                else:
                    result["stages"][0]["status"] = mutation
                    path.write_text(json.dumps(result) + "\n")
                with self.assertRaises(E2EError):
                    validate_results(plan, results)


class WorkflowContractTests(unittest.TestCase):
    def test_workflows_use_one_planner_composer_and_result_validator(self) -> None:
        for workflow in ("e2e.yml", "release-candidate.yml"):
            content = (ROOT / ".github" / "workflows" / workflow).read_text(encoding="utf-8")
            self.assertIn(".github/scripts/e2e.py", content)
            self.assertIn("e2e.py compose", content)
            self.assertIn("e2e.py validate-results", content)
            self.assertIn("runtime-bundle.json", content)
            self.assertIn("path: ${{ runner.temp }}/result/", content)
            self.assertNotIn("prepare_candidate_runtime.sh", content)
            self.assertNotIn("prepare_pr_workspace_runtime.sh", content)
        e2e = (ROOT / ".github/workflows/e2e.yml").read_text(encoding="utf-8")
        self.assertIn("github.event.pull_request.head.sha", e2e)
        self.assertIn("git rev-parse HEAD", e2e)
        self.assertIn("--intent pr", e2e)
        self.assertIn("--selection-file /tmp/changed-path-selection.json", e2e)
        self.assertIn("group: iterabase-permanent-fixture-${{ matrix.capacity }}", e2e)
        self.assertIn("cancel-in-progress: false", e2e)
        self.assertNotIn("schedule:", e2e)
        self.assertNotIn("workflow_dispatch:", e2e)
        self.assertNotIn("nightly", e2e)
        self.assertNotIn("control-plane-kind:", e2e)
        self.assertNotIn("charts-runtime:", e2e)


if __name__ == "__main__":
    unittest.main()
