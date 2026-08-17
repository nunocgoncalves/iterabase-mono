# Control-plane deployed E2E

This is the control-plane owner's compiled `TestE2E` suite. It uses `testkit/e2e` mechanics while keeping product assertions here.

Each F2 scenario creates and deletes its own fresh Kind cluster. The reusable `deployedState` fixture installs the reviewed certificate-substrate and platform charts with verified control-plane API TLS, real PostgreSQL and MinIO, and the product services selected by each scenario. HOR-477 adds the source-built execution composition: AgentPool workers, durable dispatch, inference gateway, deterministic OpenAI-compatible backend, Flux-backed immutable tools, tool gateway/runner, artifacts, and human/consequence gates.

## Scenarios

- `deployed-identity-api`: bootstrap, JWKS/delegated identity, API scopes, soft deletion, migrations, and API restart.
- `deployed-work-recovery`: concurrent idempotent starts, list/detail/filter/timeline, blockers, feedback/revisions, immutable attempts, customer-safe projections, and ordered SSE reconnect after restart.
- `deployed-artifact-durability`: upload/publication, work linking, download, MinIO/API restart persistence, admin deletion, and durable tombstones.
- `deployed-execution-contracts`: exact source/candidate image composition, late-Secret AgentPool recovery with real discovery/invocation, worker SPIFFE/mTLS, in-flight cancellation and generation fencing on worker replacement, durable assignment and inference, immutable Flux tool registration and invocation attribution, concurrent duplicate idempotency, non-idempotent `outcome_unknown` without silent retry across runner recovery, artifact lineage, disposable-child/session isolation, human-gate resume, and exact consequential repetition confirmation.
- `deployed-browser-journeys`: locked Chromium/Playwright customer journeys over a stable Go-owned proxy to the verified deployed API, covering in-memory authentication, EN/PT portfolio/search/detail, blocker feedback/uploads/downloads, loading/error/SSE reconnect, customer-safe rendering, the automated accessibility baseline, keyboard use, and critical responsive layout.

The identity and execution scenarios are the green product-owner replacements for Forge's former `kind-controlplane-identity`, `kind-inference-contract`, and `kind-tool-runner-contract` scenarios. HOR-481 removed those direct-chart Kind scenarios after the replacement gates passed; Forge retains only real-host CPU/GPU substrate authority and explicitly non-authoritative dependent serving smoke.

## Commands

```bash
make -C control-plane test-e2e-unit
make -C control-plane test-e2e-identity
make -C control-plane test-e2e-work
make -C control-plane test-e2e-artifact
make -C control-plane test-e2e-execution
make -C control-plane test-e2e-browser
make -C control-plane test-e2e
```

Source mode is the default. Go builds the current checkout's control-plane image and, for execution coverage, the harness, tool-runner, inference-gateway, and deterministic runtime fixture. It labels product images with the full source SHA, loads them into Kind, resolves containerd runtime manifest digests, and verifies every deployed product container uses the exact expected digest. The Make target builds local chart dependencies first.

Candidate mode is selected by the release workflow with `ITERABASE_E2E_FIXTURE_MODE=candidate`, `ITERABASE_E2E_CANDIDATE_PLAN`, the composed `ITERABASE_PLATFORM_LOCAL_CHART`, and exact `CONTROL_PLANE`, `HARNESS`, `TOOL_RUNNER`, and `INFERENCE_GATEWAY` image repository/tag/digest values. Published mode is intentionally unsupported: the compiled release catalogue composes selected candidates with checksum/digest-verified immutable baselines.

Set `ITERABASE_E2E_DIAGNOSTICS` to retain failure evidence. Shared collection includes Kubernetes resources/events, pod descriptions/current/previous logs, Helm state, migration and object-store health, a customer-safe request ledger, and browser JSON/network evidence. Failed browser tests add synthetic screenshots and Playwright traces only after an owner sanitizer removes the ephemeral work key and Go independently rejects any retained credential literal. Bootstrap/work credentials are registered with the shared redactor before diagnostics can retain logs; request evidence excludes authorization headers and private request bodies.
