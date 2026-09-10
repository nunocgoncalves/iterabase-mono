# Platform V2 OpenEBS LVM LocalPV storage

Status: current repository implementation contract for HOR-545 and approved `DES-HOR-545-01` / `DES-HOR-545-02` / `DES-HOR-545-03` / `DES-HOR-545-04`, recorded canonically in Obsidian `Platform V2 — OpenEBS LVM LocalPV Storage`.

## Supported topology

Platform V2 supports exactly one schedulable K3s server node and one or more explicitly selected blank stable whole data disks. Forge prepares only physical volumes and one fixed thick volume group, `iterabase-data`. Chart-owned OpenEBS LVM LocalPV `1.10.0` creates every platform LV, XFS filesystem, mount, and claim lifecycle.

K3s local-path/default/root-backed storage, Longhorn, RWX, NFS/iSCSI, thin provisioning, online expansion, alternate/BYO classes, existing PV/VG/filesystem adoption, disk-set extension/replacement, migration, and multi-node/HA are unsupported.

## Forge device and transaction contract

`spec.dataStorage.devices` is required, non-empty, unique, lexically ordered, immutable, and limited to `/dev/disk/by-id/...` whole-disk identities. Interactive init selects one or more; repeated `--data-storage-device`, comma-separated `FORGE_DATA_STORAGE_DEVICES`, and direct YAML represent the same canonical set. Explicit selection is the sole first-write authorization. No force, wipe, adopt, extend, “format anyway,” automatic candidate, or root fallback exists.

Before disk mutation and immediately before each `pvcreate`, Forge checks the complete set with bounded required probes:

- stable link, resolved device, model/serial/WWN, size, non-removable whole-device identity, and distinctness;
- no partition table/partition, unsupported mapper/loop/RAID/network device, foreign holder, mount, swap, system/root/boot/EFI/K3s backing, or raw process consumer;
- no recognized filesystem, LVM, RAID, crypt, or partition signature on a not-yet-created candidate;
- no read, parse, `/proc` enumeration, descriptor, replacement, topology, or identity uncertainty.

Forge never reads every device byte. Arbitrary non-signature bytes are not a secure-erasure proof and do not authorize adoption.

After read-only preflight, Forge installs/verifies `lvm2` and XFS tools, loads `dm-snapshot`, and persists it in `/etc/modules-load.d/iterabase-data.conf`. A root-owned `0600`, atomic, file-and-directory-fsynced receipt at `/var/lib/iterabase/data-storage.receipt` binds contract `HOR-545/v2`, install, canonical paths, resolved identities, hardware/size, planned PV UUIDs, a unique Forge ownership tag, fixed VG name, completed PV count, observed VG UUID once created, and transaction stage.

The receipt and ownership tag are durable before `pvcreate`. Planned PV UUIDs plus exact metadata make a crash after command success but before receipt advancement recognizable without adopting a foreign PV. Forge creates every PV with its planned UUID, then uses supported `vgcreate --addtag` to atomically create exactly one `iterabase-data` VG with the receipt-owned tag and its LVM-generated UUID. It verifies the exact tag/PV/member/name combination and fsyncs the observed UUID into the receipt before any later mutation or K3s/substrate/platform handoff. Reconcile, status, and purge accept only exact receipt tag/PV/VG UUID/name/membership. Forge reports bounded total/free capacity and never creates an LV, filesystem, mount, or fstab entry.

An installed K3s whose service does not include `--disable local-storage` is immutable incompatible state and requires clean destroy/rebuild before any data-storage mutation. Fresh installs always add that disablement.

## Companion and content authority

`lvm-storage-substrate` is a published companion with the same version as `iterabase-platform` and `cert-manager-substrate`. It wraps the direct reviewed archive:

- chart: OpenEBS `lvm-localpv` `1.10.0`;
- URL: `https://openebs.github.io/lvm-localpv/lvm-localpv-1.10.0.tgz`;
- SHA-256: `3ad766c56d4a0ab0f3f2baaeb726a4554d1f51bb485f1cef00846cf1d82a179d`.

