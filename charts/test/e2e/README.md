# Chart runtime E2E

This directory owns the compiled runtime contracts for declarative chart behavior. Shared mechanics—Kind lifecycle, Helm and Kubernetes clients, polling, HTTP/TLS clients, redaction, and diagnostics—come from `testkit/e2e`; product assertions stay here.

| Scenario | Make target | Contract |
| --- | --- | --- |
| `certificate-ownership-migration` | `test-e2e-certificate-migration` | Published 0.2.2 owner → current companion owner → inverse rollback handoff |
| `fresh-install` | `test-e2e-install` | Ordered substrate, manager stability, issuer, CSI workload identity, LoadBalancer allocation, and verified ingress edge |
| `n-minus-one-upgrade` | `test-e2e-upgrade` | Checksum-pinned supported predecessor → exact current pair, schema ownership, state, Secret/PVC retention, exact completed Helm-owned provisioner Job, hooks, and rollout health |
| `feature-enable-upgrade` | `test-e2e-feature-enable` | Internal-TLS predecessor without observability → authoritative operator CRD pre-apply/Established gate → combined current observability/TLS client paths |
| `single-node-observability-ingress-recovery` | `test-e2e-observability-ingress-recovery` | Exact 0.3.12 one-node observability baseline → changed Loki gateway + first private ingress → injected pre-post-hook failure → fail-closed CA recovery/reapply → explicit legacy rollback → forward recovery |
| `reapply-rollback-recovery` | `test-e2e-reapply-rollback` | Blocking exact-current reapply with stable completed provisioner Job UID and no workload rollout, supported inverse rollback to the declared predecessor, and forward recovery with retained state |
| `metallb-upgrade-reapply` | `test-e2e-metallb-transition` | Checksum-pinned hook-era 0.3.19 predecessor → current ordinary-resource chart, pool/advertisement UID + LoadBalancer VIP preservation, controller health, exact reapply, and hook-predecessor rollback/forward |
| `observability` | `test-e2e-observability` | Stack readiness, exact candidate process targets over a valid synthetic Flux tool generation, disjoint datastore/exporter endpoints, GPU Operator-compatible DCGM Service discovery through the shipped dashboard namespace-variable and GPU-panel queries, and bounded Prometheus/Loki persistence |
| `observability-tls` | `test-e2e-observability-tls` | Internal-CA/DNS verification for stack servers, self-monitors, Grafana datasources/sidecars, Loki gateway, Promtail, and Alertmanager delivery |
| `internal-tls` | `test-e2e-internal-tls` | Platform identities, control-plane HTTPS, gateway clients, and rejected plaintext Redis/PostgreSQL transport |

Every target provisions a fresh uniquely named Kind cluster and executes once. There are no scenario retries or accepted flakes. On failure, `testkit/e2e/diagnostics` collects redacted Kubernetes, Helm, describe, and log evidence. CI persists it through `ITERABASE_E2E_DIAGNOSTICS`.

## Fixture modes

Required `source` and `candidate` runs both use the single composer-produced runtime bundle. The composer verifies exact-source temporary or immutable candidate chart archives, the same-recipe companion, selected nested charts, image digests, and checksum-pinned transition inputs before exposing one local platform/substrate composition. Fixture mode changes custody only; scenario IDs, Make targets, stage DAGs, and assertions are identical. `published` remains an explicit local compatibility fixture and is not a required PR/nightly/candidate route.

The observability scenarios always publish a deterministic in-cluster `GitRepository` artifact fixture through the same source-controller hostname contract used by the materializer. When a selected tool-runner image is present, Helm readiness proves that exact composed identity can verify, load, and register the valid generation instead of waiting on an intentionally absent source.

`transition-baselines.json` is the chart owner's explicit supported-predecessor authority. It currently pins the published platform/substrate `0.3.12` archives by OCI version and SHA-256, plus a second hook-era `0.3.19` pair for the MetalLB upgrade/rollback transition. Candidate planning copies these inputs into `transition_baselines`, verifies the published bytes, and supplies those exact packages beside the selected current archives; it does not use a compatibility manifest or floating lookup. The N-1, feature-enable, single-node observability-ingress recovery, and reapply/rollback scenarios accept only source or candidate current charts and fail unless both current charts are newer than their predecessors; published mode remains excluded while its pinned current pair is `0.3.1`.

The certificate migration source is always recorded as the immutable platform `0.2.2` chart. Published execution of the new observability verified-HTTPS contract requires a semantic chart publication containing the HOR-416/HOR-420 source changes; that publication and deployment authorization are deferred to product release review.

`make test-e2e-unit` runs the compiled hermetic scenario and intentional break fixtures for mutable/mismatched or non-newer transition inputs, ambiguous duplicate or unestablished CRDs, Secret/PVC/all-stable-workload and completed provisioner Job identity retention, incorrect rollback revision/status/chart history, candidate tool-artifact integrity, disabled-component readiness, endpoint leakage, ambiguous persistence, and plaintext self-monitoring without provisioning infrastructure. The single-node recovery target separately provides the live lookup/hook-order, serving-certificate, admission-class, and injected-failure evidence that offline Helm rendering cannot produce.
