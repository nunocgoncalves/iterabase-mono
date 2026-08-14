# Chart runtime E2E

This directory owns the compiled runtime contracts for declarative chart behavior. Shared mechanics—Kind lifecycle, Helm and Kubernetes clients, polling, HTTP/TLS clients, redaction, and diagnostics—come from `testkit/e2e`; product assertions stay here.

| Scenario | Make target | Contract |
| --- | --- | --- |
| `certificate-ownership-migration` | `test-e2e-certificate-migration` | Published 0.2.2 owner → current companion owner → inverse rollback handoff |
| `fresh-install` | `test-e2e-install` | Ordered substrate, manager stability, issuer, CSI workload identity, LoadBalancer allocation, and verified ingress edge |
| `observability` | `test-e2e-observability` | Stack readiness, disjoint datastore/exporter endpoints, monitor discovery, and bounded Prometheus/Loki persistence |
| `observability-tls` | `test-e2e-observability-tls` | Internal-CA/DNS verification for stack servers, self-monitors, Grafana datasources/sidecars, Loki gateway, Promtail, and Alertmanager delivery |
| `internal-tls` | `test-e2e-internal-tls` | Platform identities, control-plane HTTPS, gateway clients, and rejected plaintext Redis/PostgreSQL transport |

Every target provisions a fresh uniquely named Kind cluster and executes once. There are no scenario retries or accepted flakes. On failure, `testkit/e2e/diagnostics` collects redacted Kubernetes, Helm, describe, and log evidence. CI persists it through `ITERABASE_E2E_DIAGNOSTICS`.

## Fixture modes

Set `ITERABASE_E2E_FIXTURE_MODE` explicitly:

- `source` uses local charts plus the full checkout SHA and immutable dependencies in `source-fixture.json`;
- `candidate` uses the exact release candidate plan and composed local candidate chart supplied by release CI;
- `published` uses `published-fixture.json` and exact OCI versions.

The certificate migration source is always recorded as the immutable platform `0.2.2` chart. Published execution of the new observability verified-HTTPS contract requires a semantic chart publication containing the HOR-416/HOR-420 source changes; that publication and deployment authorization are deferred to product release review.

`make test-e2e-unit` runs the compiled hermetic scenario and intentional break fixtures for endpoint leakage, ambiguous persistence, and plaintext self-monitoring without provisioning infrastructure.
