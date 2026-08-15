# Control-plane deployed E2E

This is the control-plane owner's compiled `TestE2E` suite. It uses `testkit/e2e` mechanics while keeping product assertions here.

Each F2 scenario creates and deletes its own fresh Kind cluster. The reusable `deployedState` fixture installs the reviewed certificate-substrate and platform charts with verified control-plane API TLS, real PostgreSQL and MinIO, and only the product services needed by HOR-478. Deployed dispatch materializes human gates; the paused zero-worker AgentPool is fixture plumbing for the `manual_api` workflow, while AgentPool/harness execution remains HOR-477 scope.

## Scenarios

- `deployed-identity-api`: bootstrap, JWKS/delegated identity, API scopes, soft deletion, migrations, and API restart.
- `deployed-work-recovery`: concurrent idempotent starts, list/detail/filter/timeline, blockers, feedback/revisions, immutable attempts, customer-safe projections, and ordered SSE reconnect after restart.
- `deployed-artifact-durability`: upload/publication, work linking, download, MinIO/API restart persistence, admin deletion, and durable tombstones.

The identity scenario is the control-plane-owned replacement for Forge's `kind-controlplane-identity`. Forge retains its existing scenario until the later replacement/removal ticket has green evidence.

## Commands

```bash
make -C control-plane test-e2e-unit
make -C control-plane test-e2e-identity
make -C control-plane test-e2e-work
make -C control-plane test-e2e-artifact
make -C control-plane test-e2e
```

Source mode is the default. Go builds the current checkout's control-plane image, labels it with the full source SHA, loads it into Kind, resolves the containerd runtime manifest digest, and verifies the API, manager, dispatch, migrate, and bootstrap containers use that exact digest. The Make target builds local chart dependencies first.

Candidate mode is selected by the release workflow with `ITERABASE_E2E_FIXTURE_MODE=candidate`, `ITERABASE_E2E_CANDIDATE_PLAN`, the composed `ITERABASE_PLATFORM_LOCAL_CHART`, and exact `CONTROL_PLANE_IMAGE_REPO/TAG/DIGEST` values. Published mode is intentionally unsupported: HOR-478 is source/candidate authority, while the retained Forge scenario provides pinned compatibility until ownership cleanup.

Set `ITERABASE_E2E_DIAGNOSTICS` to retain failure evidence. Shared collection includes Kubernetes resources/events, pod descriptions/current/previous logs, Helm state, migration and object-store health, and a customer-safe request ledger. Bootstrap/work credentials are registered with the shared redactor before diagnostics can retain logs; request evidence excludes authorization headers and private request bodies.
