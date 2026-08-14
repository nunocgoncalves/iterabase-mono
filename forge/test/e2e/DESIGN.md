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
7. Re-apply the current release and assert the reality-derived action is `skip` while every configured phase reconciles successfully.
8. Materialize an overlay-declared Secret from an operator environment variable and verify its value/type.
9. Reconcile Flux again and assert controllers, source artifact, and Kustomization readiness.

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
8. Apply identity/catalog resources, wait for real vLLM availability, assert rendered extra arguments, and request a real completion.

The inference path depends on GPU readiness, so a second VM/operator installation added cost and capacity noise rather than isolation. The chart is introduced only after the substrate and driver-transition assertions, preserving fault localization. `make test-e2e-gpu-broken-policy` is the intentional HOR-411 red proof: it builds the same source through a Go overlay that changes only `gpuPodDeletion.deleteEmptyDir` to `false`, requires real capacity, and passes only when the active baseline `emptyDir` workload drives the normal fixture through `pod-deletion-required` into `upgrade-failed`. A manual E2E workflow dispatch with `gpu_policy_red_proof=true` runs that mutation using CI's GPU credential; the ordinary PR, nightly, and release paths always run the unmodified green scenario.

### Kind — fresh cluster per contract

- `kind-controlplane-identity`: standalone control-plane chart and identity/JWT contract.
- `kind-inference-contract`: umbrella cross-service catalog/auth contract.
- `kind-cert-issuers`: minimal cert-manager/self-signed issuer values contract.
- `kind-internal-tls`: two-phase Helm transition and live internal transport contract.
- `kind-tool-runner-contract`: exact Flux artifact materialization through the chart-managed Node runner, mTLS gateway registration, and pinned generation drain using the monorepo-local control-plane/charts builds.

These remain isolated because clean chart installation, cluster-scoped CRDs/issuers, hooks, and value combinations are part of what they test. Sharing a cluster could mask missing resources or leak state between releases.

## Coverage mapping

| Previous test | New scenario/stage | Coverage |
|---|---|---|
| `TestE2E` | `digitalocean-cpu/install-migration-source` through `reapply-current-idempotently` | k3s, real Helm 4 migration, certificate handoff, exact Flux source, runner readiness, overlay, node, dual-stack, and gateway edges |
| `TestGPUE2E_PreflightFail` | `digitalocean-cpu/reject-gpu-on-cpu-host` | no-GPU preflight refusal; now actually selected by CI |
| `TestE2EOverlay` | `digitalocean-cpu/apply-public-overlay` | public tokenless clone, commit, values, CRD path |
| `TestE2ESecrets` | `digitalocean-cpu/sync-secrets` | env → SSH stdin → Kubernetes Secret |
| `TestE2EFlux` | `digitalocean-cpu/install-and-reconcile-flux` | controllers, GitRepository artifact, Kustomization |
| `TestGPUE2E` | `digitalocean-gpu/apply-gpu-substrate`, `assert-gpu-smoke` | operator readiness and usable GPU |
| HOR-411 / HOR-485 | `digitalocean-gpu/start-emptydir-workload` through `assert-driver-upgrade` | exact supported driver transition, disposable `emptyDir`, preserved PVC cache, operator/node convergence, and post-upgrade GPU use |
| `TestInferenceFlowGPU` | `digitalocean-gpu/apply-platform`, `run-real-inference` | real control-plane/vLLM/gateway completion |
| `TestControlPlaneIdentity` | `kind-controlplane-identity` | unchanged, fresh Kind cluster |
| `TestInferenceFlowContract` | `kind-inference-contract` | unchanged plus restored PermissionPolicy materialization |
| `TestCertIssuers` | `kind-cert-issuers` | unchanged, fresh Kind cluster |
| `TestInternalTLS` | `kind-internal-tls` | unchanged, fresh Kind cluster |
| HOR-397 cross-component acceptance | `kind-tool-runner-contract` | Flux artifact → materializer → runner → mTLS registration → pinned drain |

## CI

One `e2e.yml` workflow owns:

- fast harness compilation, unit tests, and nested-module lint;
- one serialized CPU cloud job against reviewed published releases;
- one serialized GPU cloud job against reviewed published releases;
- a fail-fast-disabled Kind PR matrix with one fresh runner/cluster per contract;
- a nightly, non-PR-gating Kind compatibility matrix against explicitly pinned published artifacts.

For a monorepo ticket PR, the Kind matrix composes the control-plane and chart source from the same checkout. Standard Kind scenarios install the local chart source; the tool-runner contract additionally builds the local control-plane and runner images. Scheduled compatibility runs use explicit published releases. Cloud jobs intentionally use distributable releases because their boundary is Forge's real remote OCI installation path, not local source mounting.

The nightly compatibility matrix records an exact published fixture and never floats to `latest`; intentional compatibility-baseline changes are reviewed in source. All E2E invocations are verbose so capacity skips, exact driver inputs, Forge identity, and stage results are visible. Candidate validation retains mandatory GPU capacity semantics; a missing credential or exhausted capacity fails rather than skipping. Cloud jobs never cancel in progress, allowing test cleanup to destroy VMs; the tagged reaper remains the crash safety net.

## Rejected alternatives

- **Merged mega-suite/shared Kind cluster:** weaker artifact-boundary signal and cluster-scoped state leakage.
- **Fresh VM for every forge phase:** repeated provisioning and k3s/GPU installation without an independent requirement.
- **Forge-private runner:** replaced by the approved monorepo `testkit/e2e`; owner assertions remain here while mechanics and compiled metadata are shared.

## Production impact

None. This changes test infrastructure only. `FORGE_E2E_KEEP` retains either cloud fixture for debugging; normal cleanup and the reaper remain in place.