`.github/inputs/remote-content.json` is authority for that archive and every runtime image digest. The companion disables analytics, pins all controller/node/snapshot sidecars by digest, configures K3s's actual `/var/lib/kubelet` CSI registration/mount root, installs the three Kubernetes CSI VolumeSnapshot CRDs, and enables one cluster snapshot-controller plus the OpenEBS CSI snapshotter path. The wrapper replaces upstream controller RBAC so the managed no-secret snapshot class grants neither cluster-wide Secret read/list nor CRD create/delete.

Forge applies certificate substrate, LVM substrate, then platform. It waits boundedly for volume and snapshot CRDs, controller containers, node DaemonSet, `local.csi.openebs.io`, the exact one-node CSINode registration and topology keys, both StorageClasses, the one managed VolumeSnapshotClass, and one LVMNode reporting receipt-matching `iterabase-data`. Any local-path/default class, missing/extra class, contradictory VG, or unavailable volume/snapshot controller/node/CSI authority fails closed.

## Exact StorageClasses

Both classes are non-default, non-expandable, `ReadWriteOnce`, `WaitForFirstConsumer`, `Delete`, XFS, and thick. Their provisioner is `local.csi.openebs.io` and parameters include:

```yaml
storage: lvm
vgpattern: ^iterabase-data$
fsType: xfs
thinProvision: "no"
```

- `iterabase-lvm-xfs` adds `shared: "no"` and is explicit on every chart-generated data PVC: PostgreSQL, MinIO, persistent observability components, and any future enabled chart data claim.
- `iterabase-agentpool-lvm-xfs` adds `shared: "yes"` and is authorized only for one claim per AgentPool. `shared: yes` allows multiple pods on the same node to mount the RWO filesystem; it does not create RWX or cross-node behavior.

## Exact VolumeSnapshotClass

`iterabase-lvm-snapshot` is the only Iterabase-managed VolumeSnapshotClass. It is non-default, uses `local.csi.openebs.io`, `deletionPolicy: Delete`, and `snapSize: 100%`. An authorized operator may explicitly snapshot a managed LVM PVC, wait for both CSI `VolumeSnapshot` and OpenEBS `LVMSnapshot` Ready state, restore into a separate `iterabase-lvm-xfs` PVC, and delete restore/snapshot objects with leak-free LV reclamation while preserving the source.

The snapshot is full-origin thick capacity in `iterabase-data`, shares the same node/disks/VG failure boundary, and is at most crash-consistent. Platform charts create no snapshot automatically. This primitive provides no application consistency, schedule, retention catalogue, remote/offsite copy, cross-store recovery point, customer recovery API/UI, backup, or DR; OPP-005 remains the sole recovery authority.

## AgentPool validation and isolation

The API fixes `spec.sandbox.storageClassName` to `iterabase-agentpool-lvm-xfs`, access to `ReadWriteOnce`, and the generated claim to explicit Filesystem mode. Size is immutable thick capacity, not local-path planning metadata.

The controller validates the class before creating a claim. An unbound `WaitForFirstConsumer` claim remains mount-capable so the first worker can schedule. Once bound, readiness requires:

- one exact RWO/Filesystem/Delete PV with `local.csi.openebs.io`, `fsType: xfs`, non-empty volume handle, `openebs.io/volgroup: iterabase-data`, and exact OpenEBS local-node affinity;
- one unique Ready LVMVolume matching that handle, `volGroup`, `vgPattern`, `shared: yes`, `thinProvision: no`, and owner node;
- the owner LVMNode reporting `iterabase-data` with a non-empty UUID, no missing PVs, and no thin pool.

