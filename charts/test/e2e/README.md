# Chart runtime E2E

This directory owns the compiled runtime contracts for declarative chart behavior. Shared mechanics—Kind lifecycle, Helm and Kubernetes clients, polling, HTTP/TLS clients, redaction, and diagnostics—come from `testkit/e2e`; product assertions stay here.

| Scenario | Make target | Contract |
| --- | --- | --- |
| `certificate-ownership-migration` | `test-e2e-certificate-migration` | Published 0.2.2 owner → current companion owner → inverse rollback handoff |
| `fresh-install` | `test-e2e-install` | Ordered substrate, manager stability, issuer, CSI workload identity, LoadBalancer allocation, and verified ingress edge |
| `n-minus-one-upgrade` | `test-e2e-upgrade` | Checksum-pinned supported predecessor → exact current pair, schema ownership, state, Secret/PVC retention, hooks/Jobs, and rollout health |
| `feature-enable-upgrade` | `test-e2e-feature-enable` | Internal-TLS predecessor without observability → authoritative operator CRD pre-apply/Established gate → combined current observability/TLS client paths |
| `reapply-rollback-recovery` | `test-e2e-reapply-rollback` | No-rollout current reapply, supported inverse rollback to the declared predecessor, and forward recovery with retained state |
| `observability` | `test-e2e-observability` | Stack readiness, disjoint datastore/exporter endpoints, monitor discovery, and bounded Prometheus/Loki persistence |
| `observability-tls` | `test-e2e-observability-tls` | Internal-CA/DNS verification for stack servers, self-monitors, Grafana datasources/sidecars, Loki gateway, Promtail, and Alertmanager delivery |
| `internal-tls` | `test-e2e-internal-tls` | Platform identities, control-plane HTTPS, gateway clients, and rejected plaintext Redis/PostgreSQL transport |

Every target provisions a fresh uniquely named Kind cluster and executes once. There are no scenario retries or accepted flakes. On failure, `testkit/e2e/diagnostics` collects redacted Kubernetes, Helm, describe, and log evidence. CI persists it through `ITERABASE_E2E_DIAGNOSTICS`.

## Fixture modes

Set `ITERABASE_E2E_FIXTURE_MODE` explicitly:

- `source` uses local charts plus the full checkout SHA and immutable dependencies in `source-fixture.json`;
- `candidate` uses the exact release candidate plan and composed local candidate chart supplied by release CI;
- `published` uses `published-fixture.json` and exact OCI versions.

`transition-baselines.json` is the chart owner's explicit supported-predecessor authority. It currently pins the published platform/substrate `0.3.10` archives by OCI version and SHA-256. Candidate planning copies these inputs into `transition_baselines`, verifies the published bytes, and supplies those exact packages beside the selected current archives; it does not use a compatibility manifest or floating lookup.

The certificate migration source is always recorded as the immutable platform `0.2.2` chart. Published execution of the new observability verified-HTTPS contract requires a semantic chart publication containing the HOR-416/HOR-420 source changes; that publication and deployment authorization are deferred to product release review.

`make test-e2e-unit` runs the compiled hermetic scenario and intentional break fixtures for mutable/mismatched transition inputs, ambiguous duplicate or unestablished CRDs, Secret/PVC/rollout retention, rollback history, endpoint leakage, ambiguous persistence, and plaintext self-monitoring without provisioning infrastructure.
