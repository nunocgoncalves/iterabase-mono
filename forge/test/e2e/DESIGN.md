# Forge E2E runner design

Original composition decision: HOR-406, approved 2026-08-03. Permanent-fixture
and exact-artifact cutover: `DES-HOR-540-01` and `DES-HOR-540-02`, approved
2026-09-01.

## Goals

- One compiled `TestE2E` entrypoint and one stage DAG per scenario in source and
  candidate modes.
- Exact composer-verified images, charts/companion, Forge binary, runtime
  fixture, plan, and metadata; no owner-local build/load fallback.
- Real CPU/GPU substrate behavior without provider availability or provider API
  credentials in Actions.
- A proven clean destroy/apply/test/destroy boundary on dedicated, reimageable
  fixtures.
- Fail-closed fixture, artifact, stage, and result identities with no retry or
  selected-capacity skip.

## Permanent fixture authority

F3 uses exactly one founder-provisioned CPU host and one founder-provisioned GPU
host. Repository variables supply each fixed address, SSH user, pinned OpenSSH
host public key, and Forge workspace `/dev/disk/by-id/...` identity. A separate
repository secret supplies each fixture-scoped private key. These values are not
workflow-dispatch inputs.

Every fixture path uses the literal `iterabase-permanent-fixtures` concurrency
group with `cancel-in-progress: false`: PR, complete nightly/manual, candidate,
intentional-red, diagnostics, and cleanup execution are globally serial. CPU and
GPU never overlap. Build/unit/F2 work remains parallel.

Actions has no provider credential. It cannot list, create, delete, resize,
power-cycle, rescue, reimage, or replace a fixture. If strict SSH cleanup and
reboot cannot recover a host, F3 stops until founder-operated provider recovery
restores the runbook baseline.

## Lifecycle boundary

Before every selected scenario, and unconditionally after diagnostics on every
outcome, the harness runs:

```text
forge destroy --purge-workspace --reboot --yes
```

Ordinary `forge destroy` is unchanged and preserves AgentPool workspace state.
The explicit purge runs only after the existing platform/K3s destroy path. It
revalidates the configured stable whole disk, root/system exclusion, holders and
active consumers, hardware identity, Forge receipt, filesystem UUID/label,
mount, and fstab identity. Missing, ambiguous, wrong, in-use, or drifted state
refuses. A clean second purge is idempotent only when the configured disk is
blank and every Forge mount/receipt/fstab surface is absent. Reboot is last.

The harness requires strict host-key verification, observes SSH disconnect,
requires reconnect with a changed `/proc/sys/kernel/random/boot_id`, and proves
that K3s, workspace signatures/mount/receipt, run-scoped overlay/transferred
state, and stale test processes cannot satisfy the next scenario. Failure is
incomplete/failing, never a skip.

## GPU model cache

The GPU fixture has a harness-owned block volume mounted at `/data/hf-cache`.
Its fixed by-id device and filesystem UUID must differ from the Forge AgentPool
workspace device. Forge does not configure, authorize, purge, or claim this
volume.

[`model-cache.json`](model-cache.json) pins the public
`Qwen/Qwen3.5-0.8B` model at revision
`2fc06364715b967f1860aea9cf38778875588b17` and pins the selected weight file to
SHA-256 `f0140d845aced424f17b1c75ebc5a67ef75fe309c68d2f613acda2eb551db7dd`.
Every GPU use verifies device, mount, UUID, revision path, and content hash
before product execution. The ModelBackend receives that exact revision. Cache
bytes can accelerate model loading but cannot satisfy product artifact/runtime
identity or AgentPool workspace assertions.

## Compiled scenarios

### `permanent-cpu`

Uses the CPU fixture for no-GPU refusal, supported migration-source install,
exact current Forge/chart/image/Flux handoff, dedicated local-path assertions,
two-worker RWO behavior, persistence/replacement/reapply, secret sync, and Flux
reconciliation.

### `permanent-workspace`

Resets the same CPU fixture, then proves process-open raw-device refusal, exact
workspace identity, concurrent isolated work, capacity gating, human-gate
worker replacement, persisted bytes, and idempotent reapply.

### `permanent-gpu`

Uses the GPU fixture for exact baseline/candidate driver transition, real GPU
smoke, disposable `emptyDir` versus durable cache behavior, exact platform
handoff, pinned model-cache validation, and one non-authoritative real-serving
request. `make test-e2e-gpu-broken-policy` runs the same fixture under the same
global lock and passes only when the intentional `deleteEmptyDir=false`
mutation is detected as red.

Forge registers no chart-install-only Kind scenario. Product and chart behavior
remains in control-plane/charts owner suites; Forge's deployed checks are
bounded dependent smokes after Forge-owned substrate and handoff assertions.

## Exact artifact and result contract

The one compiled planner selects PR, complete nightly/manual, and explicit
candidate intent. `.github/scripts/e2e.py` builds affected temporary or immutable
candidate artifacts once from the reviewed production recipes, composes one
verified runtime bundle, and supplies the same scenario ID, Make target,
timeout, and stage DAG in both modes.

The Forge stages transfer only composer-authorized bytes and separately verify
requested references, archive config/source labels, imported K3s CRI config
identity, and remote tag-to-manifest identity. A source-only build/load path,
published substitution for selected targets, stale host byte, or missing/extra
artifact is a failure.

Each result binds plan, catalogue, source, stage graph, runtime bundle, exact
artifact identities, terminal stage statuses, fixture capacity, pinned host-key
hash, workspace device, and pre/post-cleanup boot IDs. GPU results additionally
bind model-cache device/mount/UUID/model revision/hash. The aggregate requires
exactly one result per planned scenario.

## Qualification and legacy removal

Ephemeral DigitalOcean provisioning and tagged reaping were retained only on the
HOR-540 branch while the permanent path was qualified. Removal was authorized
only after the dated lifecycle acceptance record contained three consecutive
CPU and three consecutive GPU green destroy/apply/test/destroy cycles; any
failed or incomplete cycle reset that fixture's streak. The final repository has
no provider SDK, dynamic capacity discovery, `FORGE_E2E_KEEP`, tagged reaper, or
`DIGITALOCEAN_TOKEN` workflow path.

## Operational authority

Setup, SSH pinning, model-cache preparation, key rotation, quarantine, manual
provider recovery, and rollback are in
[`../../../docs/runbooks/permanent-e2e-fixtures.md`](../../../docs/runbooks/permanent-e2e-fixtures.md).
Qualification run IDs and bound identities are retained in the dated HOR-540
lifecycle acceptance record.

The production impact is the explicit Forge purge/reboot CLI and permanent
fixture operations. Semantic publication is not required for HOR-540
acceptance; its all-target candidate is validation-only and must not be
promoted.
