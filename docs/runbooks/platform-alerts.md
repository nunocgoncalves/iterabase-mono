# Platform alert runbook

These alerts describe internal operator health. They never assert customer-result correctness. Preserve durable state and ambiguous external effects before restarting or retrying work.

## Common first response

1. Record alert labels, start time, deployed immutable image/chart versions, and recent rollout events.
2. Check `00 — Platform Overview`, then the linked component dashboard.
3. Check pod readiness/restarts, Kubernetes events, Service endpoints, and recent structured logs in Loki.
4. Do not repeat a consequential tool action when its outcome is unknown.
5. Prefer disabling new work or rolling back immutable pins over deleting durable state.

## IterabasePlatformTargetDown

Confirm the target pod exists and is Ready, the named metrics port is present, and the ServiceMonitor/PodMonitor selects it. Validate `/metrics` from inside the cluster. Roll back the affected image/chart pin if the listener failed after rollout.

## IterabasePlatformTargetAbsent

Confirm the component is intentionally enabled in the deployed values and listed under `observability.alerts.expectedTargets`. Check that its workload, Pod, Service endpoint, and ServiceMonitor/PodMonitor still exist and that Prometheus discovered the monitor. Disable the expectation only when the component is intentionally removed; do not use it to silence an unexplained disappearance.

## IterabasePostgreSQLUnavailable

Check PostgreSQL pod/volume health, Service endpoints, TLS certificate validity, authentication, and exporter logs. Stop write traffic before destructive recovery. Restore from the accepted backup procedure only with explicit authority.

## IterabaseRedisUnavailable

Check Redis pod, Service, AUTH/TLS Secret, and exporter. Redis is not durable authority; restore connectivity rather than reconstructing product state from Redis.

## IterabaseControlPlaneAPIHighErrorRate

Use `10 — Control Plane` to identify normalized failing routes. Check API logs, readiness probes, Postgres pool saturation, MinIO, and recent migrations. Preserve idempotency keys and artifact metadata during rollback.

## IterabaseArtifactOperationsFailing

Check API/gateway route breakdown, MinIO readiness, bucket credentials, TLS, immutable metadata lifecycle, and object digest failures. Preserve metadata tombstones and never overwrite immutable bytes during recovery.

## IterabaseManagerReconciliationFailures

Identify the controller label and affected CR status. Check manager logs, RBAC denials, Kubernetes events, invalid immutable definitions, and dependency readiness. Do not manually mutate operator-owned children unless the controller is paused and recovery is documented.

## IterabaseGatewayOutcomeUnknown

Treat this as a possible external effect, including one classified during gateway crash recovery. Find the durable invocation by time and operator logs, inspect its persisted consequence summary, and reconcile with the external system. Never automatically retry. Record the reconciliation decision before explicit follow-up action.

## IterabaseToolRunnerDisconnected

Check gateway and runner readiness, mTLS certificate/CA validity, Service DNS, approved runner identity, and stream errors. Existing pinned versions must not be substituted.

## IterabaseToolGenerationUnavailable

Inspect Flux `GitRepository` artifact status, materializer logs, archive bounds, manifest validation, and digest checks. Keep the last valid generation serving when present; never bypass digest or namespace validation.

## IterabaseToolMaterializationFailures

Compare the failing Flux revision/digest with the last successful generation. Validate the artifact archive and all product/client manifests using the runner authoring command before promoting a corrected immutable revision.

## IterabaseDispatchWithoutWorkers

Check AgentPool replicas/status, worker pod readiness, RWX/RWO constraints, CSI-issued SPIFFE leaves, NetworkPolicy egress, and dispatch mTLS. Pending durable work must remain queued; do not synthesize assignments.

## IterabaseDispatchWorkerLosses

Inspect worker restarts, node/network health, heartbeat delays, fencing generations, and lease expiry. Confirm first-terminal-writer behavior and that no fenced generation remains authorized.

## IterabaseHarnessDisconnected

Check the individual harness pod, dispatch DNS/port, certificate validity, and reconnect logs. A disconnected supervisor must not continue a child execution; confirm fail-closed termination and retained WAL behavior.

