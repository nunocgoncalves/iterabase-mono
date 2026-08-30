# V2 production RWX and BYO storage contract

> **Superseded historical record (2026-08-30).** `DES-HOR-469-03`,
> `DES-HOR-538-01`, and `DES-HOR-538-02` withdrew this Longhorn/RWX/BYO release
> direction before semantic promotion or deployment. It remains only as
> reconstructable decision and implementation history. Current authority is
> [`v2-local-path-storage.md`](v2-local-path-storage.md); no artifact, chart,
> configuration, runbook, or release path below is supported.

- **Status:** Approved architecture; implementation is owned by HOR-469.
- **Approval date:** 2026-08-25
- **Architecture ticket:** [HOR-424](https://linear.app/horizonshift/issue/HOR-424/v2-decide-and-validate-the-production-rwxbyo-storage-contract)
- **Product contract:** Obsidian `Platform V2 — Managed Digital Workforce — Product Requirements`, especially `REQ-018`, `REQ-035`, `SCN-012`, `SCN-018`, and release criteria 8 and 10
- **Related authority:** [`v2-parallel-cancellation-safe-restart.md`](v2-parallel-cancellation-safe-restart.md), [`v2-chat-tool-confirmation.md`](v2-chat-tool-confirmation.md), and the HOR-245 AgentPool/sandbox contract
- **Implementation owner:** HOR-469

This record is the repository authority for the production ReadWriteMany (RWX)
storage backend used by multi-worker AgentPools, the supported single-node and
three-node profiles, the bounded customer-supplied StorageClass path, and their
capacity, lifecycle, security, failure, recovery, diagnostics, validation, and
ownership boundaries. It records a decision and measured evidence. It does not
install Longhorn, change a chart, bootstrap a host, mutate an AgentPool, or
publish an artifact.

## 1. Approved design decisions

Nuno Gonçalves approved `DES-HOR-424-01` through `DES-HOR-424-06` exactly as one
package on 2026-08-25 before repository implementation. The complete canonical
approval statement and consequences are durable in HOR-424. A different
backend, Longhorn/data-engine version, topology, lifecycle, failure model,
backup authority, or component ownership requires another explicit architecture
decision rather than an implementation-local substitution.

### DES-HOR-424-01 — Managed backend, version, and topology

- **Approved by:** Nuno Gonçalves
- **Approved on:** 2026-08-25
- **Scope:** Iterabase-managed production RWX backend and supported single-/multi-node topology.
- **Decision:** Pin Apache-2.0 Longhorn Helm/application `1.12.1`, V1 data engine only, on the reference K3s `v1.34.10+k3s1` baseline. Managed single-node uses one replica and is explicitly non-HA: node, disk, and per-volume share-manager loss are outage/single-point-of-failure boundaries. Managed multi-node requires at least three storage nodes, three replicas, and dedicated SSD capacity; the per-volume share-manager still prevents any seamless-RWX-failover claim. V2 data engine and experimental RWX fast failover remain disabled.
- **Consequences:** HOR-469 packages only this backend/version/profile contract, verifies Linux iSCSI/NFSv4/mount-propagation prerequisites, and exposes the single-node and share-manager limitations. Another backend, data engine, version, or topology reopens architecture.
- **Evidence:** Founder approval is durable in HOR-424; sections 3 and 13 preserve upstream and measured evidence.

### DES-HOR-424-02 — Declarative chart, Forge, and overlay ownership

- **Approved by:** Nuno Gonçalves
- **Approved on:** 2026-08-25
- **Scope:** Installation ownership and stable configuration boundary.
- **Decision:** Chart values choose exactly `managed-longhorn` or `external` and one exact StorageClass name. Charts own the declarative Longhorn dependency, managed StorageClass, validation resources, and lifecycle assertions. Forge owns Linux host capability preflight/bootstrap required by the reference substrate, not a customer storage toggle. Generic/reference overlays select only the approved mode/class and size AgentPool claims; no per-customer imperative backend installation or arbitrary backend tuning enters `forge.yaml`.
- **Consequences:** HOR-469 must preserve chart/substrate/overlay ownership, idempotently re-apply without recreating claims, and reject contradictory values.
- **Evidence:** Founder approval is durable in HOR-424; section 6 defines the values handoff.

### DES-HOR-424-03 — Managed StorageClass and bounded BYO conformance

- **Approved by:** Nuno Gonçalves
- **Approved on:** 2026-08-25
- **Scope:** StorageClass behavior, BYO acceptance, and isolation semantics.
- **Decision:** The managed path exposes one non-default `iterabase-rwx` generic/non-migratable RWX Filesystem StorageClass with expansion, `Retain`, and Longhorn's version-owned default NFS failure options. An external class is accepted only after a disposable claim proves dynamic RWX Filesystem mounts from at least two workers, enforced requested capacity and expansion, root `chown`/`chmod` plus UID/GID and `0711` parent/`0700` sibling isolation without root squash, atomic rename, `fsync`, unlink, persistence across worker replacement, `Retain`, and actionable failure events.
- **Consequences:** A missing, ambiguous, root-squashed, non-expanding, non-capacity-enforcing, or isolation-incompatible class fails conformance; affected AgentPools remain unready. An `RWX` advertisement or class name is not evidence.
- **Evidence:** Founder approval is durable in HOR-424; sections 7, 8, and 13 define and demonstrate conformance.

### DES-HOR-424-04 — Capacity, reclaim, expansion, and performance boundary

- **Approved by:** Nuno Gonçalves
- **Approved on:** 2026-08-25
- **Scope:** Claim sizing, physical capacity, data lifecycle, and workload baseline.
- **Decision:** Each AgentPool claim is a hard per-pool capacity boundary and is expandable but never shrinkable. Managed single-node reserves at least 25% free space when using the root disk; managed multi-node uses dedicated SSDs with physical capacity for three replicas plus rebuild/snapshot headroom. `Retain` protects data from declarative claim deletion; explicit decommission settles/reaps sessions and deliberately deletes or transfers retained volumes. The measured nested ARM64 baseline is functional evidence, not a production service-level objective.
- **Consequences:** Best-effort quotas, unchecked over-provisioning, automatic destructive reclaim, shrink, and benchmark-derived customer throughput promises are unsupported.
- **Evidence:** Founder approval is durable in HOR-424; sections 9 and 13 record sizing and results.

### DES-HOR-424-05 — Failure, recovery, and diagnostics

- **Approved by:** Nuno Gonçalves
- **Approved on:** 2026-08-25
- **Scope:** Worker, share-manager, node, and storage failure semantics.
- **Decision:** Worker replacement and supported node restart preserve committed session files, but active I/O is not promised to survive a share-manager or node outage. Longhorn may recreate the share-manager while an existing NFS client remains blocked or errors. Iterabase fails the affected worker/turn safely, stops AgentPool readiness, surfaces PVC/PV/StorageClass/backend/share-manager/node events and health, and recycles workers only after storage is healthy; it never reports transparent failover or silently replays work.
- **Consequences:** Single-node loss is a full storage outage. Three replicas protect backend data from one replica/node loss but do not remove the single active share-manager interruption. Recovery is health verification plus disposable-worker replacement, not business-execution retry.
- **Evidence:** Founder approval is durable in HOR-424; sections 10 and 13 preserve the forced share-manager and hard-restart observations.

### DES-HOR-424-06 — Security, backup, upgrade, uninstall, and implementation gate

- **Approved by:** Nuno Gonçalves
- **Approved on:** 2026-08-25
- **Scope:** Storage trust, disaster-recovery authority, supported maintenance, and HOR-469 handoff.
- **Decision:** Longhorn's required privileged/root/host access is an explicit cluster-storage trust boundary; its UI is not customer-exposed, internal network policy remains enabled, data-path/disk encryption is customer-infrastructure responsibility, and no storage credential reaches workers. AgentPool/pi session PVCs remain excluded from the authoritative PostgreSQL-plus-artifact backup/DR set; optional customer backups cannot become silent restore authority. Upgrade proceeds through supported adjacent Longhorn minors only after health/capacity/preflight and system-backup evidence, with no downgrade promise. Uninstall/decommission requires zero consumers, explicit retained-volume disposition, and Longhorn deletion confirmation. HOR-469 automates the exact conformance, two-worker isolation, replacement, expansion, failure/readiness, re-apply, upgrade, and uninstall gates before the backend enters a release.
- **Consequences:** This decision has no production mutation or semantic publication. Unsupported backup restore, downgrade, forced uninstall, UI exposure, or reduced-privilege claims fail closed.
- **Evidence:** Founder approval is durable in HOR-424; the product backup boundary is inherited from `v2-chat-tool-confirmation.md`.

### DES-HOR-469-01 — Managed Longhorn companion release packaging

- **Approved by:** Nuno Gonçalves
- **Approved on:** 2026-08-25
- **Scope:** Helm packaging and lifecycle ordering for managed Longhorn; bounded amendment to `DES-HOR-424-02` only.
- **Decision:** Pinned Longhorn `1.12.1` is packaged as the same-version `rwx-storage-substrate` companion Helm release and installed before the platform only for `storage.rwx.mode: managed-longhorn`. The enum-only mode, exact class, and managed topology remain the complete semantic selection contract. `external` installs no backend.
- **Consequences:** The companion consumes the unmodified upstream chart, renders the managed class and conformance/uninstall gates, and shares the platform chart version. Forge derives selection from overlay chart values and adds no provider field to `forge.yaml`. Direct operators use the same ordering. This amendment changes no backend, data engine, topology, failure model, backup authority, or customer responsibility.
- **Evidence:** Canonical founder approval is durable in HOR-469.

### DES-HOR-469-02 — Internal TLS and managed-storage transport boundary

- **Approved by:** Nuno Gonçalves
- **Approved on:** 2026-08-26
- **Scope:** `global.internalTLS` integration for the pinned Longhorn managed path and the production qualification boundary for managed multi-node networking.
- **Decision:** When `global.internalTLS.enabled=true`, the existing Iterabase internal CA issues the `longhorn-grpc-tls` client/server leaf required by Longhorn `1.12.1`. The leaf Secret must exist before the Longhorn manager and instance managers start, or every component that started without it must be deterministically restarted. Release validation must prove valid manager-to-instance-manager gRPC mutual TLS and prove the current instance-manager services reject clients without the leaf and plaintext gRPC, so Longhorn's compatibility fallback cannot carry current traffic. The single-node profile accepts the RWX NFSv4.1 hop only as a bounded same-host data-plane exposure; the internal CA does not secure NFS, iSCSI, CSI, engine/replica, or share-manager data transport. Any future managed multi-node production topology requires encrypted inter-node networking and evidence that NFS and replica traffic traverse it. K3s Flannel `wireguard-native` is the leading design, but its selection and implementation belong to provisional HOR-519 rather than HOR-469.
- **Consequences:** HOR-469 provisions and validates the internal-CA-backed Longhorn gRPC leaf, makes the single-node exposure and multi-node prerequisite explicit, validates internal TLS and managed storage together, and adds compact Longhorn health panels to the existing operator dashboard. The existing three-node storage scenario remains implementation/reference evidence, not production qualification for unencrypted multi-node networking. HOR-469 does not implement full-platform HA, Forge multi-node lifecycle, service replicas, or inter-node encryption and makes no claim that the Iterabase CA directly secures NFS.
- **Evidence:** Canonical approval is durable in Obsidian `Areas/ho/Architecture/HOR-469 — Longhorn Internal TLS and Managed-Storage Transport Boundary.md`. It records the founder's exact 2026-08-26 response, selected scope, and consequences for PR #63, HOR-469, and provisional HOR-519.

## 2. Product and inherited runtime constraints

The storage choice serves the existing runtime; it does not redefine it:

1. Multi-worker AgentPools mount one pool-owned RWX PVC. Each session has a
   distinct, never-reused sandbox ID and UID/GID lifecycle.
2. The root supervisor establishes and verifies the mount root as root-owned
   `0711`, provisions canonical session directories at `0700` owned by the
   session UID/GID, and refuses symlink, foreign-owner, mode, root-squash, and
   failed-removal violations.
3. A session child may traverse to its known directory but cannot list the
   mount root or list/read/write a sibling directory.
4. Parallel branches use distinct sessions. Live filesystem state never crosses
   lanes; only PostgreSQL-committed outputs and immutable artifact references do.
5. Worker or storage loss never authorizes automatic turn or external-effect
   replay. The durable runtime and Tool Gateway consequence ledgers remain
   authoritative.
6. RWX session directories are durable working state for process and worker
   replacement, not a new product datastore, object/artifact store, database
   backup, or cross-region replication mechanism.
7. The authoritative disaster-recovery set remains PostgreSQL plus the
   artifact/object store and existing ledgers. A restored installation creates
   fresh session generations rather than trusting copied session directories.

The backend must implement ordinary Linux ownership and filesystem behavior.
A service that shares bytes but cannot preserve these UID/GID, mode, rename,
`fsync`, unlink, and capacity boundaries is not conforming storage.

## 3. Option, maintenance, license, and security review

Review date: 2026-08-25. Versions are the exact versions evaluated, not a policy
to float to latest.

| Candidate | Version / license | Operational evidence | Fit and decision |
| --- | --- | --- | --- |
| **Longhorn generic RWX** | `1.12.1`, Apache-2.0; released 2026-08-14 | Native generic RWX exposes a V1 Longhorn volume through one NFSv4.1 share-manager pod per claim; supports ARM64/AMD64, dynamic claims, expansion, replica health, snapshots/backups, and Helm installation. Production guidance recommends three nodes, 4 vCPU/4 GiB per node, SSDs, and 10 Gbps networking. | **Selected managed backend.** It is the smallest reviewed declarative stack spanning supported one-node and replicated three-node profiles, but its privileged host access and active share-manager are explicit risks. |
| **Rook-Ceph/CephFS** | Rook `v1.20.6`, Apache-2.0; released 2026-08-20 | Production guidance requires at least three nodes; host clusters consume raw devices/partitions, and CephFS adds monitors, OSDs, managers, metadata servers, CSI, pools, and a filesystem. CephFS provides real shared filesystem quotas and mature distributed storage. | **Rejected as managed default.** Stronger scale/HA characteristics do not justify the substantially larger device, daemon, upgrade, recovery, and operator burden for the required single-node profile. It may qualify only through the external StorageClass path when customer-operated. |
| **OpenEBS Replicated PV Mayastor + NFS CSI** | OpenEBS `v4.5.1`, Apache-2.0; released 2026-06-18 | Filesystem RWX uses a regular Mayastor RWO volume, a separately deployed privileged single NFS-server pod, Service, and the Kubernetes NFS CSI driver. Native RWX in 4.5 is experimental block mode for KubeVirt, not a general filesystem. | **Rejected as managed default.** It composes more moving parts around the same single-NFS-server failure boundary, while native filesystem RWX is absent. |
| **NFS subdir external provisioner** | `4.0.18`, Apache-2.0; released 2023-03-13 | Requires an existing NFS server. Upstream explicitly says provisioned capacity is not guaranteed and resize/expansion is unsupported. Server lifecycle, availability, export security, and backup remain external. | **Rejected as a managed backend and rejected by conformance in this form.** It may not pass the external path unless the actual customer CSI/server combination independently enforces capacity and expansion. |
| **Customer-supplied CSI/StorageClass** | Customer-pinned and customer-operated | May be CephFS, a managed-cloud filesystem, enterprise NAS CSI, or another implementation. Backend name and `RWX` declaration alone say nothing about root squash, capacity, expansion, failure, or reclaim behavior. | **Supported only through bounded conformance.** The customer owns backend installation, licensing, capacity, availability, security, backup, upgrade, and incident response; Iterabase owns the disposable conformance gate and AgentPool fail-closed integration. |

### Sources

- Longhorn [`v1.12.1` release](https://github.com/longhorn/longhorn/releases/tag/v1.12.1), [installation requirements](https://longhorn.io/docs/1.12.1/deploy/install/), [best practices](https://longhorn.io/docs/1.12.1/best-practices/), [RWX design/failure behavior](https://longhorn.io/docs/1.12.1/nodes-and-volumes/volumes/rwx-volumes/), [StorageClass parameters](https://longhorn.io/docs/1.12.1/references/storage-class-parameters/), [upgrade contract](https://longhorn.io/docs/1.12.1/deploy/upgrade/), and [uninstall guard](https://longhorn.io/docs/1.12.1/deploy/uninstall/).
- K3s [`v1.34.10+k3s1` release](https://github.com/k3s-io/k3s/releases/tag/v1.34.10%2Bk3s1).
- Rook [`v1.20.6` release](https://github.com/rook/rook/releases/tag/v1.20.6), [storage architecture](https://github.com/rook/rook/blob/v1.20.6/Documentation/Getting-Started/storage-architecture.md), [prerequisites](https://github.com/rook/rook/blob/v1.20.6/Documentation/Getting-Started/Prerequisites/prerequisites.md), and [CephFS](https://github.com/rook/rook/blob/v1.20.6/Documentation/Storage-Configuration/Shared-Filesystem-CephFS/filesystem-storage.md).
- OpenEBS [`v4.5.1` release](https://github.com/openebs/openebs/releases/tag/v4.5.1) and [filesystem RWX via Mayastor and NFS](https://openebs.io/docs/Solutioning/read-write-many/nfspvc).
- NFS subdir provisioner [`4.0.18` release](https://github.com/kubernetes-sigs/nfs-subdir-external-provisioner/releases/tag/nfs-subdir-external-provisioner-4.0.18) and [documented limitations](https://github.com/kubernetes-sigs/nfs-subdir-external-provisioner/tree/nfs-subdir-external-provisioner-4.0.18#nfs-provisioner-limitationspitfalls).

All four reviewed open-source implementations use Apache-2.0. HOR-469 must
still pin exact chart/image identities, retain notices, consume available SBOM
and vulnerability evidence, and fail release validation on prohibited or
unreviewed transitive artifacts. License compatibility does not waive the
runtime security review.

## 4. Selected managed architecture

A generic RWX Longhorn claim has two layers:

```text
worker supervisor/child pods on one or more nodes
          | NFSv4.1 mounts
          v
one share-manager pod + Service for this PVC
          | one attached Longhorn V1 filesystem volume
          v
1 local replica (single-node) OR 3 synchronous replicas (three-node)
```

The share-manager is the active filesystem server. Three replicas protect the
underlying Longhorn block volume; they do not create three active NFS servers.
DNS, the share-manager, the attached engine, node mounts, and client recovery
remain in the I/O path.

`global.internalTLS` has a deliberately bounded Longhorn meaning:

| Longhorn path | Transport boundary |
| --- | --- |
| manager ↔ current V1 instance-manager gRPC services | Mutual TLS with the `longhorn-grpc-tls` client/server leaf issued by the Iterabase internal CA; unauthenticated TLS and plaintext are rejected |
| Longhorn ↔ Kubernetes API | Kubernetes API server TLS and service-account authorization |
| manager API/UI and `/metrics` on port 9500 | Upstream HTTP inside ClusterIP/restricted NetworkPolicies; UI has no customer ingress and Prometheus receives only the metrics exception |
| single-node worker ↔ share-manager NFSv4.1 | Unencrypted same-host data plane, accepted only as the bounded single-node exposure |
| host iSCSI/CSI sockets and V1 engine/replica/share-manager data paths | Not directly secured by the Iterabase CA; future production inter-node traffic requires the HOR-519 encrypted-network gate |

The managed installation pins:

- Longhorn chart and application `1.12.1`;
- K3s `v1.34.10+k3s1` as the exact reference validation baseline;
- V1 data engine; V2/SPDK is disabled;
- generic non-migratable RWX Filesystem volumes;
- NFSv4 client support on every worker node;
- internal Longhorn NetworkPolicies enabled with `type: k3s` and internal
  traffic restriction enabled;
- an Iterabase-internal-CA `longhorn-grpc-tls` leaf whenever
  `global.internalTLS.enabled=true`, with current instance-manager gRPC services
  rejecting plaintext and unauthenticated clients;
- no customer ingress for the Longhorn UI;
- default Longhorn StorageClass disabled; only the Iterabase class is consumed;
- RWX fast failover disabled because upstream marks it experimental;
- volume creation with degraded availability disabled;
- replica node soft anti-affinity disabled;
- storage over-provisioning capped at 100%; and
- the Longhorn deletion-confirmation flag left false during normal operation.

The StorageClass does not override `nfsOptions`. Longhorn `1.12.1` therefore
owns its complete tested NFS option set (`softerr`, `timeo=600`, `retrans=5` at
the reviewed version). This avoids a partial option string that silently drops
vendor defaults. These options may return timeout/I/O errors after an outage;
they are not a data-integrity or seamless-failover promise. A worker that sees
an I/O error or loses its storage health fails closed and is replaced after the
backend is healthy.

### Host prerequisites

Every managed storage/worker node must pass all of the following before the
Longhorn chart is enabled:

- Linux on AMD64 or ARM64 with Longhorn-supported kernel/filesystem behavior;
- K3s `v1.34.10+k3s1` for the reference release;
- `open-iscsi`/`iscsiadm` installed and `iscsid` enabled/running;
- an NFSv4 client and `mount.nfs` installed;
- bidirectional mount propagation enabled;
- `bash`, `curl`, `findmnt`, `grep`, `awk`, `blkid`, and `lsblk` available;
- an ext4 or XFS host filesystem for the Longhorn V1 data path;
- required kernel iSCSI support; and
- permission to run Longhorn's privileged/root components and host mounts.

Forge may install or verify these capabilities on the reference host because
they are substrate prerequisites. `forge.yaml` does not gain a storage provider
or customer-backend selector. A BYO cluster is customer-owned and must already
provide node prerequisites required by its CSI driver.

## 5. Supported topology profiles

### 5.1 `single-node`

- Exactly one K3s control-plane/worker/storage node.
- Longhorn V1 volume replica count: `1`.
- The RWX NFSv4.1 client-to-share-manager hop stays on this one host. It is a
  bounded unencrypted same-host data-plane exposure: the internal CA protects
  manager-to-instance-manager gRPC, not NFS, iSCSI, CSI, or volume data traffic.
- The root disk may be used only with at least 25% minimal free-space reserve,
  over-provisioning at 100% or less, and explicit capacity monitoring. A
  dedicated SSD mounted at the fixed Longhorn data path is preferred.
- One node, one disk/replica, one attached engine, and one share-manager mean no
  storage HA. Node maintenance, kernel failure, disk failure, and power loss
  make every RWX claim unavailable. Disk loss loses the session volume.
- Supported recovery is node/service restart followed by health verification
  and worker replacement. It is not failover.

This profile supports customer-controlled single-node installations honestly;
it must never be marketed as redundant or continuously available.

### 5.2 `three-node` reference profile

This chart and test profile preserves the approved three-replica storage
contract and its implementation evidence. It is not production-qualified until
HOR-519 selects and validates encrypted inter-node networking. That future gate
must prove both RWX NFS and Longhorn replica traffic traverse the encrypted
tunnel; K3s Flannel `wireguard-native` is the leading, not yet approved, design.

- At least three schedulable Linux storage/worker nodes.
- Longhorn V1 volume replica count: `3`, with replica soft anti-affinity false
  so all three replicas must land on distinct nodes.
- Each node provides a dedicated SSD-backed Longhorn data path. Physical
  planning includes three copies of every provisioned byte plus filesystem,
  snapshot, rebuild, and free-space headroom. The release preflight refuses
  claims that cannot be created fully healthy.
- Upstream recommends at least 4 vCPU and 4 GiB per node for the V1 engine and
  10 Gbps storage networking for production I/O. Iterabase does not turn those
  recommendations into a lower unsupported footprint.
- One replica/node may fail without losing all backend copies. Rebuild requires
  sufficient healthy-node capacity and is monitored.
- The per-claim share-manager remains singular. Its node loss interrupts all
  clients while Longhorn recreates it and NFS performs recovery; clients may
  block or return errors and may require worker replacement.

This is a replicated storage profile, not an unqualified HA or zero-downtime
profile. Two-node managed Longhorn is unsupported; use three nodes or a
conforming customer-operated external class.

## 6. Stable chart and overlay values contract

HOR-469 implements these exact semantic values in the platform chart. In
managed mode, the same-version `rwx-storage-substrate` companion receives the
same values files and completes before the platform release. In external mode,
that companion is absent and the platform installs no backend:

```yaml
storage:
  rwx:
    # Exactly managed-longhorn | external.
    mode: managed-longhorn
    # Managed mode requires this exact name. External mode names the
    # customer-owned conforming class.
    storageClassName: iterabase-rwx
    managedLonghorn:
      # Exactly single-node | three-node; illegal in external mode.
      topology: single-node
```

External example:

```yaml
storage:
  rwx:
    mode: external
    storageClassName: customer-production-rwx
```

Rules:

1. `mode` and `storageClassName` are required; there is no auto-detection,
   cluster-default fallback, imperative install mode, or per-customer backend
   switch.
2. `managedLonghorn.topology` is required only in managed mode. External mode
   rejects every managed setting.
3. Managed mode requires `storageClassName: iterabase-rwx`. The chart installs
   the pinned dependency and exact class; users cannot pass arbitrary Longhorn
   values through the supported contract.
4. External mode installs no backend and creates no alias StorageClass. The
   named class must already exist and pass the same-release conformance gate.
5. The platform chart and reference harness own preflight ordering: host
   prerequisites, backend/CSI ready, class ready/conformant, then AgentPools.
6. Generic/reference overlays repeat the exact class name in every AgentPool
   sandbox and choose a reviewed per-pool size:

```yaml
spec:
  sandbox:
    storageClassName: iterabase-rwx # or the exact external class value
    accessMode: ReadWriteMany
    size: 100Gi
```

7. An overlay mismatch, omitted class, `ReadWriteOnce`, or nonconformant class
   leaves the AgentPool unready. The operator does not rewrite the overlay or
   silently choose another class.
8. Re-apply may reconcile settings and health but must not replace a
   StorageClass incompatibly, mutate a bound claim's class/access mode, or
   recreate a PVC.

The code-level chart path may use normal Helm dependency plumbing, including
Longhorn's supported subchart namespace override. That plumbing may not change
the public semantic keys above or the ownership boundary.

## 7. Managed StorageClass contract

HOR-469 renders the semantic equivalent of:

```yaml
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: iterabase-rwx
  annotations:
    storageclass.kubernetes.io/is-default-class: "false"
provisioner: driver.longhorn.io
allowVolumeExpansion: true
reclaimPolicy: Retain
volumeBindingMode: Immediate
parameters:
  dataEngine: v1
  fsType: ext4
  fromBackup: ""
  migratable: "false"
  numberOfReplicas: "1" # single-node; exactly "3" in three-node
```

The class does not override `staleReplicaTimeout`; Longhorn `1.12.1`'s
version default remains in force. The chart does not modify Longhorn's default
`longhorn` StorageClass and does not make any class the cluster default.
StorageClass parameters affect new volumes only; a topology/profile change
therefore cannot be applied to existing
claims by editing the class. Migration requires a separately reviewed copy,
cutover, and rollback plan.

### Reclaim and deletion

`Retain` is intentional. Deleting or renaming declarative AgentPool input can
delete the PVC object; it must not silently destroy still-needed session bytes.
A retained PV/Longhorn volume is not a leak to ignore and is not automatically
adopted by another pool. Decommission must:

1. close starts for the pool and reach zero active assignments/sessions;
2. deliver/verify approved session reaping or preserve the exact recovery need;
3. identify the PVC, PV, Longhorn volume, pool, and data disposition;
4. delete/sanitize the retained volume or transfer it under an explicit plan;
5. verify backend capacity is reclaimed; and
6. retain an audit record without retaining customer session content.

### Expansion

Only growth is supported. Before expansion, operators prove healthy backend
state and physical replica/rebuild headroom. The PVC request changes
monotonically; controller expansion and node/filesystem expansion must both
finish before new capacity is reported. Failure leaves the old size
operational or the pool unready with events; it never reports requested size as
usable before filesystem evidence. Shrink requires a new volume/copy and is not
part of this contract.

## 8. External/BYO conformance contract

A customer may supply a StorageClass only when Iterabase-managed Longhorn is
disabled and the customer accepts backend operations. The class is conforming
only when all static and live checks pass on the exact cluster and class used by
the release.

### 8.1 Static checks

- The exact class exists, and every disposable claim and AgentPool explicitly
  sets its `storageClassName`; no class is selected by implicit defaulting. A
  customer-supplied class may independently be the cluster default.
- `reclaimPolicy` is `Retain`.
- `allowVolumeExpansion` is `true`.
- The provisioner/CSI driver and backend version, license/support owner, node
  prerequisites, supported Kubernetes versions, and upgrade path are recorded.
- A requested `ReadWriteMany` `Filesystem` claim is supported. Block-only RWX,
  RWO exported through an unowned ad hoc server, and read-only-many are not
  substitutes.
- The backend enforces a per-claim capacity boundary. A PVC request that merely
  labels an unbounded shared export is rejected.
- The customer documents physical capacity, redundancy/failure domain, reclaim,
  snapshots/backups, encryption, monitoring, maintenance, and incident owner.

### 8.2 Live disposable-claim checks

The exact release gate runs
[`historical/hor-424-rwx-conformance.sh.txt`](historical/hor-424-rwx-conformance.sh.txt)
against the named class. It does not install a backend. It proves:

1. dynamic provisioning and binding of one RWX Filesystem claim;
2. a filesystem capacity consistent with the request rather than the whole
   backing export;
3. root can establish and re-verify root ownership/mode after mount—root squash
   or ignored `chown`/`chmod` fails;
4. two non-root worker pods are simultaneously active on the same claim;
5. each worker can read/write/fsync/atomically rename/unlink inside its own
   UID/GID-owned `0700` directory;
6. neither worker can list the `0711` parent or read the known sibling path;
7. a verifier sees both workers' checksummed output;
8. a replacement worker sees committed data and retains sibling denial;
9. online expansion completes at PVC and mounted-filesystem levels without
   changing existing bytes; and
10. any assertion or timeout failure preserves the synthetic resources and
    collects bounded generic Kubernetes resources, events, and job logs.

Run it against the current Kubernetes context with an explicit class:

```bash
HOR424_STORAGE_CLASS=iterabase-rwx \
  docs/architecture/historical/hor-424-rwx-conformance.sh.txt
```

Set `HOR424_NAMESPACE` to isolate repeated evidence,
`HOR424_ATTEST_NAMESPACE` to the namespace containing AgentPools (default
`iterabase-system`), `HOR424_CLEANUP=true` to turn the successful disposable PV
to `Delete` and verify cleanup, or `KUBECTL="sudo k3s kubectl"` on a direct K3s
host. On success the gate writes a `HOR-469/v1` ConfigMap attestation bound to
the exact StorageClass UID and provisioner. Recreating or replacing the class
invalidates that evidence. The script refuses to overwrite an existing evidence
namespace and preserves all synthetic resources on failure while printing
bounded diagnostics.

This bounded collection is diagnostic evidence, not proof that a backend
failure automatically emits actionable events. A backend-specific release
scenario must additionally remove its active server or one storage node, prove
AgentPool readiness fails with actionable backend/Kubernetes events, preserve
committed data, and recover through backend health plus worker replacement. The
generic script cannot safely invent a backend-specific failure operation.

### 8.3 Root and identity requirements

Root-squashed NFS is nonconforming because the trusted supervisor must create,
`chown`, validate, and reap arbitrary session UID/GID directories. Pre-created
world-writable roots, fixed shared groups, ACLs that let a child enumerate
siblings, and drivers that rewrite ownership/mode on every RWX mount are also
nonconforming.

The backend's export root may initially arrive with a permissive mode. That is
not trusted. Every supervisor startup must establish and re-stat root-owned
`0711` before advertising readiness. Conformance repeats this after remount and
worker replacement. No pod-level `fsGroup` may recursively widen or rewrite the
shared session tree.

### 8.4 Customer/Iterabase responsibility split

| Concern | Managed Longhorn | External StorageClass |
| --- | --- | --- |
| Backend/chart version and configuration | Iterabase chart contract | Customer, recorded before conformance |
| Host/node prerequisites | Forge on the reference substrate; customer provides hardware | Customer |
| StorageClass and validation job | Iterabase | Customer class; Iterabase conformance |
| Physical disks/network/capacity | Customer infrastructure under Iterabase preflight; production multi-node networking is HOR-519-gated | Customer |
| Transport encryption | Iterabase internal CA protects Longhorn manager-to-instance-manager gRPC; single-node NFSv4.1 is a bounded same-host exposure; future production multi-node NFS/replica traffic requires an HOR-519-approved encrypted tunnel | Customer records backend and transport encryption before conformance |
| AgentPool PVC/session isolation | Iterabase operator/supervisor | Iterabase operator/supervisor |
| Backend monitoring/repair | Shared: Iterabase product diagnostics, customer infrastructure response | Customer, with Iterabase diagnostics at the claim boundary |
| Encryption at rest and key custody | Customer infrastructure | Customer |
| Backend backup/snapshot policy | Optional customer operation; not platform restore authority | Customer; not platform restore authority |
| Upgrade/uninstall | Iterabase procedure plus customer maintenance approval | Customer procedure plus Iterabase re-conformance |

## 9. Capacity and performance contract

### Capacity

- The AgentPool PVC request is the hard logical pool limit.
- Managed single-node provisions one physical replica. Managed three-node
  provisions three; a `100Gi` pool therefore needs at least `300Gi` raw replica
  allocation plus filesystem, snapshots, rebuild, and reserve headroom.
- Storage over-provisioning remains at 100% or less. A managed root disk retains
  at least 25% free; a dedicated disk retains at least the reviewed rebuild and
  minimum-free reserve (never less than Longhorn production guidance).
- Alerts precede exhaustion and cover node/disk schedulable capacity, volume
  actual/requested use, replica health/rebuild, PVC expansion, and retained PVs.
- Full storage causes explicit I/O/worker/readiness failure. It cannot spill to a
  sibling pool, local `emptyDir`, root filesystem, or artifact store.

### Performance

The workload is many small session/transcript/workspace mutations plus bounded
sequential files, not a database benchmark. Release evidence therefore records:

- two concurrent mounts and overlapping writes;
- sequential bytes, elapsed time, and per-worker MiB/s;
- small-file count/bytes, elapsed time, and files/s;
- `fsync`/rename behavior;
- node, disk, network, Kubernetes, backend, class, image, replica, and capacity
  identities; and
- p50/p95/p99 latency under the representative release workload when HOR-469
  runs on the real reference substrate.

HOR-424's nested-VM measurements establish functional feasibility only. No
customer SLO, concurrency ceiling, or minimum production hardware may be
inferred from them. HOR-469/shared-release testing must set an evidence-backed
capacity envelope or fail the release rather than conceal an inadequate result.

## 10. Failure, readiness, and recovery

| Failure | Required state/evidence | Recovery | Explicit limitation |
| --- | --- | --- | --- |
| Worker pod/process loss | Assignment/turn follows existing worker-loss fencing; AgentPool capacity decreases; committed files remain | Verify claim/backend healthy, create a fresh worker, mount and validate root/session ownership | Never redeliver the lost started turn automatically |
| Share-manager pod/process loss | Affected worker I/O may block/error; pool becomes storage-unready; backend/share-manager/PVC events identify the claim | Longhorn recreates a healthy share-manager; then terminate/recycle affected workers and validate committed hashes before new assignments | Service readiness does not prove existing NFS clients recovered; no transparent failover claim |
| Single-node restart/loss | Entire managed storage and pool unavailable | Restart node, `iscsid`, K3s, Longhorn, engine/share-managers; validate volumes and replace workers | No service during outage; disk loss can lose the one replica |
| One node/replica loss in three-node profile | Volume degraded but retained copies remain; stop unsafe new capacity if share-manager/client path is affected | Restore node or rebuild to a healthy third replica with capacity; verify share-manager and recycle affected workers | Replica redundancy is not uninterrupted NFS service |
| DNS/network partition | Mount/recovery and Longhorn recovery backend may fail; pool unready | Restore CoreDNS/storage network, backend health, then workers | Upstream recommends HA CoreDNS; single-node DNS is another SPOF |
| Full disk/volume | Writes fail; volume/pool unready; alerts/events preserve cause | Reap eligible sessions or expand monotonically after physical-headroom proof | No silent overcommit/spill, no shrink |
| StorageClass/PVC misconfiguration | PVC Pending/Lost or mount/root validation failure; no ready workers | Correct declarative value/class and re-run conformance | No fallback to default/local-path/RWO |
| Backend upgrade failure | Keep starts closed; preserve exact old manager/engine/volume evidence | Follow supported Longhorn recovery or restore pre-upgrade system state before reopening | No unsupported downgrade or mixed-version success claim |

AgentPool readiness must include more than ready pod count. It requires the exact
PVC bound to the declared class/access mode/size, successful mount-root
validation in every ready worker, managed backend volume `healthy` (or equivalent
customer health evidence), and no active conformance/expansion/failure. Losing
that proof removes scheduling credit and blocks new production parallel work.

A storage outage never changes PostgreSQL or Tool Gateway truth. Late worker
output remains late evidence and cannot advance a terminalized attempt. A
replacement worker receives only a newly fenced assignment.

## 11. Diagnostics contract

The normal customer UI exposes only a customer-safe unavailable/stopped result.
Technical diagnostics are operator-only and contain no session file names or
contents beyond bounded synthetic conformance resources.

For an affected pool/claim, collect at least:

```text
Kubernetes and K3s versions; node Ready/pressure/taints and relevant events
StorageClass YAML; PVC/PV phase, class, access mode, request/capacity, conditions
AgentPool conditions and declared class/mode/size
worker pod scheduling, mount, readiness, restart, and termination events
managed Longhorn version/settings, node/disk schedulability and capacity
Longhorn volume state/robustness/replica/engine/share-manager identities
share-manager Service/endpoints/pod readiness and recent bounded logs
CSI controller/node plugin readiness and mount/attach/expand events
conformance run identity/result and exact failed assertion
```

Required condition/reason families for HOR-469 are stable and actionable:

- `StorageClassMissing` / `StorageClassMismatch`;
- `StorageConformancePending` / `StorageConformanceFailed`;
- `PVCProvisioning` / `PVCExpansionFailed` / `PVCUnavailable`;
- `MountRootUnsafe` (owner, mode, symlink, root squash, ignored mutation);
- `BackendDegraded` / `ShareManagerUnavailable` / `CapacityInsufficient`;
- `StorageRecoveryPending`; and
- `StorageReady` only after every required predicate passes.

Messages identify resource names, observed versus required state, and the next
operator action. They do not include customer bytes, directory listings,
secrets, NFS credentials, or arbitrary backend logs.

The conformance script automatically prints the generic resources/events on
failure and deliberately preserves them by default. Backend-specific release
scenarios add bounded Longhorn diagnostics.

## 12. Lifecycle and operational runbook outline

### Install/enable

1. Record exact topology, K3s/Longhorn or external backend version, node/disk
   inventory, capacity, encryption owner, maintenance owner, and rollback.
2. Run host/CSI prerequisites before enabling the backend.
3. When internal TLS is enabled, wait for the Iterabase internal CA and issue
   `longhorn-grpc-tls` before the managed components start. If an existing
   component predates the leaf, deterministically restart it before validation.
4. For managed mode, install the same-version `rwx-storage-substrate` companion
   into `longhorn-system` before the platform release; for external mode, do not
   install that companion. Wait for controllers, node plugins, engines, and
   nodes/disks healthy, then prove mutual TLS succeeds while unauthenticated TLS
   and plaintext gRPC fail against every current instance-manager service.
5. Render and verify the exact explicitly named StorageClass; managed
   `iterabase-rwx` remains non-default.
6. Run the disposable conformance gate and store logs/resource identities.
7. Reconcile AgentPools only after conformance. Verify every worker establishes
   `0711` root and reports ready before scheduling work.

### Routine operations

- Alert on claim usage, physical schedulable space, node/disk/replica health,
  rebuild, share-manager/CSI readiness, retained volumes, mount errors, and
  conformance age.
- Re-run conformance after backend/CSI/Kubernetes/node-image/network/storage
  configuration changes and before a release.
- Treat an expired or changed-class conformance result as pending, not inherited
  success.

### Expand

1. Close or bound new assignments for the pool.
2. Verify backend/replicas healthy and enough physical/headroom capacity for the
   topology multiplier.
3. Increase the declarative PVC size; never edit PV/backend state imperatively.
4. Wait for controller and filesystem expansion, then verify mounted `df`, old
   hashes, root/session modes, and worker readiness.
5. Roll back only the consuming workload change; PVC shrink is unavailable.

### Share-manager/backend incident

1. Stop new scheduling credit and preserve runtime/gateway evidence.
2. Identify exact PVC/PV/volume/share-manager and whether active clients are
   blocked, errored, or merely stale.
3. Restore backend/share-manager health and replica sufficiency first.
4. Terminate affected worker pods; do not trust service readiness as client
   recovery and do not retry their turns.
5. Start fresh workers, establish mount root, validate known committed session
   state, and reopen only after storage readiness.

### Node maintenance/restart

- Single-node: announce full outage, close starts, settle/stop work, verify
  sessions/retained data, then restart. Bring up `iscsid`, K3s, Longhorn, volume,
  share-manager, and workers in that order.
- Three-node: drain only with healthy replicas and rebuild capacity. A
  share-manager move is still disruptive; recycle affected workers afterward.

### Upgrade

1. Only adjacent supported Longhorn minor upgrades or same-minor patch upgrades
   are eligible; never skip a minor and never promise downgrade.
2. Verify Kubernetes compatibility, upstream important/known issues, healthy
   volumes/replicas, no faulted resources, capacity, and exact image/chart
   identities.
3. Close starts, take a Longhorn system backup as upgrade metadata protection,
   and record pre-upgrade volume/engine/share-manager state. Session volumes are
   still not added to product DR authority.
4. Upgrade manager/chart, then engines through the supported path; active RWX
   share-manager image updates may wait until detach/recreation.
5. Re-run conformance, replacement, expansion, failure/readiness, and re-apply
   gates before reopening.

### Uninstall/decommission

1. Disable new pools/starts and reach zero consumers/active sessions.
2. Inventory all claims/PVs/Longhorn volumes and choose delete/sanitize or
   transfer for each retained volume.
3. Reap eligible session directories and verify deletion evidence.
4. Remove workloads/claims, deliberately dispose of retained volumes, and prove
   physical capacity/data handling complete.
5. Set Longhorn's deletion-confirmation flag only in this bounded maintenance
   window, run the supported uninstall, verify CRDs/webhooks/host mounts/data
   path disposition, then return the flag/process to the safe state.

Deleting the platform release, a values key, namespace, or AgentPool is never
implicit authorization to destroy or orphan storage.

## 13. Reference validation evidence

### Environment and identities

Validation ran on 2026-08-25 in an ephemeral ARM64 Fedora CoreOS virtual machine:

- Fedora CoreOS `41.20250105.3.0`, kernel `6.12.7-200.fc41.aarch64`;
- 4 vCPU, 6 GiB RAM, 40 GiB virtual disk;
- K3s `v1.34.10+k3s1`;
- Longhorn chart/application `1.12.1`, V1 engine, one replica;
- `open-iscsi`/`iscsid` and NFS client active;
- Longhorn internal NetworkPolicies enabled with internal traffic restriction and
  `type: k3s` for the final conformance replay;
- `iterabase-rwx`, `Retain`, expansion enabled, generic non-migratable RWX;
- one `2Gi` claim expanded online to `3Gi`; and
- Debian 13 slim multi-architecture validation image
  `sha256:d7e12182ce18b85b93007c1dedf31f2d29e01ccf3182cc4017c709b6259bc132`.

This was a functional single-node proof in nested local virtualization, not
multi-node performance, hardware qualification, availability certification, or
release evidence.

### Two-worker mount, I/O, and isolation

Two jobs overlapped from `2026-08-25T10:15:00Z` until `10:15:26Z`/`10:15:27Z`
on one RWX claim. Each ran under a distinct UID/GID and proved:

- own `0700` session read/write;
- parent listing denied under root-owned `0711`;
- explicit sibling secret read denied;
- concurrent `128MiB` sequential write with `fsync`;
- 1,000 separate 4 KiB files with final sync; and
- checksum persistence visible from a separate mount.

Measured output:

| Worker | Sequential | Small files |
| --- | --- | --- |
| UID/GID 10001 | 128 MiB in 7,087 ms = 18.1 MiB/s | 1,000 / 4,096,000 bytes in 12,670 ms = 78.9 files/s |
| UID/GID 10002 | 128 MiB in 6,261 ms = 20.4 MiB/s | 1,000 / 4,096,000 bytes in 11,494 ms = 87.0 files/s |

A replacement UID/GID 10001 worker verified its SHA-256, wrote a new marker, and
remained unable to read UID/GID 10002's known path. A root verifier saw both
checksums and all 2,000 small files.

The experiment also observed that a remounted export root can present a
permissive mode until the trusted supervisor re-establishes it. This validates,
rather than weakens, the existing startup rule: every worker must run
`ensureSandboxMountRoot`, re-stat root-owned `0711`, and remain unready on
failure. The proof intentionally used no pod `fsGroup`, which could interfere
with shared ownership semantics.

### Expansion and restart

The bound claim grew from `2Gi` to `3Gi`. A new mount completed filesystem
resize, reported 3,097,493,504 bytes, preserved both stream hashes and the
replacement marker, and re-verified root `0:0/0711`.

The committed conformance runner was then replayed end to end with internal
Longhorn NetworkPolicies enabled. Its two workers overlapped, every ownership,
isolation, `fsync`, atomic-rename, unlink, checksum, replacement, and hard
capacity assertion passed, a `1Gi` filesystem reported 997,376 KiB, and online
expansion reported 2,028,544 KiB for `2Gi` with prior hashes intact. The first
runner draft also exposed the real Kubernetes expansion ordering: creating a
new mount before controller/PV expansion leaves `FileSystemResizePending` and
requires another mount. The final runner waits for PV controller expansion,
then creates the fresh mount that completes filesystem/PVC expansion. Its
successful disposable PV/backend cleanup also passed.

The virtual machine then underwent a failed graceful stop followed by a hard
stop/start, representing a single-node power/restart boundary. K3s, `iscsid`,
Longhorn, the volume, and a new share-manager recovered. A new worker verified
the pre-restart hash and markers, sibling denial, and a new synced write. The
volume returned `attached/healthy`. This proves restart recovery of committed
bytes on the surviving disk, not node/disk redundancy.

### Forced share-manager failure

While a worker appended and synced once per second, the active share-manager
pod was force-deleted:

- Longhorn created a replacement share-manager that became Ready after 25.246 s.
- The existing client continued through some writes but did not complete or
  recover within its 15-minute failure deadline. Backend service readiness was
  therefore not client-I/O recovery evidence.
- The claim retained 45 committed pre-failure records.
- A fresh worker mounted after backend recovery, saw the 45 records and prior
  hashes, preserved sibling denial, and completed a new synced write in a
  32.848 s replacement run.

The expected product behavior is consequently fail-closed worker/turn loss plus
fresh-worker recovery after backend health, never transparent failover or
silent execution replay.

### Negative substrate proof

Docker Desktop/Kind was rejected as a reference environment after its LinuxKit
kernel lacked `iscsi_tcp`/`NETLINK_ISCSI`; `iscsid` failed with `Protocol not
supported`. The test did not bypass Longhorn prerequisites or present Kind as
production evidence. This failure becomes an explicit Forge preflight case.

### Evidence limitations still owned by HOR-469

HOR-469 must produce release-grade evidence on the actual reference substrate
for:

- three physical/virtual storage nodes and one-node replica loss/rebuild;
- full internal Longhorn NetworkPolicy allow/deny behavior on the release
  topology (the functional conformance replay used the enabled policy);
- chart install/re-apply/upgrade/uninstall and retained-volume disposition;
- exact production resource, latency, throughput, and capacity envelope;
- AgentPool readiness/diagnostic integration during backend failure;
- external StorageClass CI/reference fixture; and
- security context, image/SBOM/vulnerability, encryption-owner, and monitoring
  assertions.

## 14. HOR-469 implementation handoff

HOR-469 may proceed without reopening architecture when it implements only this
slice:

| Required implementation/evidence | Governing decision |
| --- | --- |
| Pin Longhorn `1.12.1`, K3s reference `v1.34.10+k3s1`, V1 engine, managed profiles, and exact images/chart | DES-HOR-424-01 |
| Add semantic chart values, same-version Longhorn companion/namespace ordering, Forge host prerequisites, and overlay class propagation | DES-HOR-424-02, DES-HOR-469-01 |
| Render `iterabase-rwx`; implement external static/live conformance and fail-closed AgentPool class checks | DES-HOR-424-03 |
| Enforce topology replica count, reserve/over-provisioning, hard capacity, `Retain`, expansion-only, and capacity alerts | DES-HOR-424-04 |
| Gate readiness, surface stable reasons/events, run worker/share-manager/node-loss recovery without replay, and recycle clients | DES-HOR-424-05 |
| Enable internal NetworkPolicies; keep UI private; document encryption owner; validate upgrade, re-apply, decommission, and deletion confirmation | DES-HOR-424-06 |
| Provision the internal-CA Longhorn gRPC leaf before startup, reject unauthenticated/plaintext current services, document the same-host NFS boundary, gate multi-node production on HOR-519, and add compact operator dashboard signals | DES-HOR-469-02 |

The implementation is incomplete until all of these pass:

1. chart lint/render/schema/unit checks for both modes/profiles and every invalid
   combination;
2. Forge prerequisite idempotency and negative missing-iSCSI/NFS/mount-
   propagation checks;
3. fresh managed single-node install and exact managed StorageClass;
4. reference three-node, three-replica healthy creation and one-node replica
   rebuild without treating it as encrypted-network production qualification;
5. the generic conformance script against managed and at least one maintained
   external reference class;
6. actual AgentPool two-worker mount/session isolation and worker replacement;
7. expansion, full-capacity, wrong-class, missing-class, root-squash, and mount-
   root failure paths;
8. forced share-manager and node failure with readiness loss, actionable
   diagnostics, committed-data recovery, and no turn/effect replay;
9. chart/Forge re-apply and supported patch/adjacent-minor upgrade without PVC
   recreation or data loss;
10. disable/uninstall refusing active consumers, then deliberate retained-volume
    disposition and complete cleanup;
11. a mandatory exact-head PR real-machine single-node scenario that packages
    and selects the current platform, certificate, and RWX companion charts with
    TLS-on managed single-node values, then proves the platform CA issued
    `longhorn-grpc-tls`, every current instance-manager service uses mutual TLS,
    and unauthenticated TLS/plaintext gRPC are rejected;
12. separately enforced exact-head chart static and observability-runtime owner
    checks proving the compact Longhorn panels render in `50 — Data and
    Storage`; and
13. exact release evidence for versions, images/digests, settings, nodes/disks,
    class, claims, results, timings, and accepted single-point-of-failure limits.

### Explicit non-goals

- Selecting another backend or data engine.
- Two-node managed storage, nested Longhorn, cross-region replication, or
  seamless share-manager failover.
- Artifact/object storage or PostgreSQL backup changes.
- Making session PVCs authoritative DR data.
- Customer-facing storage/backend configuration or Longhorn UI.
- Arbitrary Longhorn values, imperative customer installation, automatic class
  discovery, or fallback to default/local-path storage.
- Shrink, automatic destructive reclaim, forced downgrade, or forced uninstall.
- Any production or availability claim based only on HOR-424's nested-VM
  benchmark.

## 15. Acceptance mapping

| HOR-424 acceptance criterion | Evidence in this record |
| --- | --- |
| Approved comparison and selection | DES-HOR-424-01–06 and section 3 |
| Two-worker RWX read/write without cross-session leakage | Section 13 and executable conformance gate |
| Lifecycle, upgrade, failure, recovery, capacity, backup, security, and ownership explicit | Sections 4–12 |
| BYO conformance and diagnostics testable | Sections 8 and 11 plus `historical/hor-424-rwx-conformance.sh.txt` |
| HOR-469 can proceed without reopening architecture | Section 14 exact implementation handoff |

Semantic publication classification for HOR-424 is **None**: this ticket adds a
decision record and validation contract only. Longhorn and the managed/external
chart behavior do not enter a semantic artifact until HOR-469 implements,
validates, reviews, and releases them through the normal product release gate.
