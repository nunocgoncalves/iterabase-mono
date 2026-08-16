# Control-plane deployed E2E

This is the control-plane owner's compiled `TestE2E` suite. It uses `testkit/e2e` mechanics while keeping product assertions here.

Each F2 scenario creates and deletes its own fresh Kind cluster. The reusable `deployedState` fixture installs the reviewed certificate-substrate and platform charts with verified control-plane API TLS, real PostgreSQL and MinIO, and the product services selected by each scenario. HOR-477 adds the source-built execution composition: AgentPool workers, durable dispatch, inference gateway, deterministic OpenAI-compatible backend, Flux-backed immutable tools, tool gateway/runner, artifacts, and human/consequence gates.

## Scenarios

- `deployed-identity-api`: bootstrap, JWKS/delegated identity, API scopes, soft deletion, migrations, and API restart.
- `deployed-work-recovery`: concurrent idempotent starts, list/detail/filter/timeline, blockers, feedback/revisions, immutable attempts, customer-safe projections, and ordered SSE reconnect after restart.
- `deployed-artifact-durability`: upload/publication, work linking, download, MinIO/API restart persistence, admin deletion, and durable tombstones.
- `deployed-execution-contracts`: exact source/candidate image composition, late-Secret AgentPool recovery with real discovery/invocation, worker SPIFFE/mTLS, in-flight cancellation and generation fencing on worker replacement, durable assignment and inference, immutable Flux tool registration and invocation attribution, concurrent duplicate idempotency, non-idempotent `outcome_unknown` without silent retry across runner recovery, artifact lineage, disposable-child/session isolation, human-gate resume, and exact consequential repetition confirmation.

The execution scenario replaces Forge's former `kind-inference-contract` and `kind-tool-runner-contract` ownership. Forge retains real host/GPU substrate and serving authority; the identity scenario remains duplicated temporarily until its separate replacement/removal gate.

## Commands

```bash
make -C control-plane test-e2e-unit
make -C control-plane test-e2e-identity
make -C control-plane test-e2e-work
make -C control-plane test-e2e-artifact
make -C control-plane test-e2e-execution
make -C control-plane test-e2e
```

Source mode is the default. Go builds the current checkout's control-plane image and, for execution coverage, the harness, tool-runner, inference-gateway, and deterministic runtime fixture. It labels product images with the full source SHA, loads them into Kind, resolves containerd runtime manifest digests, and verifies every deployed product container uses the exact expected digest. The Make target builds local chart dependencies first.

Candidate mode is selected by the release workflow with `ITERABASE_E2E_FIXTURE_MODE=candidate`, `ITERABASE_E2E_CANDIDATE_PLAN`, the composed `ITERABASE_PLATFORM_LOCAL_CHART`, and exact `CONTROL_PLANE`, `HARNESS`, `TOOL_RUNNER`, and `INFERENCE_GATEWAY` image repository/tag/digest values. Published mode is intentionally unsupported: the compiled release catalogue composes selected candidates with checksum/digest-verified immutable baselines.

Set `ITERABASE_E2E_DIAGNOSTICS` to retain failure evidence. Shared collection includes Kubernetes resources/events, pod descriptions/current/previous logs, Helm state, migration and object-store health, and a customer-safe request ledger. Bootstrap/work credentials are registered with the shared redactor before diagnostics can retain logs; request evidence excludes authorization headers and private request bodies.