## IterabaseHarnessStorageUnavailable

Stop new AgentPool scheduling and inspect the pool's `StorageReady` reason,
exact StorageClass/PVC/PV, CSI/mount events, available filesystem capacity, and
managed Longhorn volume/replica/share-manager/node health. Restore backend
health first; existing NFS clients are not trusted after an outage. Let the
operator create fresh workers and verify committed files. Do not replay a lost
turn or external effect automatically, expose session filenames/content, or
claim seamless failover.

## IterabaseLonghornNodeCapacityLow

Stop unbounded new claim growth. Compare Longhorn node capacity, usage, reservation, and scheduled bytes with the topology multiplier and required rebuild/snapshot reserve. Add reviewed physical capacity or reap explicitly eligible data; do not raise over-provisioning or count requested-but-unusable expansion as capacity.

## IterabaseLonghornDiskCapacityLow

Identify the dedicated disk and its node, then inspect used, reserved, scheduled, snapshot, and rebuilding replica bytes. Verify the fixed data-path mount still targets the approved SSD. Add capacity under the maintenance procedure rather than reducing the minimum-free reserve.

## IterabaseLonghornDiskUnschedulable

Inspect the Longhorn disk `Schedulable` condition/reason, node readiness, fixed data-path mount source/UUID, filesystem health, and free capacity. Keep new starts closed until the exact dedicated disk is mounted and Longhorn reports it schedulable again; never substitute the root disk silently.

## IterabaseLonghornVolumeCapacityHigh

Map the volume to its PVC and AgentPool. Check requested versus actual allocation and physical capacity for every replica plus rebuild reserve. If growth is approved, close or bound starts, prove backend health/headroom, expand the PVC monotonically, and wait for controller plus filesystem capacity before reopening.

## IterabaseLonghornVolumeDegraded

Stop unsafe new scheduling credit for the affected pool. Inspect volume robustness, engine, every replica, failed node/disk, rebuild events, share-manager, and committed-data evidence. Restore or rebuild to the profile's required replica count before creating fresh workers; do not claim uninterrupted RWX service or replay a lost turn.

## IterabaseLonghornReplicaRebuildStalled

Inspect the starting replica and its source/destination nodes, engines, network, destination disk schedulability, and capacity. Do not delete healthy source replicas or force a second maintenance operation while the volume lacks the approved replica count.

## IterabasePVCExpansionStalled

Compare PVC request, PVC status capacity, PV capacity, and mounted filesystem `df`. Inspect resizer and node-plugin logs plus PVC/PV/mount events. Keep the pool unready when usable capacity trails the request; retry only supported growth after physical-headroom proof and never attempt shrink.

## IterabaseRWXConformanceStale

Confirm the attestation still names the exact live StorageClass UID/provisioner. Record backend/CSI/Kubernetes/node-image/network identities, then rerun the same-release disposable two-worker conformance gate. A successful run deliberately recreates the deterministic attestation ConfigMap so `metadata.creationTimestamp` and `data.validatedAt` both advance; verify those timestamps and that the alert resolves. A recreated class or changed backend requires fresh evidence rather than relabeling the old ConfigMap.

## IterabaseLonghornShareManagerUnavailable

Stop scheduling the affected AgentPool and identify the exact PVC/PV/volume/share-manager. Inspect its Service/endpoints, pod events/logs, attached engine, replicas, DNS, and node. Restore backend and share-manager health first, terminate stale clients, and use fresh workers; never treat a Ready replacement Service as proof that old NFS clients recovered.

## IterabaseLonghornCSIUnavailable

Identify nodes missing `longhorn-csi-plugin` readiness. Inspect CSINode driver registration, plugin/controller pods, mount propagation, iSCSI/NFS packages and services, kubelet/containerd, and attach/mount/expand events. Restore CSI on every required worker/storage node before replacing workers or starting expansion.

## IterabaseRetainedRwxPVRequiresDisposition

