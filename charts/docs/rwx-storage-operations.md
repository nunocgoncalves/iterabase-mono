# Managed RWX storage operations

This runbook implements `DES-HOR-424-01`–`06` and `DES-HOR-469-01`. The
architecture and responsibility authority remains
[`../../docs/architecture/v2-rwx-storage.md`](../../docs/architecture/v2-rwx-storage.md).

## Immutable dependency and license record

- Upstream chart/application: Longhorn `1.12.1`, Apache License 2.0.
- Chart repository: `https://charts.longhorn.io`.
- Upstream chart archive SHA-256:
  `d70764e2d6cce673482da4d91da5b44a9791cda842c1914f77e7806ad1cd94bb`.
- The dependency is consumed unmodified by `rwx-storage-substrate`; the
  companion's `Chart.yaml` and generated candidate SBOM retain its identity.
- Upstream source/license: <https://github.com/longhorn/longhorn/tree/v1.12.1>.

All runtime image tags retain upstream version identity and are pinned to their
multi-architecture index digest in the companion values. Chart validation
rejects changed identities and any rendered unpinned `longhornio/*` image.
Release candidate evidence records the packaged dependency and SBOM before
protected promotion.

| Image | Pinned index digest |
| --- | --- |
| `longhornio/longhorn-engine:v1.12.1` | `sha256:9b1b720b56df6612c9589cbc156acbca6419fa61de818d05db7226b0722f2868` |
| `longhornio/longhorn-manager:v1.12.1` | `sha256:83b79f57043fe1405e68bc0d4c7987accbc6bb512def3e0db12b31966c070801` |
| `longhornio/longhorn-ui:v1.12.1` | `sha256:03a3ce6673df6e948c261fe978a695adaa8fb190d68bfe5c358af8ee3d3fbef5` |
| `longhornio/longhorn-instance-manager:v1.12.1` | `sha256:b255f3279dd9d830ea153e9369928646dee519fd853036388926dddb5c66094b` |
| `longhornio/longhorn-share-manager:v1.12.1` | `sha256:efaf47aeb4e8615e312f0880df860bf2e5b9fa53006fe075f057c6dd4089f47d` |
| `longhornio/backing-image-manager:v1.12.1` | `sha256:dfb9452e4190fb80e39c7976a0036ac0ca314c05328b67952f8c165cbb4dabf3` |
| `longhornio/support-bundle-kit:v0.0.92` | `sha256:02baa824d9a4174747ab9db2635ae000b1198d2d5ed3a4c69caf28724224e783` |
| `longhornio/csi-attacher:v4.12.0` | `sha256:a814aa4784197116983ea13e376fc691e000a390de9d0b9fca2bc4a2fb7c4a1f` |
| `longhornio/csi-provisioner:v5.3.0` | `sha256:1bbb7b11d8087130e722e3249f364d0ab49ee3545e847c2f299e87b7e1ce5c4f` |
| `longhornio/csi-node-driver-registrar:v2.17.0` | `sha256:29f7cfd519008fe8f8dff5e79db43f70d65c43a89c08f1bafbb199ca90df79f0` |
| `longhornio/csi-resizer:v2.2.1` | `sha256:63d0aef25114d4a682b25afa6d9623a3cfcc19aca910269124408476bbe2c6fd` |
| `longhornio/csi-snapshotter:v8.6.0` | `sha256:2bca9ac55170efa61dc50e5cc8d9550373db2e3e5161d82d3fdaac5c25150360` |
| `longhornio/livenessprobe:v2.19.0` | `sha256:d0cb76b565ba9d36da0dc2b38e2b6a49a0ae4fe067b03086110682f32c600318` |

The validation/uninstall hook uses the multi-architecture
`alpine/k8s:1.34.1` index
`sha256:ec714df3813b5405292860f8a1c55c5727bf8c33c88992f1e981efad8065547f`.

## Install and readiness

1. Record topology, node/disk inventory, physical capacity, encryption owner,
   maintenance owner, and rollback.
2. Verify iSCSI, `iscsid`, NFSv4, shared mount propagation, ext4/XFS data path,
   required tools, and the `node.longhorn.io/create-default-disk=true` label.
3. Install the same-version companion in `longhorn-system` with the selected
   managed values file and exact platform attestation namespace.
4. Wait for the post-install hook. It requires exactly one eligible storage
   node for `single-node`, or at least three for `three-node`; then it proves
   two simultaneous mounts, UID/GID sibling isolation, rename/fsync/unlink,
   replacement persistence, hard claim capacity, expansion, and cleanup.
5. Verify the `HOR-469/v1` attestation and install the platform with the same
   semantic values. AgentPools remain unready until their exact class/PVC/PV
   and backend predicates pass.

The UI stays ClusterIP-only, internal NetworkPolicies remain enabled, V2 engine
and experimental RWX fast failover remain disabled, and no storage credential
enters workers.

## Capacity and monitoring

- `single-node`: one replica, minimum 25% unallocated/free root-disk reserve,
  explicitly no node/disk/storage HA.
- `three-node`: three distinct replicas on at least three storage nodes and
  dedicated SSD-backed `/var/lib/longhorn`; plan three physical copies plus
  snapshot/rebuild/free-space headroom.
- Alert on Longhorn node/disk schedulability and capacity, volume robustness,
  replica rebuild, share-manager/CSI readiness, PVC expansion, full filesystems,
  conformance age, and retained PVs. A full volume fails writes and readiness;
  it never spills to local or artifact storage.

## Failure and recovery

A worker, share-manager, node, network, or capacity failure removes AgentPool
storage readiness and scheduling credit. Restore Longhorn volume/replica/node
and share-manager health first. Replace affected workers and verify committed
hashes. Never interpret Service readiness as recovery of an existing NFS
client, replay a turn/effect, or claim seamless failover.

Technical diagnosis includes the exact class/PVC/PV, AgentPool condition,
worker mount/restart events, Longhorn volume/engine/replica/share-manager,
node/disk capacity, CSI components, and conformance result. Do not collect
session filenames/content, secrets, or unbounded logs.

## Expansion, upgrade, and reapply

Only monotonic PVC growth is supported. Prove physical replica/rebuild headroom,
change the declarative request, and wait for controller plus mounted-filesystem
capacity before reopening readiness. Shrink requires a separately reviewed
copy/cutover.

Exact reapply must preserve claim/PV UIDs and committed bytes. Upgrade only
through an upstream-supported same-minor patch or adjacent minor after
Kubernetes compatibility, important/known issues, health, capacity, system
backup metadata, and exact image review. Close starts, upgrade the companion,
rerun conformance/replacement/expansion/failure gates, then upgrade/reconcile
the platform. Downgrade is not promised.

## Decommission and uninstall

1. Close starts and reach zero active assignments/sessions/consumers.
2. Inventory every AgentPool PVC, retained PV, Longhorn volume, and customer
   data disposition.
3. Reap eligible sessions, then deliberately delete/sanitize or transfer each
   retained volume and verify physical capacity/data handling.
4. Uninstall the platform, then the RWX companion. Its pre-delete guard refuses
   any selected PVC, retained PV, or Longhorn volume and sets deletion
   confirmation only after all are absent.
5. Verify Longhorn CRDs/webhooks/pods/host mounts and the data path are removed,
   then remove the remaining substrate under the installation's rollback plan.

Deleting a values key, namespace, release, or AgentPool is never authorization
to destroy or abandon retained customer session bytes. Session volumes remain
outside authoritative PostgreSQL-plus-artifact DR, and encryption/key custody
remains customer-owned.
