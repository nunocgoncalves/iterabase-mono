# Platform V2 dedicated local-path RWO storage

Status: current repository implementation contract for HOR-538, implementing approved `DES-HOR-469-03`, `DES-HOR-538-01`, and `DES-HOR-538-02` recorded in Obsidian `Platform V2 — Single-Node K3s Local-Path RWO Storage`.

## Supported topology

Platform V2 supports exactly one schedulable K3s `v1.34.10+k3s1` server node and one customer-selected stable non-removable whole disk for AgentPool workspaces. Multi-node, Longhorn, RWX, BYO/alternate classes, managed storage selection, HA, existing-filesystem adoption, disk replacement/migration, and root-disk fallback are unsupported.

Every AgentPool owns one `ReadWriteOnce` claim on fixed non-default `iterabase-agentpool-local-path`. RWO limits the claim to one node, not one pod: multiple workers in the pool may mount it concurrently on the single node. Per-session UID/GID allocation, root-owned `0711` parent traversal, `0700` sibling directories, path containment, cleanup, and worker/turn/effect fencing remain mandatory.

## Forge device authorization and refusal boundary

`spec.agentPoolWorkspace.device` in `forge.yaml` is required and must be one `/dev/disk/by-id/...` whole-disk identity. `forge init` obtains the same value through interactive selection, `--agentpool-workspace-device`, or `FORGE_AGENTPOOL_WORKSPACE_DEVICE`; conflicting explicit sources fail. Hand-authored config uses the same field. Apply never discovers or substitutes a device.

The persisted stable-device selection is the sole authorization for Forge's first ext4 format. Interactive selection shows stable path, model, serial, size, fixed purpose, and the format consequence before selection. There is no post-selection confirmation, force/wipe/adopt switch, or second destructive input.

Before any K3s/chart mutation, and again immediately before first format, Forge uses bounded required probes to reject:

- missing, volatile, partition, removable, mapper, loop, RAID, or non-whole devices;
- a partition table, child device, holder, mount, swap use, or other active consumer;
- any device backing root, boot/EFI, K3s, kubelet, or system data;
- a recognized filesystem, partition, RAID, LVM, or crypt signature;
- probe read errors, ambiguity, or stable-identity/model/serial/WWN/size drift.

`wipefs -n` and `blkid -p` inspect known signature locations. Forge does not perform a block-wide read or require arbitrary bytes to be zero. Arbitrary non-signature bytes are accepted after every required predicate passes. Forge does not securely erase media.

## Crash-resumable filesystem transaction

Before format, Forge fsyncs a root-owned `0600` receipt at `/var/lib/iterabase/agentpool-workspace.receipt`. It binds contract version, install name, selected by-id value, model/serial/WWN, exact size, planned ext4 UUID/label, fixed mount, and transaction status.

Forge formats the whole device directly as ext4 with the planned UUID and label `iterabase-agentpool-workspaces`. A retry resumes only from a still-blank candidate matching the receipt or the exact receipt-created ext4 identity. Any other signature or identity mismatch fails closed.

Reconciliation owns:

- root-owned `/var/lib/iterabase/agentpool-workspaces` at mode `0711`;
- exactly one UUID-based `/etc/fstab` entry: `ext4 nodev,nosuid 0 2` (never `nofail`);
- active source, type, options, ownership, duplicate-UUID, and unexpected-consumer checks;
- a root-owned `0600` `.iterabase-workspace-identity` marker;
- fsynced receipt transitions through planned, formatted, fstab, mounted, and complete.

A complete transaction refuses marker, device, size, UUID, label, type, conflicting fstab/mount, or consumer drift. Safe same-device repair is limited to recreating the mount directory, restoring a missing exact fstab line, and remounting the exact UUID. `forge destroy` never wipes or removes the filesystem identity.

## Bundled local-path isolation

After K3s readiness, Forge reconciles `kube-system/local-path-config` with exact per-class maps:

- default `local-path` -> `/var/lib/rancher/k3s/storage`;
- `iterabase-agentpool-local-path` -> `/var/lib/iterabase/agentpool-workspaces`.

The AgentPool class uses `rancher.io/local-path`, `WaitForFirstConsumer`, `Delete`, `allowVolumeExpansion: false`, and an explicit non-default annotation. The control-plane accepts initial unbound `WaitForFirstConsumer` state so workers can trigger binding, then requires the bound PV to be RWO Filesystem `hostPath`, `Delete`, node-affine, and strictly beneath the dedicated mount. PostgreSQL, MinIO, and unrelated default claims remain on the normal K3s path.

## Capacity and failure semantics

Each harness performs a real write/fsync/rename/unlink transaction and `statfs` measurement on the mounted workspace filesystem. Metrics expose available bytes, capacity bytes, free ratio, warning state, gate state, and health-check results.

- warn below 25% free;
- withhold/revoke unspent fresh dispatch credit at or below 20%;
- once gated, reopen only at or above 25%;
- do not abort an active turn solely because capacity crossed the threshold;
- after the active turn's normal terminal/ACK boundary, withhold its next credit;
- treat zero blocks and real I/O/fsync/mount/ownership failure as worker loss, using existing fencing and no automatic replay.

Requested PVC size is planning metadata, not a quota. There is no online expansion. Customers own capacity response, infrastructure/hardware encryption, node/disk protection, and infrastructure backup. Node/disk loss may lose non-authoritative session bytes; PostgreSQL, invocation-ledger, artifact, attempt, and checkpoint records remain recovery authority.

## Validation

Required validation includes Forge config/CLI/fake-SSH command-shape and refusal tests; receipt/mount/class reapply tests; AgentPool fixed-class/RWO/PV-path and multi-replica tests; harness threshold/credit/fencing tests; alert/dashboard/runbook checks; release-target/catalogue checks; exact-candidate real-machine install/reapply/worker-replacement behavior; and proof that no Longhorn namespace, CRD, release, image, companion target, iSCSI/NFS bootstrap, RWX/BYO value, or root fallback remains.