Find the former AgentPool/PVC, Longhorn volume, owner, and approved recovery need. Settle/reap sessions, then follow an explicit transfer or delete/sanitize plan. Verify PV and backend-volume removal plus reclaimed physical capacity, and retain only content-free audit evidence. Never auto-adopt or force-uninstall a retained volume.

## IterabaseHarnessReplayBacklog

Inspect harness WAL files/logs and dispatch event persistence/ACK logs. Verify cumulative ACK progress. Do not delete WAL state before confirming every durable event is committed or intentionally reconciled.

## IterabaseInferenceGatewayHighErrorRate

Break down by listener/model. Check snapshot freshness, model/backend availability, Redis/Postgres pools, backend latency, and gateway logs. Keep API-key and workload authorization paths distinct during diagnosis.

## IterabaseInferenceSnapshotStale

Check Postgres connectivity, LISTEN/NOTIFY health, fallback refresh logs, and last-refresh time. A stale gateway is intentionally not Ready; fix snapshot consumption rather than bypassing the freshness gate.

## IterabaseModelBackendUnavailable

Inspect ModelBackend/Model status, serving pod startup/readiness, GPU allocation/health, model files, and the inference snapshot. Do not route to an unpinned substitute model.

## IterabasePersistentVolumeAlmostFull

Identify the PVC and growth source. Confirm backups and retention policy before cleanup. Expand storage or publish an approved retention change; never delete Postgres/MinIO/Prometheus/Loki files directly.

## IterabaseControlPlaneDatabasePoolSaturated

Identify the component target, then correlate request/reconciliation load, acquire wait, slow queries, and Postgres connection capacity. Do not raise limits without checking server capacity and query behavior.

## IterabaseInferenceDatabasePoolSaturated

Inspect API/workload concurrency, snapshot refresh behavior, active queries, and Postgres capacity. Preserve the snapshot freshness gate while remediating.

## IterabaseInferenceRedisPoolTimeouts

Inspect Redis availability/latency, blocked clients, gateway concurrency, and pool sizing. Rate-limit checks currently fail open on infrastructure errors; treat sustained timeouts as degraded protection.

## IterabaseCertificateExpiringSoon

This warning fires after a cert-manager Certificate remains past its scheduled renewal time for ten minutes, or when its remaining lifetime stays below one hour for ten minutes as an emergency fallback. A healthy short-lived leaf does not fire merely because its total duration is below a fixed threshold. Inspect the Certificate's `Ready`/`Issuing` conditions, `renewalTime`, CertificateRequest and issuer state, DNS challenge/API connectivity, and renewal events. Validate the renewed chain and rollout/reload path before considering manual replacement; successful renewal advances the exported renewal and expiration timestamps and resolves the alert.

## IterabasePrometheusRuleEvaluationFailures

Open Prometheus rule status and logs, identify the exact group/expression, and compare it with the deployed chart. Repair query/label drift; do not silence the failure by removing unrelated rules.

## IterabaseControlPlaneAPILatencyHigh

This alert exists only when an overlay sets an accepted threshold. Break down P95 by normalized route, then inspect request concurrency, Postgres pools, artifact transfers, and resource saturation.

## IterabaseInferenceLatencyHigh

This alert exists only when an overlay sets an accepted threshold. Inspect model queue depth, TTFT, token throughput, GPU utilization/memory, backend latency, and request/token shape before changing the threshold.

## IterabaseInferenceTTFTHigh

Inspect prompt size, queued/running requests, model cache pressure, GPU saturation, and backend startup/health. Adjust the threshold only through an accepted workload objective.

## IterabaseHarnessTurnLatencyHigh

Inspect model, tool, artifact, sandbox, replay, and child lifecycle latency. The threshold is workflow/deployment-specific and changes require an accepted operational objective.

## IterabaseModelQueueHigh

Correlate waiting and active requests with arrival rate, TTFT, GPU memory/cache, and model capacity. Reduce load or scale/tune only within the model deployment contract.

## IterabaseGPUUtilizationHigh

Correlate sustained utilization with queue depth, throughput, GPU memory, temperature, power, and XID errors. High utilization alone is not failure; this alert exists only when the overlay defines an accepted capacity threshold.
