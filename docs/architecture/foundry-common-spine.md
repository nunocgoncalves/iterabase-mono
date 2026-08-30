# Foundry common execution/domain spine and shared inference insertion map

- **Status:** Approved design; implementation is owned by follow-on tickets.
- **Approval date:** 2026-08-30
- **Architecture ticket:** [HOR-525](https://linear.app/horizonshift/issue/HOR-525/foundry-now-gate-service-api-state-book-and-kubernetes-ownership)
- **Decision:** `DES-HOR-525-01`
- **Product contracts:** Obsidian `Iterabase Foundry — Product Direction`, `Iterabase Foundry — Client Engagement Delivery — Product Requirements`, and `Iterabase Foundry — First Qualified Delivery Profile — Product Requirements`
- **Architecture handoff:** Obsidian `Iterabase Foundry — Headless Service and Forge Lab — Architecture Direction`
- **Engineering handoff:** Obsidian `Iterabase Foundry — Engineering Shaping Plan`
- **Related repository authority:** [`v2-authentication-authority.md`](v2-authentication-authority.md), [`v2-artifact-processing.md`](v2-artifact-processing.md), [`v2-parallel-cancellation-safe-restart.md`](v2-parallel-cancellation-safe-restart.md), [`../ci.md`](../ci.md), and [`../release.md`](../release.md)
- **Publication classification:** None

This record is the repository authority for the Foundry common service, module, API, state, book, Kubernetes, testing, and release ownership boundaries, plus the insertion map reserved for the wider `InferenceRuntimeCandidate` lane. It does not implement a module, API, schema, package, worker, chart, release target, data transfer, GPU run, publication, or deployment.

A product-behavior change or a different inherited component, datastore, transport, identity, failure, isolation, Kubernetes-authority, testing, or release model requires a new approved architecture decision. HOR-526 and HOR-531–HOR-536 retain every policy or mechanism explicitly deferred below.

## 1. Approved design decision

### DES-HOR-525-01 — Foundry common execution/domain spine and shared inference insertion map

- **Approved by:** Nuno Gonçalves
- **Approved on:** 2026-08-30
- **Scope:** The complete bounded architecture bundle in section 2.
- **Consequences:** The operational, compatibility, sequencing, and rejected-alternative consequences in items 17–18 of the bundle are accepted.
- **Evidence:** Founder approval is recorded durably in HOR-525. This repository record preserves the exact approved bundle and its requirement/candidate validation walk.

## 2. Exact approved bundle

The complete revised bundle was approved exactly as follows:

1. **Module, service, and release ownership.** Add a future independently buildable top-level Go module `foundry/`, without merging any existing module. One independently versioned `foundry` target (`foundry/VERSION`, `foundry-v<semver>`) publishes `ghcr.io/nunocgoncalves/iterabase-foundry`; that image contains separately deployable API, coordinator, worker-broker, and migration entrypoints. Charts add an independently versioned `foundry-chart` target (`charts/charts/foundry/Chart.yaml`, `foundry-<semver>`, `oci://ghcr.io/nunocgoncalves/iterabase-charts/foundry`) and an optional `iterabase-platform` dependency. `forge lab` stays in the existing Forge CLI/archive target. HOR-525 creates no implementation or release.

2. **Narrow shared-contract exception.** Add later a source-only `contracts/` Go/protobuf module containing generated wire types only—no runtime/domain logic and no independent semantic artifact. It owns `iterabase.foundry.operator.v1` (Forge↔Foundry), `iterabase.foundry.controlplane.v1` (Foundry↔control-plane), `iterabase.foundry.worker.v1` (Foundry↔broker/workers), and the owner-only seam for `iterabase.foundry.substrate.v1` (Foundry↔Forge substrate). This is the sole approved cross-module import exception; local `replace` directives preserve each module’s `GOWORK=off` build. Contract changes fan out to every consumer. No domain package imports another component’s `internal` package.

3. **Foundry durable state and byte ownership.** Foundry owns a separate installation-local PostgreSQL database named `foundry`, with separate migration-owner/runtime roles and no cross-database/table/view grants to Foundry, control-plane, or inference-gateway. It may share the installation PostgreSQL server only as infrastructure. Foundry metadata is authoritative for Foundry campaigns/datasets; object storage contains bytes only. Foundry owns separate `foundry-staging` and immutable content-addressed `foundry-evidence` buckets/credentials even when the installation MinIO is shared. Candidate workers receive no database or object-store credential. Control-plane artifacts/evidence are accessed only through versioned control-plane APIs with exact digest/lineage. GitHub, Hugging Face, and OCI objects remain immutable external references unless an explicitly authorized import materializes exact bytes.

4. **Policy-gated production-evidence export/materialization seam.** Control-plane remains sole authority for customer/tenant identity, work, runtime, Tool Gateway consequence, and control-plane artifact evidence. Foundry never queries control-plane tables, views, database projections, internal packages, MinIO keys, or bucket credentials. Control-plane owns a private mTLS asynchronous `ProductionEvidenceExportService` with `RequestProductionEvidenceExport`, `GetProductionEvidenceExport`, `WatchProductionEvidenceExport`, `CancelProductionEvidenceExport`, `OpenProductionEvidenceManifest`, and bounded exact-object streaming. A request is an idempotent durable control-plane operation; cancellation fences further materialization but never claims to erase already committed evidence or reverse customer effects.

   An authorized export selects exact immutable source ranges/references—not a database dump—and emits a content-addressed dataset snapshot plus digest-sealed manifest. The manifest must retain: installation/tenant; initiating human, request actor, executing identity, credential and action-decision references; approved consent/basis and scope; declared purpose; retention/delete-by and private-retention disposition/reference; redaction policy/version and applied transformations; labels and labeling actor/version; exact source cursors/event IDs/work/run/attempt/turn/invocation references; source and exported artifact/object digests and sizes; exporter/config/schema identity; creation time; and full parent/child provenance lineage. It excludes credentials, secrets, provider routes, infrastructure details, and any evidence outside the authorized selection.

   Foundry imports only through that service, verifies the manifest root and every object digest, materializes allowed bytes in `foundry-evidence`, and creates an immutable `DatasetVersion` linked to the control-plane export operation/manifest. Foundry then owns its own dataset/campaign metadata, use ledger, artifact references, and policy projection; it does not become authority for the source customer work/effects. Workers access only exact DatasetVersion objects through short-lived, task-bound, read-only Foundry credentials with no listing or transitive dataset access. HOR-526 retains the exact eligibility, consent, retention, deletion/withdrawal propagation, disclosure, customer/private custody, private-retention topology, redaction, labeling, and cross-tenant rules. Until HOR-526 approves them, production-origin import/read/use is denied. Raw production usage never flows automatically into prompts, adapters, checkpoints, models, or weights. Any external or Hugging Face export is a distinct explicit authorization/publication operation, is absent from the initial action catalogue, and remains denied until its owning gate approves it.

5. **Domain boundaries inside one Foundry service/release.** Separate API/package/SQL-schema owners are fixed: `core` owns BookRevision, Operation, Campaign, ExperimentAttempt, ExecutionReceipt, budget, idempotency, outbox, artifact manifest, and DatasetVersion references; `qualification` owns QualificationCell/Decision/QDP; qualified-tools owns ToolCandidate/ToolEvaluationSuite; engagement-delivery owns EngagementDefinition/EngagementCandidate/DeploymentProposal/CustomerAcceptanceDecision; supported-profile governance owns CoverageGraph/SupportDeclaration; inference/exact-hardware owns InferenceRuntimeCandidate references. The latter four outcome domains remain gated by HOR-531–HOR-536 and cannot be absorbed or implemented by a common-campaign ticket merely because they share a binary/store. Production Workflow, ToolGeneration, Model, and ModelBackend remain control-plane-owned; gateway aliases/routes remain inference-gateway-owned.

6. **Book authority.** Create later a separate, initially private GitHub repository `nunocgoncalves/iterabase-foundry-books` as authority for book narrative, declarations, and non-hidden fixtures/evaluator source. `foundry/` owns the versioned machine schema and deterministic compiler. A campaign names an allowlisted repository, immutable commit, and path; the service’s read-only importer resolves commit/tree/blob digests, aliases, and external references and produces one canonical plan digest before work. Branches, notebooks, local files, Kubernetes names, and floating tags are never evidence identities. Books cannot contain credentials, hidden answers, shell commands, executable scheduler logic, image selection, or PodSpecs. Hidden assets are references governed by HOR-526.

7. **Canonical V2 identity; no second Foundry IAM or human Kubernetes identity.** Ordinary `forge lab` authentication reuses the control-plane-owned V2 canonical humans/service actors, exactly current `operator|admin` roles, accountable automation ownership, and immutable API-key actions. There is no third human Foundry role, Foundry user/key/session table, per-human Kubernetes ServiceAccount, ordinary CLI TokenRequest/TokenReview flow, long-lived Foundry token, or role snapshot accepted from the client. V2 browser Settings remains the only personal/automation credential lifecycle authority. `forge lab` supplies a normal V2 bearer from stdin/environment or an OS credential store; it never writes raw bearer material to repository files, installation context, logs, evidence, or command output.

   Control-plane owns a narrow private mTLS `FoundryIdentityService.IntrospectFoundryCredential`. For every ordinary request or new watch/stream establishment, Foundry forwards the bearer over the authenticated service channel; control-plane hashes/resolves it against the current V2 epoch, account, role, owner, actor, expiry/suspension, and exact immutable action. It returns only a bounded principal/action decision envelope and short-lived audience-bound decision reference. Foundry caches no request authority, denies on introspection failure, and re-introspects on reconnect. Foundry owns method/resource authorization and an append-only Foundry audit; control-plane identity/security audit remains authoritative for the credential decision. For production-evidence export, control-plane independently revalidates/consumes the decision reference and current authority before committing the export request, preventing Foundry from asserting principal IDs or acting as an unbounded deputy.

8. **Exact Foundry API-key action catalogue and role eligibility.** Add these actions through the approved V2 authority model; arbitrary strings/wildcards/implied children are rejected and existing keys never widen automatically:

   - Personal current Operator or Admin: `foundry.books.read`, `foundry.books.validate`, `foundry.campaigns.read`, `foundry.campaigns.run`, `foundry.campaigns.cancel`, `foundry.evidence.read`, `foundry.proposals.read`, `foundry.datasets.read`, and `foundry.datasets.use`.
   - Personal current Admin only: `foundry.datasets.import`, `foundry.production_evidence.export`, `foundry.decisions.approve`, and `foundry.acceptance.record`.
   - Automation credentials, still requiring their current active Admin accountable owner under V2: only `foundry.books.read`, `foundry.books.validate`, `foundry.campaigns.read`, `foundry.campaigns.run`, `foundry.campaigns.cancel`, `foundry.evidence.read`, and `foundry.proposals.read`.

   Role establishes eligibility; the immutable credential action permits only its named endpoint family; current Foundry resource/policy checks may further restrict it. A production-origin/private DatasetVersion additionally requires a current personal Admin plus the exact dataset action and an approved HOR-526 policy, so Operator dataset read/use applies only to non-production data allowed by that policy. Import and production export are separated because either side may fail independently and each needs its own audit/idempotency boundary. Decision approval and customer-acceptance recording are Admin-only because they convert research evidence into a governed declaration; the action does not replace the named product/founder/customer gate. No bearer action creates credentials, mutates identity/security, deploys, publishes, changes support automatically, or exports to Hugging Face/external systems. Admin demotion/disablement follows existing V2 suspension/current-authority rules.

9. **Operator connectivity and API behavior.** The Foundry operator API is versioned Connect/protobuf over a deployment-selected private HTTPS endpoint; no public ingress is enabled by default. `forge lab` is a typed client and stores only re-fetchable non-secret installation context (`URL`, CA/server name, namespace, audience) under `~/.forge/<install>/`; it holds no campaign or identity authority. Normal lab calls use direct HTTPS—not kubectl, SSH, a Kubernetes API proxy, object mutation, or an arbitrary execution endpoint. Commands expose typed validate/create/start/status/watch/cancel/log/evidence/compare/proposal/decision operations, bounded redacted output, and structured machine-readable results.

10. **State, idempotency, cancellation, reconnect, proposal, and acceptance.** Every mutation requires an idempotency key and canonical payload digest: same key+digest returns the original durable Operation/resource; same key+different digest conflicts. Operations and campaign events have monotonic durable cursors. Campaign state is `draft|validated|running|cancel_requested|completed|failed|cancelled`; attempt state is `planned|admitted|running|evaluating|finalizing|succeeded|failed|cancelled|outcome_unknown`; qualification is separately `passed|failed|inconclusive`. Candidate phase, qualification, support, publication, deployment, and acceptance remain orthogonal.

   Cancellation first commits a scheduling fence, blocks future admissions, then sends best-effort exact-target controls; it never claims undo. Duplicate control delivery converges on operation/attempt/task identities. Pre-dispatch lease recovery may retry control delivery, but started execution/effects are never replayed. Original failures and later explicit attempts remain separate. Ambiguous external effects remain `outcome_unknown` until the owning ledger reconciles them. Receipt/artifact finalization failure cannot fabricate success. Watch/status/log/artifact reconnect resumes from durable cursors and never re-executes work.

   A DeploymentProposal is an immutable reference bundle over exact candidate/QDP/support/evidence/target/configuration identities; approval is a separate attributable append-only decision and never deploys. A CustomerAcceptanceDecision is a separate append-only `accepted|rejected|conditional` record bound to the exact deployed identity/configuration, acceptance evidence, actor, conditions, and time; it never rewrites campaign or deployment history. SupportDeclaration similarly binds exact profile/evidence identities and does not publish by implication.

11. **Control-plane execution/evidence integration.** Control-plane owns a dedicated private mTLS `FoundryExecutionService` with exactly bounded `ValidateExecution`, `StartExecution`, `GetExecution`, `WatchExecutionEvents`, `CancelExecution`, `GetExecutionReceipt`, and `OpenExecutionArtifact` operations, alongside the separately policy-gated production-export service. Start is idempotent on Foundry attempt UUID plus request digest and uses the existing production-compatible workflow/work/turn/harness/tool/artifact runtime, with a non-customer `foundry` source/correlation—not a research Workflow CRD or second executor. Control-plane remains authority for runtime snapshots, node/turn events, Tool Gateway invocations and `outcome_unknown`, artifacts, and cancellation fences. Foundry stores immutable references/copies only through APIs. Least-privilege coordinator/evaluator mTLS/SPIFFE service identities receive only their exact methods; candidates receive none. There is no direct DB/view/internal-package coupling and no `RetryExecution`: another trial is another Foundry attempt.

12. **Service-level Kubernetes worker authority.** No human credential, role, API key, or operator identity grants Kubernetes authority. Foundry API/coordinator submits signed typed WorkTickets to a dedicated stateless broker. Only the broker ServiceAccount has namespace-scoped RBAC to create/get/watch/delete its Jobs/Pods/events and bounded task Secrets. Distinct candidate/evaluator/builder/trusted-finalizer worker ServiceAccounts are selected by closed WorkloadKind/template policy; they normally have `automountServiceAccountToken: false`, no Kubernetes RBAC, and receive only a short-lived task-scoped Foundry credential/materialization Secret. Foundry-to-control-plane execution uses least-privilege mTLS/SPIFFE service identities rather than worker or human credentials.

   WorkTickets cannot carry shell/command, image tag, arbitrary image, PodSpec, env, volume, network, ServiceAccount, or host action. Chart/code-owned templates require digest-pinned trusted images, non-root, read-only root, RuntimeDefault seccomp, all capabilities dropped, bounded ephemeral workspace/resources/time/output, default-deny ingress/egress with exact destinations, no host namespaces/hostPath/socket, and no automounted Kubernetes token. Candidate/evaluator/builder/finalizer classes use separate namespaces/SAs/network policies. Workflow execution uses the control-plane API rather than a Job. Broker cancellation deletes a Job only after the durable Foundry fence and preserves ambiguity. Candidates receive no production, publisher, customer, host, node, cluster, database, or object-store authority.

13. **InferenceRuntimeCandidate insertion points only; HOR-536 mechanisms remain unapproved.** Foundry inference-domain state holds candidate/build/lease/evaluation/proposal references and receipts; the broker reserves a typed inference workload class, but no lease, health, quarantine, recovery, evaluation, trusted-build, identity-proof, or publication mechanism is approved here. Forge owns any future stateless in-cluster `forge-substrate-agent` implementing the narrow substrate observation/recovery seam; it stores no durable state and is released as `ghcr.io/nunocgoncalves/forge-substrate-agent` in the existing independently versioned `forge` target alongside CLI archives. HOR-536 must freeze exact RPC fields, health signals, lease/fence, quarantine/reset/reboot/clearance, and authority before implementation. Foundry remains durable authority for requests/results. Control-plane ModelBackend/Model remain the only production-compatible serving declaration/reconciliation path; a candidate is never one. Inference-gateway receives no candidate catalogue/selection API and routes only a separately trusted non-production materialization during later replay. Evaluation, selection, quarantine clearance, publication, support, and deployment do not move into control-plane, gateway, or Forge.

14. **Existing testkit, qualification, CI, and release semantics are inherited unchanged.** Every component keeps owner-local unit/integration/E2E assertions: `foundry/test/e2e` owns Foundry behavior; control-plane owns identity/execution/export; Forge owns thin-client and later substrate seams; charts own install/upgrade/rollback/RBAC/NetworkPolicy; inference-gateway owns only trusted replay/routing. `testkit/e2e` supplies mechanics only: typed bounded stages, diagnostics/redaction, exact process/Kind/Kubernetes/HTTP orchestration, compiled catalogue discovery, and explicit fixture mode. Fixtures are exact `source`, `candidate`, or `published`; source uses one checkout, candidate uses exact run-scoped digests/checksums, published uses immutable semantic identities, and floating `latest` is rejected. There is no automatic scenario/assertion retry or pass-on-retry; observation failures fail immediately, cleanup/diagnostics continue, and mandatory capacity cannot become a skip.

   Statistical qualification, hidden-case policy, aggregation, thresholds, confidence, no-pass-on-retry interpretation, QDP, and attempt evidence remain Foundry-owned and are not release E2E assertions. A failed experiment is immutable; any allowed later attempt is separately identified and the decision consumes the complete governed attempt set rather than rewriting the failure. Release E2E proves deterministic product integration/identity/evidence behavior, not model/tool statistical quality.

   PR CI remains root-owned/path-aware with stable aggregate contexts and owner jobs; shared contracts fan out to all consumers. Candidate E2E selection compiles owner registrations and takes the conservative affected-target union; `release/targets.json` remains artifact/version authority only. `foundry` and `foundry-chart` have independent versions. A manual candidate request names exact targets and an exact master SHA; each selected image/chart/archive is built once, coherent validation consumes exact selected candidates plus reviewed immutable published baselines, and the bundle records source SHA, versions, digests/checksums, fixtures, SBOMs, provenance, and evidence. Promotion rebuilds nothing, re-verifies the complete bundle, and requires one founder approval in the protected `release` environment before immutable semantic tags/charts/releases. Merge never publishes. HOR-525 publication classification is **None**.

15. **Required follow-on repository changes are enumerated, not implemented.** Root/owner AGENTS and READMEs add `foundry/` and `contracts/` ownership plus Forge thin-client/substrate-agent and chart boundaries; `go.work`, root/component Makefiles, Docker build, Buf/codegen freshness, CI selector/workflows/cache keys, gitleaks/RBAC/NetworkPolicy/static action/route classification, owner E2E catalogue, chart dependency/presets, `release/targets.json`, candidate/promotion scripts, baselines, SBOM/provenance, and release docs add `foundry`, `foundry-chart`, and the Forge substrate image. Control-plane follow-ons own V2 Foundry actions/introspection, execution, export policy/materialization, audit, and exact grants; Foundry follow-ons own DatasetVersion import/use/audit. Semantic publication remains explicit affected-target intent.

16. **All 29 deferred candidates have fixed insertion owners.** T1–T4 are shared contracts plus control-plane SDK/runner/gateway owners; T5–T9 are Foundry qualified-tools/core/broker owners; T10 is the Forge client. E1–E8 are Foundry engagement/qualification/reference owners; E9 is the Forge client. I1–I8 are Foundry inference/core/broker plus the HOR-536 Forge substrate seam; I9 is control-plane ModelBackend plus inference-gateway trusted replay; I10 is the Forge client. Production-evidence-dependent candidates use the export→DatasetVersion seam and remain blocked on HOR-526 policy. Every candidate still requires its named decision gates and fresh reshaping.

17. **Options considered and rejected.** Select the dedicated one-service Foundry module, generated-only contracts module, separate Foundry database/buckets, direct private typed API using V2 identity introspection, control-plane asynchronous export, and service-level broker authority. Reject: embedding campaigns or Foundry tables in control-plane; multiple initial Foundry microservices; CRD/Kubernetes-only durable state; direct control-plane DB/view/bucket/internal imports; a second workflow engine; automatic production-data feeds; raw usage entering prompts/training/weights; Foundry-owned human IAM/role/key/session authority; per-human Kubernetes ServiceAccounts, TokenRequest/TokenReview for ordinary CLI auth, public ingress by default, long-lived lab tokens, or Kubernetes API proxy as normal lab transport; Forge-held campaign/data state; API-owned notebook scheduling; arbitrary PodSpecs/shell/Docker/SSH; candidate-held K8s/production/publisher/customer authority; testkit-owned product/statistical qualification; pass-on-retry; candidate-as-ModelBackend/gateway identity; and automatic Git/HF/OCI/release/overlay/deployment mutation.

18. **Consequences, compatibility, and evidence.** This creates future operational service/image/chart, separate database/buckets, private control-plane contracts, V2 action-catalogue additions, service identities, codegen/CI/release targets, and policy-gated evidence-copy lifecycle. It increases follow-on work and requires coordinated control-plane+Foundry+charts validation, but preserves current control-plane identity/work/runtime/consequence/artifact authority, immediate V2 current-authority checks, existing gateway/Forge behavior, module independence, and protected release semantics. Foundry may remain disabled. No migration, API/schema/package creation, code, spend, GPU execution, data export, publication, support declaration, overlay mutation, or deployment occurs in HOR-525. First-QDP implementation remains blocked on HOR-524/HOR-526/HOR-531/HOR-532 and fresh reshaping; HOR-533–HOR-536 retain their gates.

   Durable evidence for the record will cite HOR-525; the founder’s bounded revision and final approval; the Foundry programme/first-QDP PRDs, Product Direction, Architecture Direction, and Engineering Shaping Plan; repository AGENTS ownership rules; `docs/architecture/v2-authentication-authority.md`; control-plane artifact/runtime/consequence authorities; `docs/ci.md`; `testkit/AGENTS.md`; and `docs/release.md`. Conversation memory alone will not be used as approval evidence: the final approval response, Linear record, and repository decision document will carry the approver/date/scope/consequences/options/evidence.

## 3. Requirement and architecture traceability

The tables below validate ownership and boundary continuity. They do not approve a deferred policy, threshold, tool contract, customer-connectivity mechanism, or `InferenceRuntimeCandidate` mechanism.

### 3.1 Product direction and proposed architecture register

| Authority | DES-HOR-525-01 coverage | Remaining gate |
| --- | --- | --- |
| `FND-PD-008` | Items 1, 9, and 12 fix the in-cluster service, thin `forge lab`, and Kubernetes worker owners. | Implementation reshaping only. |
| `FND-PD-009` | Items 3, 4, and 6 fix book Git authority and immutable GitHub/HF/OCI/data references. | HOR-526 owns dataset/private custody policy. |
| `FND-PD-011` | Items 5, 10, and 16 reserve all engagement-domain owners without combining their gates. | HOR-533/HOR-534 own connectivity and acceptance mechanisms. |
| `FND-PD-012` | Items 5, 12, and 16 place ToolCandidate work in Foundry’s isolated worker/domain path. | HOR-531/HOR-532 own the tool contract and candidate trust mechanism. |
| `FND-PD-013` | Items 2, 5, 10, 11, and 16 preserve typed result/certainty owners and control-plane consequence authority. | HOR-531 owns the exact v2 contract. |
| `FND-PD-014` | Items 10, 13, and 14 keep qualification, support, publication, deployment, acceptance, and release separate. | Owning outcome decisions remain required. |
| `FND-PD-015` | Items 16 and 18 preserve the first QDP as a forcing slice rather than the whole programme. | HOR-524/HOR-526/HOR-531/HOR-532 remain first-slice gates. |
| `FND-PD-016` | The complete bundle consumes the programme PRD as the superior lifecycle authority. | No product rescope is introduced. |
| `FND-PD-017` | Items 5, 10, and 16 separate support governance from engagement deployment/acceptance. | HOR-534/HOR-535 retain exact mechanisms. |
| `FND-PD-018` | Items 13 and 16 reserve shared insertion points without approving runtime/kernel mechanisms. | HOR-536 exclusively owns `FND-ARCH-019`–`021`. |
| `FND-ARCH-001` | Item 1 selects one independently buildable Foundry module/service/release. | Follow-on implementation. |
| `FND-ARCH-002` | Items 2 and 11 select versioned APIs and prohibit DB/internal coupling. | Wire schemas remain follow-on work. |
| `FND-ARCH-003` | Items 3–5 select separate relational/artifact authorities. | HOR-526 retains data policy. |
| `FND-ARCH-004` | Items 7–9 select direct private typed connectivity using V2 identity introspection. | Follow-on endpoint/chart implementation. |
| `FND-ARCH-005` | Item 6 selects the separate Git book and service compiler authorities. | Exact book schema remains follow-on implementation. |
| `FND-ARCH-006` | Item 12 selects the typed broker and closed workload-template authority. | Exact templates are follow-on work. |
| `FND-ARCH-011` | Item 14 selects testkit mechanics only and owner-local assertions/qualification. | Follow-on owner suites. |
| `FND-ARCH-013` | Items 5, 12, and 16 fix the ToolCandidate domain/worker insertion boundary. | HOR-532 retains exact trust-domain decisions. |
| `FND-ARCH-014` | Items 2, 5, and 16 fix shared-contract/control-plane ownership only. | HOR-531 retains manifest/result/SDK choices. |
| `FND-ARCH-015` | Items 10, 11, and 16 preserve typed uncertainty and Tool Gateway consequence authority. | HOR-531 retains exact wire/enforcement behavior. |
| `FND-ARCH-016` | Items 5, 10, 14, and 16 keep qualification and proposal authority in Foundry and production promotion elsewhere. | HOR-532 retains trusted promotion details. |
| `FND-ARCH-017` | Items 4, 5, 8, 12, and 16 reserve scoped data/connectivity/evidence seams. | HOR-533 owns customer-environment connectivity and credentials. |
| `FND-ARCH-018` | Items 5, 8, 10, and 16 reserve proposal/acceptance records without mutation authority. | HOR-534 owns exact persistence and approval mechanisms. |
| Proposed `FND-ARCH-019` | Item 13 places candidate/build/generated-artifact references in Foundry and reserves the broker build class. | Candidate identity and reproducible AOT/JIT/generated-artifact build remain unapproved and wholly owned by HOR-536. |
| Proposed `FND-ARCH-020` | Item 13 places durable lease/quarantine references in Foundry and only the stateless substrate seam in Forge. | Exact GPU lease/fence/health/quarantine/recovery/clearance remain unapproved and wholly owned by HOR-536. |
| Proposed `FND-ARCH-021` | Item 13 places evaluation/selection evidence in Foundry and later trusted replay in control-plane/inference-gateway. | Evaluators, gates, trusted rebuild, publication, and identity proof remain unapproved and wholly owned by HOR-536. |

### 3.2 First-QDP requirements

| Requirement | Ownership continuity proved by this decision |
| --- | --- |
| `REQ-FND-QDP-001` | Items 3, 4, 6, 10, and 11 require immutable/content-addressed identities across book, data, execution, and evidence. |
| `REQ-FND-QDP-002` | Items 5 and 10 place immutable candidate/attempt lineage in Foundry. |
| `REQ-FND-QDP-003` | Items 6, 9, and 10 place complete validation before admission under the Foundry service. |
| `REQ-FND-QDP-004` | Items 3, 10, and 11 assign requested/observed execution evidence to control-plane receipts referenced by Foundry. |
| `REQ-FND-QDP-005` | Items 5, 12, and 14 give environment lifecycle/evidence an isolated workload and owner-test path; exact environment policy remains deferred. |
| `REQ-FND-QDP-006` | Item 11 mandates the production-compatible control-plane runtime and rejects a second executor. |
| `REQ-FND-QDP-007` | Items 10, 11, and 16 preserve structured/tool/consequence/final-state evidence owners; HOR-531/HOR-532 retain exact tool details. |
| `REQ-FND-QDP-008` | Items 5, 10, and 16 preserve typed failure routing into the owning domain rather than candidate refinement. |
| `REQ-FND-QDP-014` | Items 3, 5, 10, 12, and 14 provide durable state, restart reconstruction, bounds, and owner-local evidence. |
| `REQ-FND-QDP-015` | Items 10–12 fix operation/attempt/task idempotency and prevent duplicate execution/effects. |
| `REQ-FND-QDP-016` | Items 10–12 fix durable scheduling fences, best-effort stop delivery, and honest effect retention. |
| `REQ-FND-QDP-017` | Items 5, 10, and 16 reserve the bounded refinement/failure-router owners; HOR-524/HOR-526 own exact policy. |
| `REQ-FND-QDP-018` | Items 7, 8, 11–13 separate human/service, worker, evaluator, coordinator, substrate, and promotion authorities. |
| `REQ-FND-QDP-027` | Items 9 and 10 provide the thin typed reconnectable `forge lab` operation journey. |
| `REQ-FND-QDP-028` | Items 3, 4, 10, 13, and 14 permit immutable proposals/references but no overlay, registry, or runtime mutation. |

### 3.3 Programme requirements

| Requirements | Ownership continuity proved by this decision |
| --- | --- |
| `REQ-FND-ENG-001` | Item 5 assigns immutable EngagementDefinition ownership to Foundry engagement-delivery. |
| `REQ-FND-ENG-002` | Item 5 gives capability requirements exact domain references without moving coverage authority into an engagement record. |
| `REQ-FND-ENG-003` | Item 5 assigns nearest-profile eligibility to supported-profile governance and consumption to engagement-delivery. |
| `REQ-FND-ENG-004` | Items 5 and 10 keep every material engagement delta as an immutable explicit reference rather than inherited support. |
| `REQ-FND-ENG-005` | Items 5 and 10 place budgets/stopping evidence in Foundry core while exact commercial policy remains gated. |
| `REQ-FND-ENG-006` | Item 5 assigns CoverageGraph to supported-profile governance, not an all-by-all runtime or deployment owner. |
| `REQ-FND-ENG-007` | Items 16 and 18 preserve the first OPO1/DSv4 cell as the forcing slice without widening the programme claim. |
| `REQ-FND-CAND-001` | Items 3, 5, 6, and 10 require coupled immutable/content-addressed candidate inputs. |
| `REQ-FND-CAND-002` | Items 5 and 10 assign immutable parent/change/researcher lineage to the owning Foundry domain. |
| `REQ-FND-CAND-003` | Items 3, 10, and 11 bind attempts to Foundry receipts and authoritative control-plane evidence. |
| `REQ-FND-CAND-004` | Items 10 and 14 retain failed/rejected attempts and prohibit pass-on-retry rewriting. |
| `REQ-FND-CAND-005` | Items 6 and 10 require aliases to resolve before plan/attempt identity is committed. |
| `REQ-FND-CAND-006` | Items 10, 13, and 14 separate candidate/evaluation authority from merge, publication, support, and deployment. |
| `REQ-FND-RSCH-009` | Items 3, 10, 12, and 14 provide durable, idempotent, cancellable, budget-owning campaign boundaries and typed recovery evidence. |
| `REQ-FND-GOV-006` | Item 10 assigns budget enforcement and cancellation truth to durable Foundry state. |
| `REQ-FND-GOV-007` | Item 9 assigns durable behavior to Foundry and keeps Forge a thin reconnectable client. |
| `REQ-FND-GOV-008` | Item 12 exposes only closed workload classes and rejects Docker socket, shell, SSH, or arbitrary PodSpec execution. |
| `REQ-FND-DEP-001` | Items 10, 13, and 14 make deployment output an immutable proposal and keep release/deployment mutation external. |
| `REQ-FND-ACC-001` | Items 5, 8, and 10 reserve immutable preregistered acceptance records and Admin-only approval eligibility; HOR-534 retains exact freeze mechanics. |
| `REQ-FND-ACC-002` | Items 5, 11, and 12 reserve exact-runtime/evidence execution seams without giving candidates customer authority; HOR-533/HOR-534 remain required. |
| `REQ-FND-ACC-003` | Item 10 assigns append-only accepted/conditional/rejected decisions and exact evidence references to engagement-delivery. |
| `REQ-FND-ACC-004` | Items 5 and 10 reserve immutable support-boundary, monitoring, rollback, and ownership handoff references. |
| `REQ-FND-ACC-005` | Item 10 keeps CustomerAcceptanceDecision orthogonal to QDP and SupportDeclaration, preventing silent catalogue generalization. |

### 3.4 Inference-runtime shared-boundary requirements

| Requirement | Shared insertion owner fixed here | Mechanism retained by HOR-536 |
| --- | --- | --- |
| `REQ-FND-INF-011` | Foundry inference domain stores immutable candidate/reference lineage. | Exact digest and generated-state identity. |
| `REQ-FND-INF-012` | Foundry campaign policy references protected controls; broker exposes a closed workload class. | Exact mutation/protection enforcement. |
| `REQ-FND-INF-013` | Foundry references build receipts; trusted builds use the broker; release custody remains protected. | Reproducibility, AOT/JIT/generated-artifact contract. |
| `REQ-FND-INF-014` | Foundry evaluator references results; later replay enters control-plane ModelBackend and inference-gateway paths. | Dispatch/fallback instrumentation and gates. |
| `REQ-FND-INF-015` | Foundry stores lease/quarantine references; Forge owns only the stateless substrate seam. | Lease/fence/health/reset/reboot/clearance. |
| `REQ-FND-INF-016` | Foundry qualification domain owns decisions; candidate and gateway cannot decide. | Numerical and serving-semantic evaluators/tolerances. |
| `REQ-FND-INF-017` | Foundry evidence references broker/Forge observations. | Safety/cleanup cases and clearance rule. |
| `REQ-FND-INF-018` | Foundry owns declared envelope/evidence references; control-plane/gateway own later exact replay. | Exact envelope schema and fallback boundary. |
| `REQ-FND-INF-019` | Foundry owns stage/decision references and blocks proposal on prior failure. | Ordered evaluator implementations and gates. |
| `REQ-FND-INF-020` | Foundry owns capacity/evidence references; broker/Forge supply bounded observations. | Workload, metrics, memory, long-context, and speculative evidence mechanics. |
| `REQ-FND-INF-021` | Foundry owns campaign/Pareto decision references and immutable attempts. | Comparison, stopping, and selection policy. |
| `REQ-FND-INF-022` | Foundry emits a proposal reference; protected release, control-plane replay, and gateway verify later identities. | Trusted rebuild/publication/deployed-identity proof. |

## 4. Acceptance-scenario dry walk

### 4.1 First-QDP scenarios

| Scenario | Dry-walk result |
| --- | --- |
| `SCN-FND-QDP-001` | Book compiler resolves every Git/HF/OCI/data reference before a durable validated campaign; unresolved aliases fail before admission. |
| `SCN-FND-QDP-002` | Environment lifecycle runs only through a declared closed worker kind; Foundry records exact receipts while owner-local tests prove reset/drift behavior. |
| `SCN-FND-QDP-003` | Foundry starts one idempotent control-plane execution; the existing runtime and Tool Gateway remain authoritative for the action and final evidence. |
| `SCN-FND-QDP-004` | The production-compatible runtime owns the exception/human-gate path; Foundry observes exact events rather than recreating the graph. |
| `SCN-FND-QDP-005` | Submission key+digest and attempt/task IDs converge duplicates; control-plane start also keys on attempt UUID+digest, so no second action is created. |
| `SCN-FND-QDP-006` | Operation/campaign cursors and API re-introspection allow Forge to reconnect without local state or replay. |
| `SCN-FND-QDP-007` | Coordinator restart reconstructs from Foundry state; worker loss, evaluator failure, and artifact finalization remain typed and cannot fabricate a pass. |
| `SCN-FND-QDP-008` | Candidate workers have only exact task inputs and no hidden/object-store authority; HOR-526 must approve hidden custody before implementation. |
| `SCN-FND-QDP-009` | Domain/failure references route tool, credential, environment, and evaluator defects to their owners rather than prompt/model mutation. |
| `SCN-FND-QDP-010` | New candidates/attempts are immutable children, budgets are server-owned, and failed evidence remains present; exact adaptive policy remains gated. |
| `SCN-FND-QDP-011` | Foundry owns metric/evidence references and qualification output; exact workload/statistical thresholds remain HOR-524/HOR-526 scope. |
| `SCN-FND-QDP-012` | Qualification is independent `passed|failed|inconclusive`; hidden failure yields no QDP and cannot be hidden by retry. |
| `SCN-FND-QDP-013` | Exact book/cell/runtime/hardware references plus control-plane and worker receipts provide replay identity; exact tolerance remains gated. |
| `SCN-FND-QDP-014` | Forge reads the immutable QDP/proposal; no API action has overlay, Git, HF, OCI, or deployment authority. |
| `SCN-FND-QDP-015` | QDP/support/proposal identities are immutable and orthogonal; changed semantic identities require a new cell or requalification decision. |

### 4.2 Programme engagement, deployment, and acceptance scenarios

| Scenario | Dry-walk result |
| --- | --- |
| `SCN-FND-ENG-001` | Foundry engagement/core domains can reference the versioned contract, nearest profile, deltas, and budgets; exact selection policy remains HOR-535. |
| `SCN-FND-ENG-002` | SupportDeclaration and engagement deltas are separate references, preventing similarity-based support inheritance. |
| `SCN-FND-DEP-001` | DeploymentProposal is content-addressed and append-only; no operator/worker action can mutate source, registry, Linear, HF, OCI, overlay, or cluster. |
| `SCN-FND-DEP-002` | Imported release/deployment evidence binds exact digests; mismatch remains a proposal delta and cannot be rewritten by Foundry. |
| `SCN-FND-ACC-001` | The engagement domain records an exact immutable accepted/conditional/rejected decision and evidence after the separately authorized run. |
| `SCN-FND-ACC-002` | Acceptance and SupportDeclaration are orthogonal; customer acceptance cannot widen the reusable catalogue. |

### 4.3 Inference-runtime scenarios, ownership only

| Scenario | Ownership continuity | Still unapproved |
| --- | --- | --- |
| `SCN-FND-INF-003` | Foundry candidate/build references → broker build kind → independent evaluator → later control-plane/gateway replay. | Reproducibility and dispatch proof. |
| `SCN-FND-INF-004` | Evaluator decision/rejection evidence stays Foundry-owned; broker/Forge observations are referenced and immutable. | Correctness/safety gates. |
| `SCN-FND-INF-005` | Foundry stores durable quarantine state/request; stateless Forge substrate agent observes/executes approved recovery. | Fault signals, reset/reboot, and clearance. |
| `SCN-FND-INF-006` | Foundry evaluator owns envelope evidence; control-plane/gateway own exact replay behavior. | Envelope matrix and fallback mechanisms. |
| `SCN-FND-INF-007` | Foundry decision blocks proposal/QDP/support when any required stage fails. | Evaluation ladder implementations. |
| `SCN-FND-INF-008` | Foundry references run-book evidence; broker and Forge seams supply bounded exact-hardware observations. | GLM workload, long-context, modality, memory, and speculation gates. |
| `SCN-FND-INF-009` | Foundry proposal → protected trusted build/release → control-plane ModelBackend → inference-gateway replay; mismatches become explicit drift. | Trusted rebuild and executable identity proof. |

## 5. All 29 deferred candidates

Each candidate remains absent from Linear and must be freshly reshaped after its listed decision gates. The table fixes insertion ownership only.

| Candidate | Exact insertion owner(s) | Boundary preserved |
| --- | --- | --- |
| T1 | `contracts/` wire/corpus source plus control-plane contract owner | No v1 semantic change or independent contracts release. |
| T2 | Control-plane-owned authoring SDK/harness package | Forge remains only a service client. |
| T3 | Control-plane trusted runner | Candidate execution remains outside the production runner. |
| T4 | Control-plane Tool Gateway/harness | Gateway consequence ledger remains authoritative. |
| T5 | Foundry qualified-tools domain/store/API | A ToolCandidate never becomes ToolGeneration implicitly. |
| T6 | Foundry broker and closed candidate workload class | No production/customer/Kubernetes/publisher authority. |
| T7 | Foundry qualified-tools evaluation plus control-plane trusted replay references | Statistical/product assertions remain Foundry-owned. |
| T8 | Foundry campaign/qualified-tools domains | Hidden controls, budgets, failures, and selection remain protected. |
| T9 | Foundry immutable proposal domain | No Git/registry/release mutation. |
| T10 | Forge thin `forge lab` client over operator API | No Forge durable state or arbitrary executor. |
| E1 | Foundry engagement-delivery domain | Consumes rather than duplicates supported-profile governance. |
| E2 | Foundry engagement/qualification references | No inherited support or catalogue mutation. |
| E3 | Foundry DeploymentProposal domain | Proposal only; no external writes. |
| E4 | Foundry evidence/reference import through owning versioned APIs | No cross-system authority or direct datastore access. |
| E5 | Foundry engagement domain using HOR-533 connectivity seam | Exact customer connectivity/credentials remain HOR-533. |
| E6 | Foundry engagement acceptance-contract records | Freeze/authority details remain HOR-534/HOR-526. |
| E7 | Foundry campaign/engagement acceptance evidence references | Customer execution remains separately authorized. |
| E8 | Foundry CustomerAcceptanceDecision and handoff references | No automatic support/catalogue widening. |
| E9 | Forge thin `forge lab` client | No customer-specific overlay behavior or deployment authority. |
| I1 | Foundry inference domain/store/API | Candidate is not ModelBackend, gateway identity, or support claim. |
| I2 | Foundry broker trusted-build class and immutable receipt references | Build/reproducibility mechanism remains HOR-536. |
| I3 | Foundry durable lease/quarantine references + broker + stateless Forge substrate seam | Recovery/clearance mechanism remains HOR-536. |
| I4 | Foundry independent evaluator domain/workload class | Correctness/safety implementation remains HOR-536. |
| I5 | Foundry inference evidence/run-book domain | Exact workload/capacity mechanism remains HOR-535/HOR-536. |
| I6 | Foundry campaign/inference domains | Pareto/comparison policy remains HOR-536/HOR-526. |
| I7 | Foundry qualification domain | Hidden workflow policy remains HOR-526/HOR-536. |
| I8 | Foundry immutable proposal domain | No source/build/publication mutation. |
| I9 | Control-plane non-production ModelBackend/Model plus inference-gateway trusted replay | Candidate never self-registers or becomes production-visible. |
| I10 | Forge thin `forge lab` client | No accelerator recovery or durable campaign authority in Forge. |

## 6. Failure, recovery, and authority validation

| Case | Required convergence/evidence | Owning boundary |
| --- | --- | --- |
| Duplicate API delivery | Same key+digest returns the original operation; a different digest conflicts. | Foundry API/core. |
| Duplicate worker/control delivery | Operation/attempt/task identity converges; no new attempt, effect, or spend is silently created. | Foundry coordinator/broker and control-plane start idempotency. |
| Forge/client disconnect | Reconnect re-introspects V2 authority and resumes from a durable monotonic cursor. | Control-plane identity + Foundry API; Forge is stateless. |
| Coordinator restart | Reconstruct from Foundry PostgreSQL operations, fences, leases, and outboxes. | Foundry core. |
| Worker loss before dispatch | Expired control lease may be reclaimed under the same task identity. | Foundry broker. |
| Worker/execution loss after start | Do not replay; retain failure or `outcome_unknown` until the owning ledger reconciles. | Foundry attempt + control-plane/Tool Gateway evidence. |
| Cancellation | Commit Foundry scheduling fence first, prevent admissions, then retry only exact control messages. | Foundry core with control-plane/broker targets. |
| Ambiguous external effect | Never infer undo/failure/success or retry; retain `outcome_unknown`. | Tool Gateway consequence ledger, referenced by Foundry. |
| Evaluator failure | Typed attempt/evaluation failure; no decision pass and no candidate-controlled retry concealment. | Foundry qualification; HOR-526 exact policy. |
| Receipt/artifact finalization failure | Remain `finalizing` or fail with staging evidence; never fabricate success. | Foundry core/artifact metadata. |
| Production export failure | Control-plane export operation remains authoritative; Foundry creates no DatasetVersion until full digest verification. | Control-plane export + Foundry import. |
| Production export cancellation | Fence future materialization; preserve committed source/export evidence and make no erasure/undo claim. | Control-plane export; HOR-526 deletion policy. |
| Proposal-only behavior | Proposal/approval records create no Git, registry, release, overlay, cluster, or support change. | Foundry proposal domain + protected external authorities. |
| Deployment identity drift | Stop acceptance; preserve mismatch as a new explicit delta/requalification input. | Foundry engagement references; release/overlay/runtime remain external authorities. |
| Acceptance evidence | Append exact deployed identity, cases, evidence, actor, conditions, and result without rewriting campaign/deployment/support history. | Foundry engagement-delivery domain. |
| Accelerator fault | Foundry owns durable request/quarantine references; Forge may only perform the HOR-536-approved stateless substrate action. | Foundry + Forge seam; all mechanisms deferred to HOR-536. |
| Release validation retry | No scenario pass-on-retry; an external-capacity retry is a new evidenced run and mandatory capacity cannot become a skip. | Owner E2E + testkit/release flow. |
| Statistical retry | Earlier failed/rejected/inconclusive attempt remains immutable and visible to the decision. | Foundry qualification; exact policy HOR-524/HOR-526. |

## 7. Follow-on gate summary

- **HOR-524:** first-QDP thresholds, workload, budget, and support decision.
- **HOR-526:** data/evaluator custody, exact retry/recovery policy, publication, production trust, consent, retention, deletion, disclosure, redaction, labeling, private custody, and cross-tenant policy.
- **HOR-531:** Tool Contract v2, SDK, result/certainty, and effect-observation contract.
- **HOR-532:** ToolCandidate trust domain and trusted promotion boundary.
- **HOR-533:** customer-environment connectivity and credential brokerage.
- **HOR-534:** deployment-proposal and customer-acceptance persistence/authority mechanisms.
- **HOR-535:** initial exact coverage graph and supported-profile governance mechanisms.
- **HOR-536:** all `InferenceRuntimeCandidate` build, accelerator lease/health/quarantine/recovery, evaluation, trusted-rebuild, and deployed-identity-proof mechanisms.

No deferred implementation candidate becomes engineering-ready merely because `DES-HOR-525-01` fixes its insertion owner. Each follow-on must re-read repository state, obtain its own required decisions, and preserve the independent module, owner-local test, affected-target release, and protected promotion contracts in this record.
