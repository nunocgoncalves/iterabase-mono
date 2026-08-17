# Deterministic repository-owned testing strategy

This is the durable policy for Iterabase end-to-end validation. The one-time migration inventory remains in [`AUDIT-2026-08.md`](AUDIT-2026-08.md); current scenario coverage comes only from compiled owner registrations.

## Ownership

Assertions stay with the behavior they authorize:

| Owner | Authoritative behavior |
| --- | --- |
| Control-plane | Identity, API authorization, work/artifacts, AgentPool, dispatch, harness, gateway/tool execution, recovery, and browser journeys. |
| Inference gateway | Routing, snapshot consumption, authentication enforcement, rate limiting, request transforms, and component-local transport behavior. Shared producer/consumer scenarios remain close to the named owner suite. |
| Charts | Rendering plus declarative install, upgrade, feature enablement, reapply, rollback, TLS, Services, persistence, component rollout, and observability client paths. |
| Forge | SSH/k3s bootstrap, reality-as-state reconciliation, source/overlay handoff, host migration, secret transport, and CPU/GPU substrate behavior. |
| OPO1 / production | Only evidence that cannot be represented without the real GPU, DNS/ACME, SMTP, firewall, customer storage/data, or resource envelope. |

A dependent layer may retain a composition smoke check, but it does not become duplicate authority. Product/chart assertions are not moved into `testkit/e2e`; the shared module owns mechanics only.

## Fixture tiers

| Tier | Boundary |
| --- | --- |
| **F0** | Pure/static process, parser, fake, or hermetic mechanics example. |
| **F1** | Local real process, envtest, testcontainer, or native protocol integration. |
| **F2** | Fresh isolated Kind cluster with real Kubernetes, Helm, and network boundaries. |
| **F3** | Fresh ephemeral real CPU/GPU host reconciled through its real substrate path. |
| **P** | Mutable production confirmation that satisfies the strict criteria below. |

Tier is compiled scenario metadata, not an estimate of importance. A higher tier supplements rather than replaces faster owner authority.

## Exact fixture modes

Every suite execution records exactly one mode:

- **`source`** — one full source SHA plus an explicit dirty-worktree bit for local development. Local charts/images may be built from that checkout; every unselected dependency remains explicitly pinned. Source runs that consume published dependencies explicitly set `ITERABASE_E2E_SOURCE_INPUTS` to an exact checked-in input fixture; the library never loads one implicitly. Candidate fixtures reject dirty source.
- **`candidate`** — the release candidate plan's full source SHA, selected candidate identities, checksum/digest-pinned published baselines, and any owner-declared checksum-pinned transition predecessor required by a selected lifecycle scenario.
- **`published`** — explicit immutable semantic versions and, where available, digests/checksums.

There is no default inside the library, floating `latest`, matching-branch lookup, coordinated-ref fallback, or silent source→published fallback. Owner Make targets explicitly choose source mode for local use. Candidate and published workflows override it with their exact retained inputs. The fixture record is printed before scenarios execute and retained in candidate evidence.

## Suite entrypoint convention

Repository owners use one nested Go module and one top-level test:

```text
<owner>/test/e2e/go.mod
<owner>/test/e2e/*_test.go
TestE2E
```

`TestE2E` creates one `e2e.Suite`, registers typed scenarios/stages, and calls `Suite.Run`. Scenario metadata includes tier, references, supported fixture modes, release targets when relevant, and bounded Make/timeout/capacity data. Scenario and stage names are lowercase kebab-case and unique in their scope.

Current entrypoints are:

- `charts/test/e2e`
- `control-plane/test/e2e`
- `forge/test/e2e`

Each has an F0 hermetic example so registration and shared execution remain testable without infrastructure. Examples are mechanics evidence, not product coverage. The control-plane owner registers fresh-Kind identity/API, `manual_api` work/recovery, artifact-durability, execution, and locked-Chromium browser journeys; charts owns its rollout Kind matrix. Forge registers only CPU/GPU F3 scenarios after HOR-481 because its former Kind scenarios installed charts directly without exercising Forge.