Class, access/volume mode, size, CSI, VG, OpenEBS identity, or topology drift withdraws readiness and quiesces workers without automatic turn/effect replay. Transient or unknown StorageClass/PV/LVMVolume/LVMNode observation errors withdraw readiness and fresh credit but retain healthy workers; only positively observed unsafe drift authorizes destructive quiescence. One storage assessment is reused per reconcile, and known OpenEBS volume/node identities are read directly rather than through repeated cluster-wide lists.

All trusted root supervisors in one pool may access that whole claim; separate pools receive separate PVC/PV/LVMVolume identities. Disposable children retain stable distinct session UID=GID, cleared groups/capabilities, `no_new_privs`, umask `0077`, root-owned `0711` pool root, session-owned `0700` trees, sibling denial, and path containment. `DES-HOR-538-03` remains authoritative for constrained cert-manager CSI AtomicWriter key validation, supervisor mTLS, and child `EACCES`.

## Capacity and failure

The harness measures and performs real write/fsync/rename/unlink health against its pool PVC. A claim-local state file and `runtime.workspace_capacity_state` row keyed by materialized pool preserve hysteresis:

- warn below 25% free;
- withhold all fresh credits for that pool at or below 20%;
- reopen only at or above 25%;
- never abort an active turn solely for threshold crossing.

Different AgentPool PVCs remain independent. Real I/O/fsync/mount/ownership failure fences without replay. Dispatch metrics carry a bounded `pool` label and manager conditions query only the matching pool.

The OpenEBS node metric endpoint supplies `lvm_vg_free_size_bytes{name="iterabase-data"}` and `lvm_vg_total_size_bytes{name="iterabase-data"}`. Chart-owned monitoring reports aggregate VG pressure independently and alerts on a managed PVC that remains Pending. Thick-capacity exhaustion for a new claim or full-origin snapshot must fail visibly and actionably; there is no reduced snapshot, thin overcommit, alternate class, or root fallback. Soft-deleting a pool removes its durable capacity row, process-local hysteresis entry, and every per-pool Prometheus label series; a same-UUID revival starts fail-closed until its new PVC is observed.

## Claim deletion, destroy, and purge

A deleted claim uses `Delete` and must remove its PV, LVMVolume, and LV after consumers release it. An explicitly deleted snapshot uses its class's `Delete` policy and removes the VolumeSnapshotContent, LVMSnapshot, and snapshot LV without deleting the source. Platform uninstall and ordinary `forge destroy` preserve the receipt, selected PVs, VG, LVs, snapshots, and bytes.

`forge destroy --purge-data-storage --reboot --yes` is the explicit fixture/decommission path. Purge refuses unless the receipt, ownership tag, set/order, resolved hardware, PV UUIDs, VG UUID/name/membership, system safety, mounts, raw consumers, and empty LV/snapshot set all agree. A fsynced receipt records intent and completion around VG removal, every receipt PV removal, and receipt removal so interruption resumes only the same exact decommission transaction. It has no force path and does not securely erase media.

## Release and acceptance

The affected semantic target set is `control-plane`, `forge`, `control-plane-chart`, and `iterabase-platform-chart`; the platform target contains both same-version companions. HOR-538 artifacts remain immutable historical evidence.

Required evidence includes focused owners, generated CRDs, static/render checks, real Kind OpenEBS provisioning/deletion and explicit snapshot/create/Ready/restore/hash/delete/source-preservation/full-origin-exhaustion/reapply/leak proof, permanent-fixture PV/VG receipt safety and deterministic reconcile/purge crash matrices, same-pool concurrency/isolation, separate pools, per-pool and aggregate capacity, worker replacement, reboot/reapply, MinIO Job identity, ordinary destroy/non-purge, explicit purge, exact-head CI/E2E, an exact-head explicit four-target candidate rehearsal, exact-source candidate, protected promotion, and fresh bare-metal Ubuntu 24.04 LTS OPO1 acceptance. Active F3 evidence uses only the provider-neutral `forge/permanent-fixture-cpu`, `forge/permanent-fixture-cpu-workspace`, and `forge/permanent-fixture-gpu` identities. Merge and publication do not complete HOR-545.
