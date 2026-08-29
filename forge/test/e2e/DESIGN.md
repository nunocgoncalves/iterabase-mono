# Forge E2E runner design

Decision: HOR-406, approved 2026-08-03.

## Goals

- Expose one Go test entrypoint and one GitHub Actions workflow.
- Compose scenarios from named stages and shared fixtures.
- Preserve every existing boundary assertion while avoiding redundant cloud setup.
- Keep isolation where clean installation is part of the contract.

## Runner

`TestE2E` is the only top-level infrastructure test. It registers typed scenarios/stages through the shared `testkit/e2e`; `go test -run` selects a scenario and each scenario reports named stage subtests. Failed/skipped prerequisites suppress only dependent stages, independent stages continue, and cleanup/diagnostics still run. Catalogue mode compiles these exact registrations without provisioning infrastructure.

## Isolation boundary

### DigitalOcean CPU — one VM

Stages:

1. Provision a cloud-init-complete CPU host.
2. Assert `gpu.enabled` is rejected when no NVIDIA GPU exists.
3. Install the explicit `0.2.2` migration source with the local MetalLB edge overlay.
4. Assert node readiness, dual-stack pod CIDRs, label propagation, gateway pod readiness, and HTTPS/MetalLB health.
5. Upgrade to the reviewed current platform release through the public E2E overlay: exact Flux source first, certificate ownership handoff, companion substrate, platform, then CR reconciliation.
6. Assert both Helm versions, certificate CRD ownership, exact Flux revision/digest, tool-runner readiness, and HTTPS health through the fixture's NodePort edge.
7. Create a two-worker managed-RWX AgentPool, capture the first worker UIDs after its claim is Bound, and prove that exact set reaches Ready without initial-attachment churn while durable operational readiness is recorded.
8. Delete the established pool's share-manager and prove fail-closed scheduling removal, a durable replacement-pending marker, preserved claim identity, a bounded zero-client detach/reset, and recovery only through two fresh workers.
9. Re-apply the current release and assert the reality-derived action is `skip` while every configured phase reconciles successfully.
10. Materialize an overlay-declared Secret from an operator environment variable and verify its value/type.
11. Reconcile Flux again and assert controllers, source artifact, and Kustomization readiness.

The old release is an intentional migration input, never the desired test target. Only migration setup needs a fresh host. Current-platform, secret-sync, and Flux checks compose on that host so this scenario covers the exact `0.2.2` → `0.3+` ownership and Helm-status path used by OPO1.

### DigitalOcean GPU — one VM

Stages:

1. Record the exact source/candidate Forge identity and the supported `580.126.20` → `595.71.05` driver inputs.
2. Apply k3s + GPU operator with the exact baseline driver, without the platform chart.
3. Assert ClusterPolicy readiness and run a real GPU smoke pod.
4. Start a representative GPU Deployment with memory-backed `/dev/shm` `emptyDir` and a separate PVC-backed cache sentinel.
5. Run one Forge reconciliation with the exact candidate driver.
6. Assert the workload pod was deleted/recreated, `emptyDir` state was fresh, PVC state survived, ClusterPolicy selected the candidate with the pinned deletion/drain policy, the node reached Ready + schedulable + `upgrade-done`, and the recreated workload reported the candidate through `nvidia-smi`.
7. Reconcile the reviewed current platform release on the same host with exact Flux source + companion certificate substrate and the already-proven GPU phase skipped.
8. Apply the minimal identity/catalog fixture, wait for real vLLM availability, and request one completion as non-authoritative infrastructure-serving smoke.

The inference path depends on GPU readiness, so a second VM/operator installation added cost and capacity noise rather than isolation. The chart is introduced only after the substrate and driver-transition assertions, preserving fault localization. Under HOR-494, candidate readiness is generation-current only when ClusterPolicy `Ready=True`/`Error=False`, the selected and node-reported drivers equal the requested candidate, and the node is Ready, schedulable, and `upgrade-done`. The legacy ClusterPolicy state remains explicit evidence: `ready` is normal, while `notReady` is accepted only for the documented GPU Operator status-write conflict when every stronger current signal agrees. The recreated workload and its `nvidia-smi` result remain an independent post-upgrade proof.