Local commands:

```bash
make testkit-test       # shared race tests, every owner example, JSON/Markdown generation
make testkit-kind-example # explicit real Kind create/kubectl/delete validation
make e2e-catalogue      # JSON to stdout
make e2e-catalogue-check
make -C charts test-e2e-unit
make -C control-plane test-e2e-unit
make -C control-plane test-e2e-identity  # fresh Kind + source-built image
make -C control-plane test-e2e-work
make -C control-plane test-e2e-artifact
make -C control-plane test-e2e-execution
make -C control-plane test-e2e-browser
make -C forge test-e2e-unit
```

## Compiled catalogue

`testkit/e2e/cmd/e2e-catalogue` reads the committed Go workspace, finds modules ending in `/test/e2e`, compiles each `TestE2E` in catalogue mode, and merges the emitted registrations in stable suite/scenario order. Catalogue mode never resolves a runtime fixture or provisions infrastructure.

JSON and Markdown are two renderings of the same compiled registrations. A golden test covers Markdown generation. No hand-maintained coverage YAML/JSON or release scenario map is allowed.

## Deterministic stage semantics

A scenario owns typed state and ordered stages. Dependencies are explicit and may reference only previously registered stages:

- duplicate names, unknown/forward dependencies, and invalid metadata fail before execution;
- a failed or skipped prerequisite suppresses only its transitive dependents;
- independent stages continue, preserving useful fault localization;
- diagnostics run after a failure;
- every cleanup hook runs even if a prior cleanup hook fails.

External commands run exactly once with a positive timeout. Condition polling is bounded, observes immediately, and fails immediately on an observation error. Polling readiness is not permission to retry a failed assertion, scenario, install, request, or release gate. Required tests have no automatic retry or pass-on-retry status.

Fresh Kind clusters use a timestamp-plus-random DNS-safe name and a private temporary kubeconfig rather than `~/.kube/config`. Cleanup is idempotent. Stale clusters or local files therefore cannot satisfy or collide with a fresh-run contract.

## Shared mechanics

The testkit provides:

- one-shot bounded process execution with redacted retained output;
- unique Kind create/delete and local image loading;
- kubeconfig-bound `kubectl`, deterministic Helm values, exact chart validation, and loopback-only port forwarding;
- bounded plaintext HTTP and verified-CA/server-name TLS clients (no shared insecure-skip path);
- bounded readiness polling;
- Kubernetes resources/events, per-pod describe/current/previous logs, and per-release Helm state diagnostics;
- component-declared artifact collection;
- a Go seam that runs `npm ci`, invokes the locked Playwright binary with `--retries=0`, and collects declared traces/screenshots/reports.

Playwright/TypeScript owns browser assertions. Go owns fixture/runtime orchestration and process/artifact lifecycle. The control-plane browser owner uses the shared process seam, a Go-owned stable proxy to the verified deployed endpoint, and a Go restart coordinator; Playwright cannot provision or replace the stack.

## Failure evidence and secret handling

Process output and all text evidence pass through a shared redactor before persistence. Owners register exact runtime secret literals; structural rules also redact authorization/bearer values, credential-shaped keys, URL passwords, and private-key PEM blocks. Generic Kubernetes collection deliberately excludes Secret objects, and rendered Helm evidence strips every Secret `data`/`stringData` payload before shared redaction and persistence.

Component artifacts are fail-closed:

- text is copied only after redaction;
- opaque/binary bytes are rejected by default;
- an owner may explicitly declare an artifact **safe synthetic opaque** when its fixture cannot contain credentials or customer data.

That declaration is required for Playwright screenshots/traces and is part of the reviewable scenario code. The control-plane fixture is wholly synthetic; its owner sanitizes trace archive entries before declaration, deletes raw evidence, and independently rejects retained work-key literals before shared collection. Customer/production browser artifacts do not qualify.

