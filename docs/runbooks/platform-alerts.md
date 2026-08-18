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

Treat this as a possible external effect. Find the durable invocation by time and operator logs, inspect its persisted consequence summary, and reconcile with the external system. Never automatically retry. Record the reconciliation decision before explicit follow-up action.

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

Inspect Certificate, CertificateRequest, issuer, DNS challenge/API connectivity, and renewal events. Validate the renewed chain and rollout/reload path before considering manual replacement.

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
