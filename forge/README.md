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
forge destroy            # uninstall k3s + remove local artifacts
```

`forge` SSHes to the host as a sudoer user (key auth) and installs k3s with flags derived from `forge.yaml`. The kubeconfig is fetched, rewritten to the host address, and stored at `~/.forge/<install>/kubeconfig.yaml`.

`apply` is **idempotent**: it reconciles from the live system — installs if absent, skips if in sync, refuses immutable changes (`cluster-cidr`/`service-cidr`/`dualStack` → `destroy` + `apply`), and routes version changes to `upgrade`.

When GPU support is enabled, apply accepts readiness only from one coherent operator/node observation: the ClusterPolicy must expose `Ready=True` and `Error=False`, its configured driver must match any requested pin, and the single node must be Ready, schedulable, outside an active upgrade, and report that exact loaded driver. The GPU Operator's legacy `status.state=notReady` remains visible evidence but is not the sole authority because v26.3.3 writes it separately from conditions and can lose that status update to a resource-version conflict. Forge accepts that one documented contradiction only when every stronger current signal agrees; other missing or contradictory evidence keeps apply blocked, and `upgrade-failed` terminates it immediately.

Before each Helm apply, forge reads the CRDs bundled in the exact pinned chart artifact, server-side applies them, and waits for them to become `Established`. This permits an existing release to enable an operator-backed dependency later (for example, enabling observability adds the Prometheus Operator CRDs) despite Helm's limitation that `crds/` are installed only during a release's initial install. Charts without bundled CRDs are unchanged. CRDs are intentionally retained on rollback/uninstall to protect custom resources and their data.

Every single-node config persists exactly one `spec.agentPoolWorkspace.device`
stable `/dev/disk/by-id/...` whole-disk selection. `forge init` obtains it from
the interactive list, `--agentpool-workspace-device`, or
`FORGE_AGENTPOOL_WORKSPACE_DEVICE`; hand-authored config uses the same field.
The selection is the sole authorization for Forge's first ext4 format.

Before any K3s/chart mutation, apply rejects root/system, removable, volatile,
partitioned, mounted, held, in-use, recognized-signature, missing, ambiguous, or
identity-drifted devices. It repeats bounded topology/signature probes
immediately before format; it does not scan the full device or accept a second
confirmation, wipe/adopt switch, or root fallback. A fsynced root-owned receipt
makes format/fstab/mount/marker reconciliation crash-resumable with exact UUID,
`iterabase-agentpool-workspaces` label, and
`/var/lib/iterabase/agentpool-workspaces` mount identity.

After K3s is Ready, Forge keeps default `local-path` on K3s's normal platform
path and configures fixed non-default `iterabase-agentpool-local-path` through
the bundled `rancher.io/local-path` provisioner on only the dedicated mount.
Destroy uninstalls K3s/platform resources but never wipes the workspace disk or
filesystem. See [`../docs/architecture/v2-local-path-storage.md`](../docs/architecture/v2-local-path-storage.md).

See `forge.example.yaml` for the full substrate config schema.

## Development

```sh
make test           # unit + fake-SSH integration tests
make test-e2e       # composed DigitalOcean single-node CPU e2e (needs DIGITALOCEAN_TOKEN)
make test-e2e-workspace # exact-candidate dedicated-disk/local-path RWO gate
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
- `test/e2e/` — composable DigitalOcean/Kind E2E runner (separate module; see `DESIGN.md`)

## License

Proprietary — Horizonshift. All rights reserved.
