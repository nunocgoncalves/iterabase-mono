# forge

> Canonical source: [`iterabase-mono/forge`](https://github.com/nunocgoncalves/iterabase-mono/tree/master/forge). The former standalone repository is historical and read-only.

`forge` is the installer for the Horizonshift / Iterabase platform. It bootstraps a production-ready single-node [k3s](https://k3s.io) cluster on a VM or host over SSH, with dual-stack networking and prod-ready defaults.

> Per-customer, fully isolated, self-hosted. forge takes VMs/hosts (SSH) or a kubeconfig; it does **not** provision bare metal, Proxmox, or network appliances.

## Status

Walking skeleton (HOR-238). Implements single-node k3s bootstrap + the `forge` CLI. The platform Helm umbrella chart (HOR-239), GPU node readiness via the NVIDIA GPU Operator (HOR-240), Flux, and the overlay repo are follow-on tickets; the vLLM/SGLang backend is CRD-driven by the control-plane (HOR-306).

## Install

Pre-built binaries are published from the monorepo's protected release workflow on the [GitHub Releases](https://github.com/nunocgoncalves/iterabase-mono/releases) page (linux/darwin × amd64/arm64), with checksums, an SBOM, provenance, compatibility, and validation evidence. Raw tags do not publish; see [`../docs/release.md`](../docs/release.md).

> Homebrew tap: deferred — `goreleaser` deprecated its `brews` section; the tap will return once the replacement stabilizes.

Build from source:

```sh
make build      # -> bin/forge
```

## Quickstart

```sh
forge init               # generate forge.yaml (interactive)
forge apply --dry-run    # preflight the target host and print the plan (read-only)
forge apply              # provision / reconcile the cluster
forge kubeconfig         # fetch (or refresh) the kubeconfig
forge status             # cluster health + drift
forge upgrade --to v1.32.0   # upgrade k3s
forge destroy            # customer-safe: uninstall k3s; preserve workspace bytes
forge destroy --purge-workspace --reboot --yes # explicit destructive fixture/decommission lifecycle
```

`forge` SSHes to the host as a sudoer user (key auth) and installs k3s with flags derived from `forge.yaml`. `spec.hosts[].sshHostKey` optionally pins one OpenSSH host public key; permanent automation must set it and fails on replacement. The kubeconfig is fetched, rewritten to the host address, and stored at `~/.forge/<install>/kubeconfig.yaml`.

`apply` is **idempotent**: it reconciles from the live system — installs if absent, skips if in sync, refuses immutable changes (`cluster-cidr`/`service-cidr`/`dualStack` → `destroy` + `apply`), and routes version changes to `upgrade`.

When GPU support is enabled, preflight reports the NVIDIA PCI device and the complete driver-build surface: `/lib/modules/$(uname -r)/build/Makefile`, `dkms`, `gcc`, and `make`. Before GPU Operator reconciliation, `apply` idempotently installs the matching `linux-headers-$(uname -r)`, `build-essential`, and `dkms` packages, verifies the exact surface, and refuses a stale running kernel whose matching headers are no longer available from the configured Ubuntu archives because the driver container must independently resolve that package. Apply then accepts readiness only from one coherent operator/node observation: the ClusterPolicy must expose `Ready=True` and `Error=False`, its configured driver must match any requested pin, and the single node must be Ready, schedulable, outside an active upgrade, and report that exact loaded driver. The GPU Operator's legacy `status.state=notReady` remains visible evidence but is not the sole authority because v26.3.3 writes it separately from conditions and can lose that status update to a resource-version conflict. Forge accepts that one documented contradiction only when every stronger current signal agrees; other missing or contradictory evidence keeps apply blocked, and `upgrade-failed` terminates it immediately. GPU Operator v26.3.3 embeds the NFD 0.18.3 subchart, but Forge pins its compatible runtime image to NFD v0.19.0 and sets the supported master `resyncPeriod` to 30 seconds. NFD v0.19.0 makes that interval drive a periodic full reconcile, so a missed fresh-NodeFeature event cannot defer the required NVIDIA node label until the operator readiness timeout. The embedded chart retains identical NodeFeature CRDs, supplies the default worker pod identity needed by v0.19.0, and leaves the topology updater (whose v0.19.0 image-only upgrade needs additional RBAC) disabled.

Before each Helm apply, forge reads the CRDs bundled in the exact pinned chart artifact, server-side applies them, and waits for them to become `Established`. This permits an existing release to enable an operator-backed dependency later (for example, enabling observability adds the Prometheus Operator CRDs) despite Helm's limitation that `crds/` are installed only during a release's initial install. Charts without bundled CRDs are unchanged. CRDs are intentionally retained on rollback/uninstall to protect custom resources and their data.

Every single-node config persists exactly one `spec.agentPoolWorkspace.device`
stable `/dev/disk/by-id/...` whole-disk selection. `forge init` obtains it from
the interactive list, `--agentpool-workspace-device`, or
`FORGE_AGENTPOOL_WORKSPACE_DEVICE`; hand-authored config uses the same field.
`spec.agentPoolWorkspace.filesystem`, `--agentpool-workspace-filesystem`, and
`FORGE_AGENTPOOL_WORKSPACE_FILESYSTEM` support `auto|ext4|xfs`. Auto resolves
only reliably detected NVMe to XFS and uses ext4 for SATA, unknown, and virtual
transports. Interactive init shows transport plus recommended/resolved type and supports an override only when no explicit flag/environment filesystem source was supplied; a supplied source is preserved rather than silently reprompted.
The disk selection is the sole authorization for Forge's first format;
filesystem choice is not a second destructive confirmation.

Before any K3s/chart mutation, apply rejects root/system, removable, volatile,
partitioned, mounted, holder-backed, process-held raw/in-use,
recognized-signature, missing, ambiguous, or identity-drifted devices. It repeats bounded topology/signature/active-open probes
immediately before format; it does not scan the full device or accept a second
confirmation, wipe/adopt switch, or root fallback. Forge installs/checks the
required XFS tooling. A fsynced root-owned receipt makes ext4/XFS
format/fstab/mount/marker reconciliation crash-resumable with exact transport,
configured/resolved type, UUID, `iterabase-ws` label, and
`/var/lib/iterabase/agentpool-workspaces` mount identity.

After K3s is Ready, Forge keeps default `local-path` on K3s's normal platform
path and configures fixed non-default `iterabase-agentpool-local-path` through
the bundled `rancher.io/local-path` provisioner on only the dedicated mount.

Ordinary `forge destroy` remains customer-safe: it uninstalls platform/K3s and
preserves the workspace filesystem, receipt, mount identity, and bytes. Only
`--purge-workspace` opts into destructive removal. Purge repeats the stable
by-id, whole-disk, root/system, holder, active-consumer, hardware, receipt,
filesystem UUID/label, mount, and fstab checks before unmounting and erasing the
configured Forge filesystem signatures. Missing, ambiguous, wrong, in-use, or
drifted identity refuses. `--reboot` is independent and runs only after successful
destroy and any requested purge. Non-interactive destructive operation uses the
exact explicit command `forge destroy --purge-workspace --reboot --yes`; no CI
environment or prior state implies either flag. See
[`../docs/architecture/v2-local-path-storage.md`](../docs/architecture/v2-local-path-storage.md).

See `forge.example.yaml` for the full substrate config schema.

## Development

```sh
make test           # unit + fake-SSH integration tests
make test-e2e       # composed bundle on the configured permanent CPU fixture
make test-e2e-workspace # permanent-fixture dedicated-disk/local-path RWO gate
make test-e2e-unit  # compile + unit-test the separate E2E harness module
make lint           # golangci-lint
make fmt-check      # gofmt check
make install-hooks  # wire .githooks/ via core.hooksPath
```

Architecture invariants and v1 boundaries are documented in `AGENTS.md`.

## Layout

- `cmd/forge/` — entrypoint
- `internal/cli/` — Cobra command tree
- `internal/config/` — `forge.yaml` schema + loader
- `internal/provisioner/` — provisioner interface (the testability seam)
- `internal/sshprovisioner/` — SSH implementation
- `internal/k3s/` — k3s install-arg builder
- `internal/kubeconfig/` — kubeconfig fetch + server-URL rewrite
- `internal/lifecycle/` — phase orchestration + reconcile
- `internal/artifacts/` — local state dir (`~/.forge/<install>/`)
- `internal/version/` — build version
- `test/e2e/` — permanent CPU/GPU fixture runner (separate module; see `DESIGN.md`)

## License

Proprietary — Horizonshift. All rights reserved.