`make test-e2e-gpu-broken-policy` is the intentional HOR-411 red proof: it builds the same source through a Go overlay that changes only `gpuPodDeletion.deleteEmptyDir` to `false`, requires real capacity, and passes only when the active baseline `emptyDir` workload drives the normal fixture through `pod-deletion-required` into `upgrade-failed`. A manual E2E workflow dispatch with `gpu_policy_red_proof=true` runs that mutation using CI's GPU credential; the ordinary PR, nightly, and release paths always run the unmodified green scenario.

### Kind ownership cleanup

Forge registers no Kind scenario after HOR-481. The removed scenarios installed charts directly and never exercised Forge:

- `kind-controlplane-identity` maps to control-plane `deployed-identity-api` (HOR-478).
- `kind-inference-contract` maps to control-plane `deployed-execution-contracts` (HOR-477).
- `kind-tool-runner-contract` maps to the same execution scenario for immutable tool generation, materialization, registration, pin/drain/retirement, invocation, and artifact attribution (HOR-477).
- Certificate substrate, issuer, internal-TLS, observability, install, upgrade, reapply, and rollback authority maps to the chart-owned compiled suite (HOR-416/HOR-475).

Those replacements passed their owner PRs and required source/candidate-backed gates before deletion. Forge uses Kubernetes helpers only against a kubeconfig fetched from a real Forge-provisioned host. A future Forge Kind scenario is valid only if it invokes Forge-owned behavior or proves a substrate operation that does not require a real machine; direct chart-install-only coverage must remain with charts/control-plane.

## Coverage mapping

| Previous test | New scenario/stage | Coverage |
|---|---|---|
| `TestE2E` | `digitalocean-cpu/install-migration-source` through `reapply-current-idempotently` | k3s, real Helm 4 migration, certificate handoff, exact Flux source, runner readiness, overlay, node, dual-stack, gateway edges, and managed-RWX initial/post-ready AgentPool convergence |
| `TestGPUE2E_PreflightFail` | `digitalocean-cpu/reject-gpu-on-cpu-host` | no-GPU preflight refusal; now actually selected by CI |
| `TestE2EOverlay` | `digitalocean-cpu/apply-public-overlay` | public tokenless clone, commit, values, CRD path |
| `TestE2ESecrets` | `digitalocean-cpu/sync-secrets` | env → SSH stdin → Kubernetes Secret |
| `TestE2EFlux` | `digitalocean-cpu/install-and-reconcile-flux` | controllers, GitRepository artifact, Kustomization |
| `TestGPUE2E` | `digitalocean-gpu/apply-gpu-substrate`, `assert-gpu-smoke` | operator readiness and usable GPU |
| HOR-411 / HOR-485 / HOR-494 | `digitalocean-gpu/start-emptydir-workload` through `assert-driver-upgrade` | exact supported driver transition, generation-current readiness authority, disposable `emptyDir`, preserved PVC cache, operator/node convergence, and post-upgrade GPU use |
| `TestInferenceFlowGPU` | `digitalocean-gpu/apply-dependent-platform-smoke`, `run-real-serving-smoke` | minimal real control-plane/vLLM/gateway request proving usable infrastructure; product correctness is non-authoritative here |
| `TestControlPlaneIdentity` | `control-plane/deployed-identity-api` | migrated to product authority; bootstrap/JWKS/scopes/identity deletion plus broader API/recovery evidence |
| `TestInferenceFlowContract` | `control-plane/deployed-execution-contracts` | migrated to product authority with workload mTLS and a deterministic OpenAI-compatible backend |
| `TestCertIssuers` | `charts/fresh-install` | migrated to chart authority; issuer and CSI identity on fresh Kind |
| `TestInternalTLS` | `charts/internal-tls` | migrated to chart authority; verified HTTPS plus rejected plaintext datastore transport |
| HOR-397 cross-component acceptance | `control-plane/deployed-execution-contracts` | Flux artifact → materializer → runner → mTLS registration plus generation pin/drain/retirement, invocation, and artifact attribution |