A normal F2 failure bundle includes cluster resources, events, pod describes, current/previous logs, Helm list/state, revision history, effective values, hooks, status, process output, and declared component evidence. Forge F3 uses the same collector against its fetched kubeconfig, adds SSH/cloud-init and GPU-operator evidence, and records whether the failure belongs to provisioning, Forge substrate/reconciliation/handoff, dependent smoke, or cleanup. Diagnostics are best effort and do not suppress teardown.

## CI and release gates

Pull requests run affected owner checks and deterministic selected E2E. Control-plane or chart changes select all five control-plane-owned fresh-Kind/browser scenarios independently, preserving failure localization while each gets a fresh cluster. Changes to `testkit/e2e`, catalogue discovery, or shared CI selection fan out conservatively because they can invalidate every owner and release decision. Required checks do not silently skip selected deterministic scenarios.

The nightly schedule and explicit `complete_catalogue` manual rehearsal compile the catalogue from their exact source SHA and select every F2/F3 scenario exactly once under its registered owner. Every required scenario must declare source mode, a Make target, and a positive bound; every F3 entry must also declare mandatory named capacity. The retained complete-catalogue plan records the source SHA, selected IDs, owner totals, fixture mode, and dynamic Kind/browser and real-machine matrices. Source execution records its immutable published dependency fixtures, so this orchestration never resolves a floating coordinated fallback or creates a second scenario list.

Complete-catalogue Kind/browser work has bounded parallelism and isolated owner fixtures. CPU and GPU jobs use capacity-scoped non-canceling concurrency, require credentials and capacity, and fail rather than skip when either is unavailable. The schedule/manual aggregate requires selection, shared-harness validation, the complete Kind/browser matrix, and the complete real-machine matrix to succeed; skipped, failed, or canceled required jobs are incomplete. Owner cleanup and redacted diagnostics remain active on failure/cancellation, and the independent Forge reaper remains the backstop for interrupted cloud cleanup.

Release planning takes an explicit non-empty target set and selects the union of every compiled scenario whose `release_targets` intersects it. It does not use changed-file narrowing. The compiled metadata supplies owner, Kind and real-machine Make targets, bounds, and capacity requirements. Chart releases execute the chart owner's complete exact-candidate matrix through the reusable chart workflow; image-only releases can select chart-owned scenarios through the owner-aware generic candidate matrix without duplicating chart-release jobs.

The release gate preserves these invariants:

- all selected artifacts are exact candidates built once;
- unselected runtime dependencies are immutable manifest/plan-pinned published baselines;
- lifecycle predecessors come from owner-local immutable fixture authority, are copied into the generated candidate plan, and are checksum-verified before execution;
- coordinated target sets execute the deduplicated scenario union;
- Forge and platform-chart release coverage includes both CPU and GPU F3 scenarios;
- missing mandatory CPU/GPU credentials or capacity is incomplete/failing, never passing;
- generated candidate evidence binds the selected compiled catalogue metadata and stages.

Fixture tests in `.github/scripts/test_release.py` assert equivalent-or-stronger coverage for every target and the complete coordinated union.

## Production-only criteria

A check may remain tier P only when a representative ephemeral fixture cannot establish the claimed behavior without one of:

- the actual GPU/hardware or customer resource envelope;
- public DNS and ACME authorization;
- the real SMTP provider/mailbox;
- production firewall/routing topology;
- customer-owned storage or data-handling constraints.

Cost, test duration, missing automation, historical placement, or an inconvenient fixture do not make behavior production-only. Portable gaps receive an owner scenario/ticket. Production is confirmation rather than first discovery for portable behavior.

## Change control

Failure semantics, fixture resolution, artifact security, ownership boundaries, and release selection are architecture contracts. Changes require explicit approval under the root repository rules. Performance/load testing, duration history, flake dashboards, mutable test-state caches, and generic public-framework scope remain non-goals.
