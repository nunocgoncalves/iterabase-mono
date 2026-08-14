#!/usr/bin/env python3
from __future__ import annotations

import importlib.util
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
SPEC = importlib.util.spec_from_file_location("release_contract", Path(__file__).with_name("release.py"))
assert SPEC and SPEC.loader
release = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(release)


class ReleaseContractTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls) -> None:
        cls.catalogue = release.load_scenario_catalogue(ROOT)

    def setUp(self) -> None:
        self.targets = release.load_json(ROOT / "release" / "targets.json")
        self.sha = "a" * 40

    def plan(self, targets: str) -> dict:
        return release.make_plan(
            self.targets, targets, self.sha, "123", ROOT, self.catalogue
        )

    def check_availability(
        self,
        plan: dict | str,
        *,
        docker_status: int = 1,
        docker_output: str = "not found",
        helm_status: int = 1,
        helm_output: str = "not found",
    ) -> tuple[subprocess.CompletedProcess[str], str]:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            plan_path = root / "candidate-plan.json"
            plan_text = plan if isinstance(plan, str) else release.compact(plan) + "\n"
            plan_path.write_text(plan_text, encoding="utf-8")
            log_path = root / "commands.log"
            binaries: dict[str, str] = {}
            for name, status, output in (
                ("docker", docker_status, docker_output),
                ("helm", helm_status, helm_output),
            ):
                binary = root / name
                binary.write_text(
                    "#!/usr/bin/env bash\n"
                    f"printf '{name} %s\\n' \"$*\" >> \"$COMMAND_LOG\"\n"
                    f"printf '%s\\n' {json.dumps(output)}\n"
                    f"exit {status}\n",
                    encoding="utf-8",
                )
                binary.chmod(0o755)
                binaries[name] = str(binary)
            result = subprocess.run(
                [
                    "bash",
                    str(ROOT / ".github" / "scripts" / "check_release_availability.sh"),
                    str(plan_path),
                    "nunocgoncalves",
                ],
                check=False,
                capture_output=True,
                text=True,
                env={
                    **os.environ,
                    "COMMAND_LOG": str(log_path),
                    "DOCKER_BIN": binaries["docker"],
                    "HELM_BIN": binaries["helm"],
                },
            )
            commands = log_path.read_text(encoding="utf-8") if log_path.exists() else ""
            return result, commands

    def test_repository_contract_is_valid(self) -> None:
        release.validate_contract(self.targets, ROOT, self.catalogue)
        self.assertEqual(self.targets["schema_version"], 3)
        self.assertNotIn("suite_mapping_until", self.targets)
        for definition in self.targets["targets"].values():
            self.assertTrue(
                {"source_suites", "kind_scenarios", "real_machine", "chart_runtime"}.isdisjoint(definition)
            )

    def test_compiled_catalogue_preserves_or_strengthens_every_target_suite(self) -> None:
        expected = {
            "control-plane": (
                {"controlplane-identity", "inference-contract", "internal-tls", "tool-runner-contract"},
                set(),
            ),
            "inference-gateway": ({"inference-contract", "internal-tls"}, set()),
            "forge": (set(), {"cpu", "gpu"}),
            "control-plane-chart": ({"controlplane-identity", "tool-runner-contract"}, set()),
            "inference-gateway-chart": ({"inference-contract"}, set()),
            "iterabase-platform-chart": (
                {"controlplane-identity", "inference-contract", "tool-runner-contract"},
                {"cpu", "gpu"},
            ),
        }
        for target, (kind, real) in expected.items():
            with self.subTest(target=target):
                plan = self.plan(target)
                self.assertGreaterEqual({item["name"] for item in plan["kind_matrix"]}, kind)
                self.assertGreaterEqual(
                    {item["name"] for item in plan["real_machine_matrix"]}, real
                )
                self.assertTrue(
                    all(item["mandatory"] for item in plan["real_machine_matrix"])
                )
                self.assertEqual(plan["chart_runtime"], target.endswith("-chart"))

    def test_chart_owner_routes_without_duplicate_chart_release_jobs(self) -> None:
        image_only = self.plan("control-plane")
        internal_tls = next(
            item for item in image_only["kind_matrix"] if item["name"] == "internal-tls"
        )
        self.assertEqual(internal_tls["owner"], "charts")
        self.assertFalse(image_only["chart_runtime"])

        chart_release = self.plan("iterabase-platform-chart")
        self.assertTrue(chart_release["chart_runtime"])
        self.assertNotIn("charts", {item["owner"] for item in chart_release["kind_matrix"]})

    def test_coordinated_release_uses_the_catalogue_union_without_narrowing(self) -> None:
        selected = "control-plane,inference-gateway,forge,control-plane-chart,inference-gateway-chart,iterabase-platform-chart"
        plan = self.plan(selected)
        self.assertEqual(
            {item["name"] for item in plan["kind_matrix"]},
            {"controlplane-identity", "inference-contract", "tool-runner-contract"},
        )
        self.assertEqual(
            {item["name"] for item in plan["real_machine_matrix"]}, {"cpu", "gpu"}
        )
        self.assertEqual(
            set(plan["selected_scenarios"]),
            {
                "forge/digitalocean-cpu",
                "forge/digitalocean-gpu",
                "forge/kind-controlplane-identity",
                "forge/kind-inference-contract",
                "forge/kind-tool-runner-contract",
                "charts/certificate-ownership-migration",
                "charts/fresh-install",
                "charts/internal-tls",
                "charts/observability",
                "charts/observability-tls",
            },
        )

    def test_component_versions_are_local_authorities(self) -> None:
        self.assertEqual(release.read_version(ROOT / "control-plane" / "VERSION"), "0.0.26")
        self.assertEqual(release.read_version(ROOT / "inference-gateway" / "VERSION"), "0.2.6")
        self.assertEqual(release.read_version(ROOT / "forge" / "VERSION"), "0.8.2")
        self.assertFalse((ROOT / "release" / "compatibility.json").exists())

    def test_candidate_accepts_and_canonicalizes_an_explicit_target_set(self) -> None:
        plan = self.plan("forge, control-plane-chart,control-plane")
        self.assertEqual(
            plan["targets"], ["control-plane", "forge", "control-plane-chart"]
        )
        self.assertEqual(
            [(item["target"], item["version"], item["production_tag"]) for item in plan["releases"]],
            [
                ("control-plane", "0.0.26", "control-plane-v0.0.26"),
                ("forge", "0.8.2", "forge-v0.8.2"),
                ("control-plane-chart", "0.4.8", "control-plane-0.4.8"),
            ],
        )
        self.assertEqual(
            [item["name"] for item in plan["image_matrix"]],
            ["control-plane", "control-plane-harness", "control-plane-tool-runner"],
        )
        self.assertEqual([item["chart"] for item in plan["chart_matrix"]], ["control-plane"])
        self.assertTrue(plan["forge"])
        self.assertTrue(plan["real_machine"])
        self.assertEqual(len(plan["kind_matrix"]), 3)

    def test_single_forge_target_remains_supported(self) -> None:
        plan = self.plan("forge")
        self.assertEqual(plan["targets"], ["forge"])
        self.assertTrue(plan["forge"])
        self.assertTrue(plan["real_machine"])
        self.assertEqual(plan["releases"][0]["production_tag"], "forge-v0.8.2")
        self.assertEqual(plan["image_matrix"], [])
        self.assertEqual(plan["chart_matrix"], [])

    def test_chart_version_and_dependencies_come_from_chart_source(self) -> None:
        plan = self.plan("iterabase-platform-chart")
        self.assertEqual(plan["releases"][0]["version"], "0.3.10")
        self.assertEqual(plan["chart_matrix"][0]["companions"], ["cert-manager-substrate"])
        dependencies = {
            item["name"]: item["version"]
            for item in plan["tested_with"]["selected_chart_dependencies"][0]["dependencies"]
        }
        self.assertEqual(dependencies["control-plane"], "0.4.8")
        self.assertEqual(dependencies["inference-gateway"], "0.2.10")
        self.assertEqual(
            plan["tested_with"]["chart_metadata"]["control-plane"]["appVersion"],
            "0.0.25",
        )
        self.assertEqual(
            plan["tested_with"]["chart_metadata"]["inference-gateway"]["appVersion"],
            "0.2.5",
        )
        self.assertEqual(plan["tested_with"]["repository_versions"]["control-plane"], "0.0.26")
        self.assertEqual(
            plan["tested_with"]["repository_versions"]["inference-gateway"], "0.2.6"
        )

    def test_invalid_source_and_target_sets_are_rejected(self) -> None:
        for targets in ("", "everything", "forge,forge", "forge,", ",forge"):
            with self.subTest(targets=targets), self.assertRaises(release.ReleaseError):
                release.make_plan(self.targets, targets, self.sha, "1", ROOT)
        with self.assertRaises(release.ReleaseError):
            release.make_plan(self.targets, "forge", "short", "1", ROOT)
        with self.assertRaises(release.ReleaseError):
            release.make_plan(self.targets, "forge", self.sha, "run", ROOT)

    def test_bundle_uses_candidates_for_selected_members_and_published_baselines_otherwise(self) -> None:
        plan = self.plan("control-plane,control-plane-chart,forge")
        self.assertEqual(
            {item["target"] for item in plan["image_matrix"]}, {"control-plane"}
        )
        self.assertEqual(
            {item["target"] for item in plan["chart_matrix"]}, {"control-plane-chart"}
        )
        baseline_charts = {
            (item["chart"], item["version"])
            for item in plan["baseline_dependencies"]["charts"]
        }
        self.assertIn(("iterabase-platform", "0.3.1"), baseline_charts)
        baseline_images = plan["baseline_dependencies"]["images"]
        self.assertEqual({item["name"] for item in baseline_images}, {"inference-gateway"})
        self.assertEqual(
            baseline_images[0]["version_from"]["chart"], "iterabase-platform"
        )

    def test_runtime_baseline_graph_covers_forge_chart_and_image_only_fixtures(self) -> None:
        forge = self.plan("forge")
        self.assertEqual(
            {item["chart"] for item in forge["baseline_dependencies"]["charts"]},
            {"iterabase-platform", "cert-manager-substrate"},
        )
        self.assertEqual(
            {item["name"] for item in forge["baseline_dependencies"]["images"]},
            {"control-plane", "control-plane-tool-runner", "inference-gateway"},
        )

        control_chart = self.plan("control-plane-chart")
        self.assertIn(
            "cert-manager-substrate",
            {item["chart"] for item in control_chart["baseline_dependencies"]["charts"]},
        )
        self.assertEqual(
            {item["name"] for item in control_chart["baseline_dependencies"]["images"]},
            {"control-plane", "control-plane-tool-runner", "inference-gateway"},
        )

        image_only = self.plan("control-plane")
        self.assertIn(
            "inference-gateway",
            {item["name"] for item in image_only["baseline_dependencies"]["images"]},
        )

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
                "version": "0.2.6",
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
            self.assertEqual(verified["targets"], ["inference-gateway"])

            metadata["source_sha"] = "b" * 40
            (images / "candidate-inference-gateway.json").write_text(
                release.compact(metadata) + "\n", encoding="utf-8"
            )
            with self.assertRaisesRegex(release.ReleaseError, "checksums"):
                release.verify_candidate(root)

    def test_image_metadata_must_match_every_planned_identity_field(self) -> None:
        plan = self.plan("inference-gateway")
        valid = {
            "schema_version": 2,
            "artifact_type": "image",
            "name": "inference-gateway",
            "target": "inference-gateway",
            "repository": "ghcr.io/nunocgoncalves/inference-gateway",
            "candidate_tag": self.sha,
            "version": "0.2.6",
            "digest": "sha256:" + "1" * 64,
            "source_sha": self.sha,
        }
        changes = {
            "schema_version": 1,
            "repository": "ghcr.io/example/wrong",
            "candidate_tag": "b" * 40,
            "version": "9.9.9",
        }
        for field, value in changes.items():
            with self.subTest(field=field), tempfile.TemporaryDirectory() as directory:
                images = Path(directory) / "images"
                images.mkdir()
                metadata = {**valid, field: value}
                (images / "candidate-inference-gateway.json").write_text(
                    release.compact(metadata) + "\n", encoding="utf-8"
                )
                with self.assertRaisesRegex(release.ReleaseError, field):
                    release.validate_candidate_assets(plan, Path(directory))

    def test_duplicate_and_unexpected_image_metadata_are_rejected(self) -> None:
        plan = self.plan("inference-gateway")
        metadata = {
            **plan["image_matrix"][0],
            "schema_version": 2,
            "artifact_type": "image",
            "digest": "sha256:" + "1" * 64,
            "source_sha": self.sha,
        }
        with tempfile.TemporaryDirectory() as directory:
            images = Path(directory) / "images"
            images.mkdir()
            for name in ("candidate-inference-gateway.json", "candidate-copy.json"):
                (images / name).write_text(release.compact(metadata) + "\n", encoding="utf-8")
            with self.assertRaisesRegex(release.ReleaseError, "duplicated"):
                release.validate_candidate_assets(plan, Path(directory))

            (images / "candidate-copy.json").write_text(
                release.compact({**metadata, "name": "unexpected"}) + "\n",
                encoding="utf-8",
            )
            with self.assertRaisesRegex(release.ReleaseError, "unexpected"):
                release.validate_candidate_assets(plan, Path(directory))

    def test_forge_candidate_requires_four_archives_and_no_unpacked_binaries(self) -> None:
        plan = self.plan("forge")
        with tempfile.TemporaryDirectory() as directory:
            assets = Path(directory) / "forge"
            assets.mkdir(parents=True)
            for platform in ("linux_amd64", "linux_arm64", "darwin_amd64", "darwin_arm64"):
                (assets / f"forge_0.8.2_{platform}.tar.gz").write_bytes(platform.encode())
            (assets / "checksums.txt").write_text("fixture\n", encoding="utf-8")
            release.validate_candidate_assets(plan, Path(directory))
            self.assertFalse(any(path.name == "forge" for path in assets.rglob("*")))

    def test_chart_candidate_assets_use_the_flat_promotion_contract(self) -> None:
        plan = self.plan("iterabase-platform-chart")
        with tempfile.TemporaryDirectory() as directory:
            assets = Path(directory) / "charts"
            assets.mkdir(parents=True)
            (assets / "iterabase-platform-0.3.10.tgz").write_bytes(b"platform")
            (assets / "cert-manager-substrate-0.3.10.tgz").write_bytes(b"substrate")
            (assets / "checksums-iterabase-platform.txt").write_text(
                "fixture\n", encoding="utf-8"
            )
            release.validate_candidate_assets(plan, Path(directory))

    def test_candidate_bundle_evidence_binds_every_selected_release(self) -> None:
        plan = self.plan("control-plane-chart,iterabase-platform-chart")
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            assets = root / "assets" / "charts"
            assets.mkdir(parents=True)
            for chart, version in (
                ("control-plane", "0.4.8"),
                ("iterabase-platform", "0.3.10"),
                ("cert-manager-substrate", "0.3.10"),
            ):
                (assets / f"{chart}-{version}.tgz").write_bytes(chart.encode())
            for chart in ("control-plane", "iterabase-platform"):
                (assets / f"checksums-{chart}.txt").write_text("fixture\n", encoding="utf-8")
            (root / "candidate-plan.json").write_text(release.compact(plan) + "\n", encoding="utf-8")
            evidence = release.assemble_evidence(plan, root / "assets")
            (root / "candidate-evidence.json").write_text(
                release.compact(evidence) + "\n", encoding="utf-8"
            )
            verified = release.verify_candidate(root)
            self.assertEqual(
                verified["targets"], ["control-plane-chart", "iterabase-platform-chart"]
            )
            self.assertEqual(len(verified["releases"]), 2)

    def test_baseline_resolver_records_immutable_image_and_chart_identities(self) -> None:
        plan = self.plan("control-plane")
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            plan_path = root / "plan.json"
            plan_path.write_text(release.compact(plan) + "\n", encoding="utf-8")
            log_path = root / "commands.log"
            digest = "sha256:" + "a" * 64
            docker = root / "docker"
            docker.write_text(
                "#!/usr/bin/env bash\nprintf '\"%s\"\\n' \"$BASELINE_DIGEST\"\n",
                encoding="utf-8",
            )
            docker.chmod(0o755)
            helm = root / "helm"
            helm.write_text(
                "#!/usr/bin/env bash\n"
                "set -eu\n"
                "repository=$2\n"
                "destination=''\n"
                "version=''\n"
                "while [ $# -gt 0 ]; do case \"$1\" in --destination) destination=$2; shift 2;; --version) version=$2; shift 2;; *) shift;; esac; done\n"
                "chart=${repository##*/}\n"
                "fixture=$(mktemp -d)\n"
                "mkdir -p \"$fixture/$chart\" \"$destination\"\n"
                "printf 'apiVersion: v2\\nname: %s\\nversion: %s\\n' \"$chart\" \"$version\" > \"$fixture/$chart/Chart.yaml\"\n"
                "if [ \"$chart\" = iterabase-platform ]; then\n"
                "  mkdir -p \"$fixture/$chart/charts/control-plane\" \"$fixture/$chart/charts/inference-gateway\"\n"
                "  printf 'image:\\n  tag: 0.0.19\\ntoolRunner:\\n  image:\\n    tag: 0.0.19\\n' > \"$fixture/$chart/charts/control-plane/values.yaml\"\n"
                "  printf 'image:\\n  tag: 0.2.4\\n' > \"$fixture/$chart/charts/inference-gateway/values.yaml\"\n"
                "elif [ \"$chart\" = control-plane ]; then\n"
                "  printf 'image:\\n  tag: 0.0.19\\ntoolRunner:\\n  image:\\n    tag: 0.0.19\\n' > \"$fixture/$chart/values.yaml\"\n"
                "fi\n"
                "tar -czf \"$destination/$chart-$version.tgz\" -C \"$fixture\" \"$chart\"\n",
                encoding="utf-8",
            )
            helm.chmod(0o755)
            result = subprocess.run(
                ["bash", str(ROOT / ".github/scripts/resolve_release_baselines.sh"), str(plan_path)],
                check=False,
                capture_output=True,
                text=True,
                env={
                    **os.environ,
                    "DOCKER_BIN": str(docker),
                    "HELM_BIN": str(helm),
                    "BASELINE_DIGEST": digest,
                    "COMMAND_LOG": str(log_path),
                },
            )
            self.assertEqual(result.returncode, 0, result.stderr)
            resolved = release.load_json(plan_path)
            self.assertTrue(all(item["digest"] == digest for item in resolved["baseline_dependencies"]["images"]))
            self.assertRegex(resolved["baseline_dependencies"]["charts"][0]["sha256"], r"^[0-9a-f]{64}$")
            versions = {item["name"]: item["version"] for item in resolved["baseline_dependencies"]["images"]}
            self.assertEqual(versions["inference-gateway"], "0.2.4")

    def test_runtime_rejects_a_baseline_chart_that_does_not_match_the_plan_checksum(self) -> None:
        plan = {
            "source_sha": self.sha,
            "chart_matrix": [],
            "baseline_dependencies": {
                "images": [],
                "charts": [
                    {
                        "chart": "cert-manager-substrate",
                        "repository": "oci://example/cert-manager-substrate",
                        "version": "0.3.1",
                        "sha256": "f" * 64,
                    }
                ],
            },
        }
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            plan_path = root / "plan.json"
            plan_path.write_text(release.compact(plan) + "\n", encoding="utf-8")
            candidate = root / "candidates"
            candidate.mkdir()
            output = root / "environment"
            helm = root / "helm"
            helm.write_text(
                "#!/usr/bin/env bash\n"
                "set -eu\n"
                "repository=$2\n"
                "destination=''\n"
                "version=''\n"
                "while [ $# -gt 0 ]; do case \"$1\" in --destination) destination=$2; shift 2;; --version) version=$2; shift 2;; *) shift;; esac; done\n"
                "chart=${repository##*/}\n"
                "mkdir -p \"$destination\"\n"
                "printf different > \"$destination/$chart-$version.tgz\"\n",
                encoding="utf-8",
            )
            helm.chmod(0o755)
            result = subprocess.run(
                [
                    "bash",
                    str(ROOT / ".github/scripts/prepare_candidate_runtime.sh"),
                    str(plan_path),
                    str(candidate),
                    str(output),
                ],
                cwd=root,
                check=False,
                capture_output=True,
                text=True,
                env={**os.environ, "HELM_BIN": str(helm)},
            )
            self.assertNotEqual(result.returncode, 0)
            self.assertIn("did NOT match", result.stderr)

    def test_promotion_preflights_existing_release_asset_bytes(self) -> None:
        plan = self.plan("control-plane")
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            candidate = root / "candidate"
            images = candidate / "assets" / "images"
            images.mkdir(parents=True)
            (candidate / "candidate-plan.json").write_text(
                release.compact(plan) + "\n", encoding="utf-8"
            )
            (candidate / "candidate-evidence.json").write_text(
                '{"schema_version":3}\n', encoding="utf-8"
            )
            for image in plan["image_matrix"]:
                name = image["name"]
                (images / f"candidate-{name}.json").write_text(
                    "metadata\n", encoding="utf-8"
                )
                (images / f"candidate-{name}.spdx.json").write_text(
                    "sbom\n", encoding="utf-8"
                )

            docker = root / "docker"
            docker.write_text(
                "#!/usr/bin/env bash\nprintf 'not found\\n' >&2\nexit 1\n",
                encoding="utf-8",
            )
            docker.chmod(0o755)
            helm = root / "helm"
            helm.write_text("#!/usr/bin/env bash\nexit 1\n", encoding="utf-8")
            helm.chmod(0o755)
            existing = root / "existing-plan.json"
            existing.write_bytes((candidate / "candidate-plan.json").read_bytes())
            gh = root / "gh"
            gh.write_text(
                "#!/usr/bin/env bash\n"
                "set -eu\n"
                "case \" $* \" in\n"
                "  *\" --repo $EXPECTED_GITHUB_REPOSITORY \"*) ;;\n"
                "  *) printf 'unexpected --repo argument: %s\\n' \"$*\" >&2; exit 2 ;;\n"
                "esac\n"
                "if [ \"$2\" = view ]; then\n"
                "  printf '%s\\n' '{\"tagName\":\"control-plane-v0.0.26\",\"assets\":[{\"name\":\"candidate-plan.json\"}]}'\n"
                "elif [ \"$2\" = download ]; then\n"
                "  destination=''\n"
                "  while [ $# -gt 0 ]; do case \"$1\" in --dir) destination=$2; shift 2;; *) shift;; esac; done\n"
                "  cp \"$EXISTING_RELEASE_ASSET\" \"$destination/candidate-plan.json\"\n"
                "fi\n",
                encoding="utf-8",
            )
            gh.chmod(0o755)
            command = [
                "bash",
                str(ROOT / ".github/scripts/check_promotion_destinations.sh"),
                str(candidate),
                "nunocgoncalves",
                "nunocgoncalves/iterabase-mono",
            ]
            environment = {
                **os.environ,
                "DOCKER_BIN": str(docker),
                "HELM_BIN": str(helm),
                "GH_BIN": str(gh),
                "EXPECTED_GITHUB_REPOSITORY": "nunocgoncalves/iterabase-mono",
                "EXISTING_RELEASE_ASSET": str(existing),
            }
            matching = subprocess.run(
                command, check=False, capture_output=True, text=True, env=environment
            )
            self.assertEqual(matching.returncode, 0, matching.stderr)

            existing.write_text("conflict\n", encoding="utf-8")
            conflicting = subprocess.run(
                command, check=False, capture_output=True, text=True, env=environment
            )
            self.assertNotEqual(conflicting.returncode, 0)
            self.assertIn("conflicts with the candidate", conflicting.stderr)

    def test_candidate_preflight_rejects_existing_semantic_image_versions(self) -> None:
        result, _ = self.check_availability(
            self.plan("inference-gateway"), docker_status=0, docker_output="manifest"
        )
        self.assertNotEqual(result.returncode, 0)
        self.assertIn(
            "ghcr.io/nunocgoncalves/inference-gateway:0.2.6 already exists",
            result.stderr,
        )

    def test_candidate_preflight_allows_missing_semantic_versions(self) -> None:
        result, commands = self.check_availability(self.plan("iterabase-platform-chart"))
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn(
            "helm show chart oci://ghcr.io/nunocgoncalves/iterabase-charts/iterabase-platform --version 0.3.10",
            commands,
        )
        self.assertIn(
            "helm show chart oci://ghcr.io/nunocgoncalves/iterabase-charts/cert-manager-substrate --version 0.3.10",
            commands,
        )

    def test_candidate_preflight_fails_closed_when_registry_is_unavailable(self) -> None:
        result, _ = self.check_availability(
            self.plan("control-plane"), docker_output="connection reset by peer"
        )
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("could not verify", result.stderr)

    def test_candidate_preflight_rejects_malformed_plan_before_registry_checks(self) -> None:
        result, commands = self.check_availability('{"image_matrix": [')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("parse error", result.stderr)
        self.assertEqual(commands, "")

    def test_workflows_are_split_and_promotion_keeps_environment_gate(self) -> None:
        candidate = (ROOT / ".github" / "workflows" / "release-candidate.yml").read_text(
            encoding="utf-8"
        )
        promotion = (ROOT / ".github" / "workflows" / "release-promote.yml").read_text(
            encoding="utf-8"
        )
        self.assertIn("targets:", candidate)
        self.assertNotIn("environment: release", candidate)
        self.assertIn("candidate_run_id:", promotion)
        self.assertIn("environment: release", promotion)
        self.assertNotIn("candidate_namespace", candidate)
        self.assertNotIn("iterabase-release-candidates", candidate)
        self.assertNotIn("compatibility.json", candidate + promotion)
        self.assertNotIn("Reuse an existing immutable full-SHA candidate", candidate)
        self.assertIn("Reject an existing unverified full-SHA alias", candidate)
        self.assertIn("Reject existing semantic artifacts before candidate validation", candidate)
        self.assertIn("check_release_availability.sh candidate-plan.json", candidate)
        self.assertIn("resolve_release_baselines.sh candidate-plan.json", candidate)
        self.assertIn("check_promotion_destinations.sh \\\n            candidate", promotion)
        self.assertIn("Preflight every semantic destination before publication", promotion)
        self.assertIn("Create or verify protected namespaced tags", promotion)
        self.assertIn("candidate_bundle: true", candidate)
        self.assertIn("path: candidate-charts/", candidate)
        self.assertIn(
            "> 'candidate-charts/candidate-chart-${{ matrix.chart }}.json'", candidate
        )
        self.assertIn(
            "output-file: candidate-charts/candidate-chart-${{ matrix.chart }}.spdx.json",
            candidate,
        )
        kind = candidate.split("  kind-candidates:\n", 1)[1].split(
            "\n  real-machine-candidates:\n", 1
        )[0]
        real_machine = candidate.split("  real-machine-candidates:\n", 1)[1].split(
            "\n  validation:\n", 1
        )[0]
        self.assertIn("name: candidate-plan", kind)
        self.assertIn("name: candidate-plan", real_machine)
        self.assertIn(
            "needs: [preflight, image-candidates, forge-candidate, chart-candidate]",
            real_machine,
        )
        self.assertIn("prepare_candidate_runtime.sh", real_machine)
        self.assertIn("--release-notes /dev/null", candidate)
        self.assertIn("prepare_candidate_runtime.sh", candidate)
        runtime_helper = (
            ROOT / ".github" / "scripts" / "prepare_candidate_runtime.sh"
        ).read_text(encoding="utf-8")
        self.assertIn('printf \'%s  %s\\n\' "$checksum" "$archive" | sha256sum --check -', runtime_helper)

    def test_candidate_validation_allows_unselected_jobs_to_skip(self) -> None:
        for target_set in (*release.TARGET_NAMES, "control-plane,control-plane-chart,forge"):
            with self.subTest(targets=target_set):
                plan = self.plan(target_set)
                selected = release.candidate_job_selection(plan)
                needs = {
                    name: {"result": "success" if required else "skipped"}
                    for name, required in selected.items()
                }
                self.assertIn(False, selected.values())
                release.validate_candidate_job_results(plan, needs)

    def test_candidate_validation_rejects_selected_job_skips(self) -> None:
        plan = self.plan("control-plane")
        selected = release.candidate_job_selection(plan)
        valid_needs = {
            name: {"result": "success" if required else "skipped"}
            for name, required in selected.items()
        }
        for name, required in selected.items():
            if not required:
                continue
            with self.subTest(job=name):
                needs = {job: dict(value) for job, value in valid_needs.items()}
                needs[name]["result"] = "skipped"
                with self.assertRaisesRegex(release.ReleaseError, name):
                    release.validate_candidate_job_results(plan, needs)

    def test_candidate_evidence_runs_after_expected_target_skips(self) -> None:
        candidate = (ROOT / ".github" / "workflows" / "release-candidate.yml").read_text(
            encoding="utf-8"
        )
        self.assertIn(
            "python3 workflow-validator/.github/scripts/release.py validate-jobs",
            candidate,
        )
        self.assertIn(
            """  evidence:
    name: Assemble immutable candidate record
    if: >-
      always() &&
      needs.preflight.result == 'success' &&
      needs.validation.result == 'success'
    needs: [preflight, validation]
""",
            candidate,
        )

    def test_bundle_chart_assets_require_every_selected_archive(self) -> None:
        plan = self.plan("control-plane-chart,iterabase-platform-chart")
        with tempfile.TemporaryDirectory() as directory:
            assets = Path(directory) / "charts"
            assets.mkdir(parents=True)
            for chart, version in (
                ("control-plane", "0.4.8"),
                ("iterabase-platform", "0.3.10"),
                ("cert-manager-substrate", "0.3.10"),
            ):
                (assets / f"{chart}-{version}.tgz").write_bytes(chart.encode())
            for chart in ("control-plane", "iterabase-platform"):
                (assets / f"checksums-{chart}.txt").write_text("fixture\n", encoding="utf-8")
            release.validate_candidate_assets(plan, Path(directory))
            (assets / "control-plane-0.4.8.tgz").unlink()
            with self.assertRaisesRegex(release.ReleaseError, "control-plane"):
                release.validate_candidate_assets(plan, Path(directory))

    def test_candidate_validation_supports_an_older_master_source(self) -> None:
        candidate = (ROOT / ".github" / "workflows" / "release-candidate.yml").read_text(
            encoding="utf-8"
        )
        self.assertIn(
            "steps.resolved.outputs.real_machine_matrix || '[{\"name\":\"cpu\"",
            candidate,
        )
        legacy_plan = self.plan("forge")
        legacy_plan.pop("real_machine_matrix")
        selected = release.candidate_job_selection(legacy_plan)
        self.assertTrue(selected["real-machine-candidates"])
        validation = candidate.split("  validation:\n", 1)[1].split("\n  evidence:\n", 1)[0]
        self.assertIn("PLAN: ${{ needs.preflight.outputs.plan }}", validation)
        self.assertIn("ref: ${{ github.workflow_sha }}", validation)
        self.assertIn("path: workflow-validator", validation)
        self.assertNotIn("ref: ${{ inputs.master_sha }}", validation)
        self.assertIn(
            "python3 workflow-validator/.github/scripts/release.py validate-jobs",
            validation,
        )

    def test_platform_chart_keeps_mandatory_real_machine_validation(self) -> None:
        plan = self.plan("iterabase-platform-chart")
        self.assertTrue(plan["real_machine"])
        self.assertEqual(
            {item["capacity"] for item in plan["real_machine_matrix"]}, {"cpu", "gpu"}
        )

    def test_e2e_workflows_record_exact_fixture_modes_without_floating_latest(self) -> None:
        workflow = (ROOT / ".github" / "workflows" / "e2e.yml").read_text()
        self.assertNotIn("FORGE_E2E_USE_LATEST_RELEASE", workflow)
        self.assertNotIn("published-latest", workflow)
        self.assertIn("ITERABASE_E2E_FIXTURE_MODE: source", workflow)
        self.assertEqual(workflow.count("ITERABASE_E2E_SOURCE_INPUTS:"), 2)
        self.assertIn("ITERABASE_E2E_FIXTURE_MODE: published", workflow)
        candidate = (ROOT / ".github" / "workflows" / "release-candidate.yml").read_text()
        self.assertIn("real_machine_matrix", candidate)
        self.assertIn("include: ${{ fromJSON(needs.preflight.outputs.real_machine_matrix) }}", candidate)
        self.assertIn("ITERABASE_E2E_FIXTURE_MODE: candidate", candidate)
        self.assertIn("ITERABASE_E2E_CANDIDATE_PLAN", candidate)

    def test_release_workflows_never_publish_from_push_or_tag_events(self) -> None:
        for name in ("release-candidate.yml", "release-promote.yml", "release-rehearsal.yml"):
            workflow = (ROOT / ".github" / "workflows" / name).read_text(encoding="utf-8")
            self.assertIn("workflow_dispatch:", workflow)
            self.assertNotIn("push:", workflow)


if __name__ == "__main__":
    unittest.main()