## HOR-406 harness/fixture reconciliation

The composable runner remains one `TestE2E` entrypoint with typed shared-testkit stages, prerequisite blocking, diagnostics, and cleanup hooks. HOR-481 intentionally removes Forge-private F0 code that existed only to support the deleted Kind product/chart scenarios:

- historical floating GitHub chart-release pagination, semver, and `appVersion` helpers are superseded by explicit source/candidate/published fixture records and release-contract tests; floating latest already failed closed;
- tool-runner packaged-chart dependency preparation is superseded by control-plane's candidate execution owner plus `prepare_candidate_runtime.sh` checksum/digest verification;
- Forge-private Kind create/chart/JWT mechanics are superseded by shared testkit mechanics in the chart/control-plane owners.

Forge retains its exact candidate overlay/chart/image-digest tests, GPU transition policy/evidence tests, and hermetic runner example. This is a reviewed ownership migration, not silent loss of HOR-406 coverage.

## CI

One `e2e.yml` workflow owns:

- fast harness compilation, unit tests, and nested-module lint;
- one serialized CPU cloud job against reviewed published releases;
- one serialized GPU cloud job against reviewed published releases;
- owner-local control-plane and chart Kind jobs, selected by those owners rather than Forge;
- no Forge-published Kind compatibility matrix after its product/chart scenarios moved to their owners.

Published compatibility and migration baselines use distributable releases because their boundary is Forge's real remote OCI installation path. When the PR selector chooses `digitalocean-cpu`, the job additionally packages the exact-head managed companions, builds the exact-head control-plane, tool-runner, and harness images, imports those images into the ephemeral host without publishing them, and fails closed unless the managed AgentPool path executes. Candidate runs consume the exact Forge binary, chart archives, image digests, and immutable published baselines in the generated plan. Every CPU/GPU real-machine composition keeps only the synthetic storage-readiness dispatch permission needed by its fixture; portable dispatch behavior remains authoritative in control-plane E2E. The CPU chart/tool-runner/gateway checks and GPU completion are explicitly dependent smoke, not product or chart authority.

Owner-unit validation proves the production CPU/GPU create requests carry the `forge-e2e` reaper tag, forces provisioning failures after each cloud resource identity exists, and runs the registered owner cleanup hooks with both deletion paths forced to fail. The retained cleanup evidence binds the exact resource ID to its reaper tags. The reaper's two-hour default exceeds the longest 115-minute Forge workflow bound; its seam proves a host at that active-run boundary is retained, expired tagged orphans are deleted, and one delete failure does not suppress later deletions or report a false-success run.

The checked-in published fixture never floats to `latest`; intentional compatibility-baseline changes are reviewed in source. Forge's reviewed current platform/certificate-substrate pair is `0.3.11`; the chart owner's checksum-pinned `0.3.10` transition predecessors remain separate inputs and never replace that current runtime pair during candidate planning. All E2E invocations are verbose so capacity skips, exact driver inputs, Forge identity, failure domain, and stage results are visible. Shared redacted cluster diagnostics run before shared cleanup hooks. Candidate validation retains mandatory CPU/GPU capacity semantics; a missing credential or exhausted capacity fails rather than skipping. Cloud jobs never cancel in progress, allowing cleanup to destroy VMs; the root tagged reaper remains the crash/cancel safety net.

## Rejected alternatives

- **Merged mega-suite/shared Kind cluster:** weaker artifact-boundary signal and cluster-scoped state leakage.
- **Fresh VM for every forge phase:** repeated provisioning and k3s/GPU installation without an independent requirement.
- **Forge-private runner:** replaced by the approved monorepo `testkit/e2e`; owner assertions remain here while mechanics and compiled metadata are shared.

## Production impact

The runner remains test infrastructure: `FORGE_E2E_KEEP` retains either cloud fixture for debugging; normal cleanup and the reaper remain in place. HOR-494 also changes Forge's production GPU readiness authority, documented in `forge/README.md`, and requires a semantic Forge publication before acceptance.
