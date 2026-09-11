# Foundry global trust boundaries: data, evaluators, recovery, publication, production, and authorities

- **Status:** Approved design; implementation and evidence collection are owned by follow-on tickets.
- **Approval date:** 2026-09-11
- **Architecture ticket:** [HOR-526](https://linear.app/horizonshift/issue/HOR-526/foundry-now-gate-data-evaluator-recovery-publication-and-trust)
- **Decision:** `DES-HOR-526-01`
- **Product contracts:** Obsidian `Iterabase Foundry — Product Direction`, `Iterabase Foundry — Client Engagement Delivery — Product Requirements`, and `Iterabase Foundry — First Qualified Delivery Profile — Product Requirements`
- **Architecture handoff:** Obsidian `Iterabase Foundry — Headless Service and Forge Lab — Architecture Direction`
- **Engineering handoff:** Obsidian `Iterabase Foundry — Engineering Shaping Plan`
- **Ownership handoff:** [`foundry-common-spine.md`](foundry-common-spine.md) (`DES-HOR-525-01`), [`v2-authentication-authority.md`](v2-authentication-authority.md), and [`../release.md`](../release.md)
- **Publication classification:** None

This record is the repository authority for the global Foundry trust boundaries across data, hidden qualification, evaluators, candidate execution, retry/recovery, publication, customer-environment evidence, deployment proposals, acceptance records, production-promotion separation, and exact authorities. It complements `DES-HOR-525-01` (module/service/API/state/book/Kubernetes/release ownership) and reserves the shared insertion map for the wider `InferenceRuntimeCandidate` lane without approving HOR-536's exact mechanisms. It implements no store, worker, evaluator, publisher, broker, proposal, data transfer, GPU run, publication, or deployment.

A product-behavior change or a different trust, data, evaluator, recovery, publication, authority, or inference-boundary model requires a new approved architecture decision. HOR-531–HOR-536 and the first-QDP statistical/data gates retain every policy or mechanism explicitly deferred below.

## 1. Approved design decision

### DES-HOR-526-01 — Foundry global trust boundaries and shared inference trust separation

- **Approved by:** Nuno Gonçalves
- **Approved on:** 2026-09-11
- **Scope:** The complete bounded trust-boundary bundle in section 2.
- **Consequences:** The operational, compatibility, sequencing, and rejected-alternative consequences in items 17–18 of the bundle are accepted.
- **Evidence:** Founder approval is recorded durably in HOR-526. This repository record preserves the exact approved bundle and its requirement/scenario validation walk.

## 2. Exact approved bundle

The complete revised bundle was approved exactly as follows:

1. **Dataset and evidence custody and authorities.** Foundry owns `foundry-staging` (import staging) and immutable content-addressed `foundry-evidence` (durable evidence) buckets/credentials even when the installation object store is shared. Every data/evidence class has an immutable content-addressed identity bound to purpose and tenant scope. Foundry-owned metadata is separate from control-plane artifact/evidence (accessed only through versioned control-plane APIs). Hidden/customer/private evidence is never stored in a candidate-readable, cross-tenant, or listing-able namespace. Workers receive only short-lived, task-bound, read-only, exact-object DatasetVersion credentials with no listing or transitive access. No cross-tenant raw evidence exists. Managed hosting and customer access are non-goals.

2. **Provenance, licensing, consent, tenant, privacy, redaction, retention, and production-evidence admission/use/withdrawal.** Every dataset/evidence class records provenance, licensing, redistribution, consent/basis, tenant and identity scope, purpose, preprocessing, redaction policy/version and applied transformations, deduplication, contamination analysis, partitions, checksums, and retention/delete-by with a private-retention disposition/reference. **Production-origin evidence admission and use:** production-origin evidence may be imported, read, or used only after (i) eligibility and tenant/identity-scope checks, (ii) approved consent/basis and declared purpose, (iii) redaction or approved private retention, and (iv) immutable `DatasetVersion` creation with exact digest/lineage; any unmet condition **fails closed** and no materialized bytes leave `foundry-evidence`. **Withdrawal and delete-by:** a customer/data-policy withdrawal or delete-by only bounds further access and future materialization and may prune raw staging/bytes only where separately authorized; it never erases already committed evidence, revokes prior qualification evidence, or reverses customer effects, and any retained evidence is sealed and relabelled for its approved audience. Cross-tenant reuse is prohibited without an explicit approved policy and legal basis. Raw production usage never flows automatically into prompts, adapters, checkpoints, models, or weights; external or Hugging Face export is a distinct explicit authorization/publication operation, absent from the initial action catalogue, and remains denied until its owning gate approves it. Disclosure and customer/private custody rules remain explicitly scoped and never erase committed evidence or reverse customer effects.

3. **Hidden-set and evaluator custody.** Hidden cases, expected states, oracles, and answer-bearing evaluator assets remain in evaluator-owned, separately permissioned private custody and are referenced—not embedded—in candidate-readable book content. Adaptive candidates cannot access hidden answers, answer-bearing traces, hidden aggregate results, or candidate-specific hidden feedback. Hidden evaluators run under a separate identity and expose only the decision/report allowed by policy. Candidate-controlled processes cannot select hidden cases or gates.

4. **Result-disclosure and protected-case boundaries.** Development/validation feedback is available to adaptive search; hidden qualification results and detailed hidden failure traces are not revealed during adaptive search. Discovery/development, validation, hidden qualification, security, and later governed production-canary data are distinct, versioned, contamination-controlled domains with explicit custody and leakage controls. Model judges are version-pinned, blinded to candidate identity, calibrated against human labels, and treated as fallible secondary evidence; programmatic final-state verification is primary.

5. **Evaluator immutability, blinding, and re-baselining.** Evaluators are immutable versioned programs; qualification workers execute the exact preregistered evaluator/gate identities under separate authority. A candidate cannot alter evaluators, gates, hidden cases, thresholds, or acceptance criteria. Any evaluator threshold/reward/qualification-contract change triggers re-baselining and requalification; evidence never silently propagates across a changed evaluator. Candidate-controlled code cannot falsify receipts or rewrite qualification evidence.

6. **Idempotency, retry, cancellation, reconciliation, and `outcome_unknown`.** Every mutation is idempotent (key + canonical payload digest) and no duplicate effect or duplicate spend is silently created. Pre-dispatch lease recovery may retry control delivery, but started execution/effects are never replayed. Original failures and later explicit attempts remain separate immutable attempts; a pass after retry never erases the original failure. Ambiguous external effects remain `outcome_unknown` until the owning ledger reconciles them; `outcome_unknown` and non-idempotent writes are never automatically retried. Cancellation commits a scheduling fence, blocks future admissions, then sends best-effort exact-target controls, and never claims undo. Worker/evaluator/publisher/coordinator loss produces typed durable state; receipts and qualification evidence are append-only/content-addressed, attributable, redacted for their audience, and cannot be overwritten by candidate code.

7. **Separate publisher and promotion identities.** GitHub, Hugging Face, OCI/registry, artifact, and overlay publishers are distinct identities from candidate/coordinator/researcher roles. No API action or worker has overlay, Git, HF, OCI, registry, or release mutation authority. A trusted publisher may act only through a separately authorized operation after validating exact receipts, licenses, checksums, and destination scope. Production promotion remains a normal reviewed overlay change with exact source evidence and rollback identity. Candidate, evaluator, campaign coordinator, artifact publisher, and production promoter have separate authority boundaries.

8. **Adaptive candidate execution boundary.** Candidate workers use only deterministic simulators or explicitly scoped non-production integration endpoints. Research workers receive no production credentials/systems, broad customer networks, hidden assets, Kubernetes/Docker/SSH access, Git write, publisher, or production-gateway registration authority. Actual customer validation is a separately authorized trusted boundary (HOR-533). Tool candidates (with HOR-532) and candidate code never register with the production gateway or become a recipient of production authority.

9. **Customer-environment evidence (with HOR-533).** Customer qualification uses explicitly scoped connectivity, identities, fixtures/data, rate and failure envelopes, drift detection, evidence capture, and target-state verifiers. Research workers receive neither general customer-network access nor production credentials. Exact connective/credential-broker mechanics remain HOR-533. Customer acceptance evidence is engagement-specific until explicitly curated, redacted, and approved for reusable support claims.

10. **Immutable proposals and acceptance records (with HOR-534).** A DeploymentProposal is a content-addressed reference bundle over exact candidate/QDP/support/evidence/target/configuration identities; approval is a separate attributable append-only decision and never deploys. A CustomerAcceptanceDecision is a separate append-only `accepted|rejected|conditional` record bound to the exact deployed identity/configuration, acceptance evidence, actor, conditions, and time; it never rewrites campaign, deployment, or support history. SupportDeclaration similarly binds exact profile/evidence identities and does not publish by implication. Exact proposal/acceptance persistence and authority mechanisms remain HOR-534.

11. **Qualification, support, publication, deployment, acceptance, and catalogue authorities are distinct and never implied.** Qualification does not silently imply support, publication, deployment, acceptance, or catalogue expansion. `qualified`, `supported`, `published`, and `promoted` remain separate actions and authorities. Customer acceptance cannot widen the reusable support catalogue. A candidate can never approve or deploy itself; researcher, worker, coordinator, evaluator, builder, publisher, deployer, qualifier, support authority, and acceptor each hold distinct authorities such that no single role lets a candidate approve or deploy itself.

12. **Budget and cancellation truth; evidence integrity.** Cost, storage, attempt, and wall-time budgets are enforced server-side. Cancellation stops future work without claiming to reverse completed effects. Receipts and qualification artifacts are append-only or content-addressed, attributable, redacted for their audience, and cannot be overwritten by candidate code. Failed/rejected/inconclusive attempts are retained and queryable. Release E2E never becomes the statistical/hidden qualification engine; no pass-on-retry.

13. **`InferenceRuntimeCandidate` shared trust separation only; HOR-536 mechanisms remain unapproved.** Foundry owns durable candidate/build/lease/evaluation/proposal references and receipts for the inference lane. This decision fixes only the shared separation among: untrusted candidate code/results; the trusted builder; the trusted accelerator broker plus the health/recovery/clearance authority; the evaluator and hidden-suite custodian; the receipt owner; the source reviewer; the publisher; the deployer; the qualifier; the support authority; and the production-compatible replay owners. Candidate code or results cannot forge trusted build/evaluation/health receipts, choose hidden cases or gates, clear quarantine, control reset/recovery evidence, register or route themselves through `ModelBackend` or the inference gateway, select publication destinations, or use production, publisher, deployer, or support credentials. It is never a production `ModelBackend`, gateway-visible catalogue identity, published runtime image, qualified `InferenceProfile`, QDP, or support declaration by implication. HOR-536 exclusively owns the exact build, lease/fence, pre/post-health, quarantine/recovery, evaluation, proposal, trusted-rebuild, deployment, and deployed-identity-proof mechanisms and alternatives.

14. **Existing paths consume only separately authorized evidence.** Control-plane, inference-gateway, Forge, chart, and protected-release paths never become candidate evaluation, selection, or self-promotion authorities. Control-plane `ModelBackend`/`Model` remain the only production-compatible serving declaration/reconciliation path. Inference-gateway receives no candidate catalogue/selection API and routes only a separately trusted non-production materialization during later replay. Forge owns only the thin `forge lab` client and, for the inference lane, a future stateless substrate observation seam; it stores no durable state. Candidate evaluation, selection, quarantine clearance, publication, support, and deployment do not move into control-plane, gateway, or Forge.

15. **Repository, release, and ownership compatibility.** This decision inherits unchanged from `DES-HOR-525-01`: independent `foundry/` module and `foundry`/`foundry-chart` release targets; a separate book repository; the generated-only `contracts/` cross-module exception; owner-local test/E2E and affected-target release semantics; protected promotion with one founder approval; and merge-never-publishes. Privacy, licensing, redaction, custody, and trust policy decided here do not override the monorepo/publication or protected-release contracts. Semantic publication remains explicit affected-target intent. HOR-526 publication classification is **None**.

16. **Authority path summary.** Exact authorities are: control-plane for customer/tool/work/runtime/consequence/artifact identity and V2 identity; Foundry for campaign/dataset/evaluator reference/qualification/proposal/support/acceptance metadata and policy projection (with its own role-based action catalogue from `DES-HOR-525-01`); hidden-suite custodian for hidden/oracle/answer assets; evaluator for decision/report disclosure; trusted builder/broker for the inference lane per HOR-536; publisher for publication; overlay/release for production promotion; and customer/engagement acceptance authority for delivery. No store, worker, evaluator, publisher, broker, or proposal implementation occurs in HOR-526.

17. **Options considered and rejected.** Select: scoped immutable dataset/evidence custody; evaluator-owned hidden custody; immutable/blinded/re-baselined evaluators; typed idempotency/retry/`outcome_unknown`; separate publisher identities; simulator/scoped-non-production-only candidate endpoints; content-addressed proposals and append-only acceptance; and exact separated authorities. Reject: cross-tenant raw evidence or managed hosting; candidate-visible hidden sets or evaluator secrets; candidate-controlled evaluators/gates/thresholds/receipts; automatic retry of ambiguous/unknown writes; duplicate-discharge or duplicate-effect retry; publisher/overlay/Git/HF/OCI authority on any candidate or worker; candidate access to production/customer/publisher credentials; production-origin data feeding raw into prompts/training/weights automatically; unscoped customer-network access; qualification silently implying support/publication/deployment/acceptance/catalogue expansion; acceptance silently widening the reusable support catalogue; candidate self-registration as `ModelBackend`/gateway identity; and any implementation, evidence collection, spend, publication, deployment, or customer access within HOR-526.

18. **Consequences, compatibility, and evidence.** This creates future dataset/evidence custody and policy projection, protected hidden-set custody, evaluator disclosure rules, exact retry/recovery semantics, separate publisher identities, and coordinated control-plane+Foundry+charts+Forge trust validation. It preserves current control-plane identity/work/runtime/consequence/artifact authority, production-gateway trust, module independence, protected release semantics, and the exact eight-project product split. No data ingestion, candidate run, credential issuance, GPU execution, publication, deployment, acceptance, support declaration, or production mutation occurs in HOR-526. First-QDP implementation remains blocked on HOR-524/HOR-526/HOR-531/HOR-532 and fresh reshaping; HOR-533–HOR-536 retain their gates.

Durable evidence for the record will cite HOR-526; the founder’s approval; the Foundry programme/first-QDP PRDs, Product Direction, Architecture Direction, and Engineering Shaping Plan; `DES-HOR-525-01`; repository AGENTS ownership rules; `v2-authentication-authority.md`; and `docs/release.md`. Conversation memory alone will not be used as approval evidence: the final approval response, Linear record, and repository decision document will carry the approver/date/scope/consequences/options/evidence.

## 3. Requirement and architecture traceability

The tables below validate trust-boundary continuity. They do not approve a deferred policy, threshold, tool contract, customer-connectivity mechanism, or `InferenceRuntimeCandidate` mechanism.

### 3.1 Product direction and proposed architecture register

| Authority | DES-HOR-526-01 coverage | Remaining gate |
| --- | --- | --- |
| `FND-PD-007` | Items 8, 10, and 11 keep qualification, support, publication, deployment, and promotion separate. | Owning outcome decisions remain required. |
| `FND-PD-009` | Items 1–4 reserve immutable GitHub/HF/OCI/data references and scoped custody. | Exact data licences/policy gates remain outside. |
| `FND-PD-011` | Items 9, 10, 11, and 14 preserve whole-engagement trust without moving deployment/acceptance authority into candidates. | HOR-533/HOR-534 own exact mechanisms. |
| `FND-PD-012` | Items 8 and 11 keep candidate code isolated and non-self-promoting. | HOR-532 owns the exact tool trust mechanism. |
| `FND-PD-013` | Item 6 preserves typed uncertainty and consequence authority. | HOR-531 owns the exact v2 contract. |
| `FND-PD-014` | Items 10 and 11 keep qualification, support, publication, deployment, acceptance, and catalogue separate. | Owning outcome decisions remain required. |
| `FND-PD-015` | Item 15 preserves the first QDP as a forcing slice. | HOR-524/HOR-526/HOR-531/HOR-532 remain first-slice gates. |
| `FND-PD-016` | The complete bundle consumes the programme PRD as the superior lifecycle authority. | No product rescope is introduced. |
| `FND-PD-017` | Items 9–11 and 14 separate engagement deployment/acceptance from supported-profile governance. | HOR-533/HOR-534/HOR-535 remain. |
| `FND-PD-018` | Item 13 reserves shared inference trust separation without approving runtime/kernel mechanisms. | HOR-536 exclusively owns `FND-ARCH-019`–`021`. |
| `FND-ARCH-007` | Items 1–4 select scoped immutable dataset/evidence custody and evaluator-owned hidden custody. | Exact hidden-store topology remains implementation. |
| `FND-ARCH-008` | Item 6 selects typed idempotency/retry/`outcome_unknown`/reconciliation. | Exact operation/attempt/task wire details remain follow-on. |
| `FND-ARCH-009` | Item 5 selects immutable/blinded/re-baselined evaluators under separate authority. | Exact gate statistics remain HOR-524/HOR-526 scope. |
| `FND-ARCH-010` | Items 7 and 11 select separate publisher identities and reviewed promotion. | Exact publication mechanics remain HOR-536/release. |
| `FND-ARCH-013` | Items 8, 11, and 14 keep tool candidates isolated with no production gateway/customer authority. | HOR-532 owns exact trust-domain mechanics. |
| `FND-ARCH-016` | Items 7, 10, 11 select proposal-only promotion and separated authorities. | HOR-532/HOR-534 retain exact details. |
| `FND-ARCH-017` | Item 9 keeps customer-environment validation separately authorized and scoped. | HOR-533 owns exact connectivity/credential mechanics. |
| `FND-ARCH-018` | Items 10, 11 keep proposals/acceptance immutable and never-deploying. | HOR-534 owns exact persistence/approval mechanics. |
| Proposed `FND-ARCH-019`–`021` | Item 13 fixes shared trust separation only. | All exact build/lease/health/quarantine/recovery/evaluation/publication/identity mechanisms remain HOR-536. |

### 3.2 First-QDP requirements

| Requirement | Boundary continuity proved by this decision |
| --- | --- |
| `REQ-FND-QDP-009` | Items 3, 4 keep calibration/discovery/validation/hidden/security/production domains distinct with custody and leakage controls. |
| `REQ-FND-QDP-010` | Item 3 denies candidate access to hidden cases, expected states, evaluator internals, and hidden results during adaptive search. |
| `REQ-FND-QDP-011` | Items 4, 5 make programmatic final-state verification primary and model judges pinned/blinded/calibrated/secondary. |
| `REQ-FND-QDP-012` | Item 6 retains raw/typed evidence and prohibits pass-on-retry; exact repeated/paired comparison, uncertainty, and category-regression statistics remain deferred to the HOR-524/HOR-526 qualification gate. |
| `REQ-FND-QDP-013` | Item 6 retains failed/rejected/inconclusive attempts and prohibits pass-on-retry rewriting. |
| `REQ-FND-QDP-014` | Item 6 keeps campaigns durable, idempotent, resumable, and budgeted. |
| `REQ-FND-QDP-015` | Item 6 prevents duplicate effects or spend on duplicate delivery. |
| `REQ-FND-QDP-016` | Items 6, 12 keep cancellation fenced with honest effect retention. |
| `REQ-FND-QDP-017` | Item 8 limits adaptive work to isolated candidate lanes and Item 12 enforces budgets and plateau/budget stops; the exact first-loop prompt/skill-only mutation allowlist remains the approved first-QDP slice (HOR-524). |
| `REQ-FND-QDP-018` | Items 7, 8, 11 separate candidate, evaluator, coordinator, publisher, and promoter authority. |
| `REQ-FND-QDP-024` | Items 1, 10, 12 require content-addressed cell/evidence/decision references and immutable evidence policy; exact qualification-cell digest and evidence-policy mechanics remain HOR-524/HOR-526 implementation. |
| `REQ-FND-QDP-025` | Item 11 keeps qualification distinct from support/publication; exact QDP issuance-only-from-a-passing-decision and limitations/envelope/requalification-trigger mechanics remain HOR-524. |
| `REQ-FND-QDP-026` | Item 11 keeps `qualified`, `supported`, `published`, and `promoted` separate. |
| `REQ-FND-QDP-028` | Items 7, 10, 14 permit immutable proposals/references but no overlay, registry, or runtime mutation. |

### 3.3 Programme requirements

| Requirements | Boundary continuity proved by this decision |
| --- | --- |
| `REQ-FND-CAND-001` | Items 1, 10 require coupled immutable/content-addressed candidate evidence, including reference resolution before admission. |
| `REQ-FND-CAND-002` | Item 6 keeps attempts immutable with parent/rationale via receipts; Item 11 prevents any candidate from approving or deploying itself. Exact candidate-lineage persistence remains a follow-on implementation gate. |
| `REQ-FND-CAND-003` | Item 6 binds attempts to Foundry receipts and authoritative control-plane evidence. |
| `REQ-FND-CAND-004` | Items 6, 12 retain failed/rejected evidence and prohibit pass-on-retry rewriting. |
| `REQ-FND-CAND-005` | Item 1 requires aliases to resolve to immutable identities before plan/attempt identity is committed. |
| `REQ-FND-CAND-006` | Items 7, 10, 11 keep candidate/evaluation authority separate from merge, publication, support, deployment, and self-promotion. |
| `REQ-FND-TOOL-011` | Item 8 runs candidate code in a separate trust domain with simulators or scoped non-production resources. |
| `REQ-FND-TOOL-012` | Items 3, 5, 8 deny candidate access to hidden cases, evaluator changes, permission widening, or production credentials. |
| `REQ-FND-TOOL-013` | Items 7, 11 require trusted replay, human source review, and normal repository/release approval for promotion. |
| `REQ-FND-TOOL-014` | Items 2, 3, 12 treat tool-returned/content material as untrusted with preserved provenance and non-overwritable evidence; exact presentation/artifact-plane rules remain HOR-531. |
| `REQ-FND-RSCH-001` | Items 3, 4 partition calibration/discovery/validation/hidden/security/production data. |
| `REQ-FND-RSCH-002` | Items 3, 4 protect hidden cases, evaluator secrets, and final decision policy from researchers. |
| `REQ-FND-RSCH-003` | Items 3, 4, 8 deny candidate access to disallowed/hidden feedback and isolate candidate work from production mutation; the exact failure-classification/failure-router mechanics remain a follow-on gate. |
| `REQ-FND-RSCH-004` | Items 3, 4, 8 keep tool/environment defects from being trained around by denying candidate access to such loading feedback and production authority; exact owning-lane routing remains a follow-on gate. |
| `REQ-FND-RSCH-005` | Item 12 enforces declared mutation allowlist, protected surfaces, and budgets server-side; exact mutation-allowlist and protected-surface enforcement mechanics remain a follow-on gate. |
| `REQ-FND-RSCH-006` | Item 6 retains raw/typed evidence and Item 11 keeps selection authority separate; exact pinned-control and repeated/paired comparison statistics remain deferred to HOR-524. |
| `REQ-FND-RSCH-007` | Item 11 keeps selection/decision authority separate from candidate output; exact multi-objective visibility and no-scalar regression rules remain deferred to HOR-524. |
| `REQ-FND-RSCH-008` | Item 6 prohibits a retry turning a failed qualification into a pass without rewriting evidence. |
| `REQ-FND-RSCH-009` | Items 6, 12 keep campaigns durable, idempotent, cancellable, budgeted, and recoverable. |
| `REQ-FND-RSCH-010` | Items 7, 8, 11 keep selection trusted and prohibit self-selection, receipt falsification, or promotion invocation. |
| `REQ-FND-DEP-001` | Item 10 makes deployment output an immutable proposal and keeps release/deployment mutation external. |
| `REQ-FND-DEP-002` | Items 10, 14 require deployed artifacts to resolve to evaluated identities and digests. |
| `REQ-FND-DEP-003` | Item 15 lets approved changes enter owning Git/review/CI/release under existing authorities. |
| `REQ-FND-DEP-004` | Item 9 validates customer installation deltas rather than inferring them. |
| `REQ-FND-DEP-005` | Item 10 makes the DeploymentProposal an immutable identity bundle with no deployment effect; exact rollout/monitoring/rollback-identity recording remains HOR-534. |
| `REQ-FND-DEP-006` | Items 10, 14 verify exact running identities; merge/publication alone is insufficient. |
| `REQ-FND-DEP-007` | Items 7, 10, 11 separate qualification, support, publication, deployment, promotion, rollback, and acceptance. |
| `REQ-FND-ACC-001` | Item 10 freezes preregistered acceptance cases, verifiers, thresholds, and authority before execution. |
| `REQ-FND-ACC-002` | Item 9 exercises acceptance in the exact customer-controlled environment under a separately authorized boundary. |
| `REQ-FND-ACC-003` | Item 10 records an accepted/conditional/rejected decision with per-case/aggregate evidence, limitations, and obligations. |
| `REQ-FND-ACC-004` | Item 10 records an immutable acceptance/decision boundary without catalogue widening; exact support-handoff, monitoring, rollback, and owner mechanics remain HOR-534/HOR-535. |
| `REQ-FND-ACC-005` | Items 10, 11 keep customer acceptance from silently widening the reusable support catalogue. |
| `REQ-FND-GOV-001` | Items 8, 9, 14 give research workers only task-scoped inputs, bounded egress, and non-production credentials. |
| `REQ-FND-GOV-002` | Item 11 separates researcher, worker, evaluator, hidden-suite custodian, builder, publisher, deployer, qualifier, and approver so no candidate can approve or deploy itself. |
| `REQ-FND-GOV-003` | Items 2, 9 preserve tenant/identity/consent/purpose/retention scope through traces, artifacts, datasets, and evidence. |
| `REQ-FND-GOV-004` | Item 3 treats books, repos, notebooks, datasets, patches, tool results, browser content, and generated artifacts as untrusted inputs. |
| `REQ-FND-GOV-005` | Items 6, 12 make receipts/evidence append-only/content-addressed, attributable, redacted, and non-overwritable. |
| `REQ-FND-GOV-006` | Item 12 enforces budget and cancellation truth server-side. |
| `REQ-FND-GOV-007` | Items 14, 15 keep durable behavior in the Foundry service and Forge a thin client. |
| `REQ-FND-GOV-008` | Item 8 exposes no privileged generic executor and uses least-privilege declared workloads. |
| `REQ-FND-LEARN-001` | Items 2, 9 make production evidence eligible only after policy, scope, redaction/private retention, and immutable versioning. |
| `REQ-FND-LEARN-003` | Items 2, 4 keep raw daily usage from flowing directly into deployed prompts/tools/adapters/checkpoints/weights. |
| `REQ-FND-LEARN-004` | Items 1, 6, 10 preserve immutable candidate/attempt/reference lineage; exact coupled-variant tuple preservation remains a follow-on governed-production-learning gate. |
| `REQ-FND-LEARN-005` | Not decided here; shadow/sticky-canary/promotion/supersession/rollback remain later governed capabilities gated by the governed-production-learning outcome. |

### 3.4 Inference-runtime shared trust requirements

| Requirement | Shared trust owner fixed here | Mechanism retained by HOR-536 |
| --- | --- | --- |
| `REQ-FND-INF-011` | Foundry stores immutable candidate/reference lineage; candidate cannot forge receipts. | Exact digest and generated-state identity. |
| `REQ-FND-INF-012` | Campaign mutation allowlist and protected controls fixed; candidate cannot choose hidden cases/gates. | Exact mutation/protection enforcement. |
| `REQ-FND-INF-013` | Trusted builder is separate from untrusted candidate; candidate cannot acquire publisher credentials. | Reproducibility and AOT/JIT/generated-artifact contract. |
| `REQ-FND-INF-014` | Independent dispatch/fallback evidence is required; candidate logs/labels alone are not proof. | Dispatch/fallback instrumentation and gates. |
| `REQ-FND-INF-015` | Candidate cannot clear quarantine or control recovery; trusted broker/health authority is separate. | Lease/fence/health/reset/reboot/clearance. |
| `REQ-FND-INF-016` | Numerical/serving-semantic correctness precedes performance; candidate cannot decide. | Reference tolerances and serving gates. |
| `REQ-FND-INF-017` | Safety/cleanup gates are separate; candidate cannot hide faults. | Exact accelerator safety cases. |
| `REQ-FND-INF-018` | Envelope evidence is owned by Foundry; candidate cannot overclaim. | Exact envelope schema and fallback boundary. |
| `REQ-FND-INF-019` | Foundry blocks proposal/QDP/support on earlier failure; candidate cannot select itself. | Ordered evaluator implementations and gates. |
| `REQ-FND-INF-020` | Evidence is retained raw and independently; candidate cannot fabricate results. | Workload/metrics/memory/long-context/speculation mechanics. |
| `REQ-FND-INF-021` | Multi-objective Pareto review is separate from candidate; candidate cannot select. | Comparison/stopping/selection policy. |
| `REQ-FND-INF-022` | Foundry emits proposal only; candidate cannot choose destinations or use production/publisher/deployer/support credentials. | Trusted rebuild/publication/deployed-identity proof. |

## 4. Acceptance-scenario dry walk

### 4.1 First-QDP scenarios

| Scenario | Dry-walk result |
| --- | --- |
| `SCN-FND-QDP-001` | Unpinned book references fail validation before a campaign starts; every unresolved identity is identified. |
| `SCN-FND-QDP-005` | Duplicate submission and duplicate worker delivery reuse the same operation/attempt identity; no second attempt, repeat effect, or silent spend. |
| `SCN-FND-QDP-006` | Operator disconnect/reconnect resumes from durable cursors with re-introspection; no local state or replay. |
| `SCN-FND-QDP-007` | Coordinator restart, worker loss, evaluator failure, and finalization failure each produce typed durable state; unsafe work is not silently retried. |
| `SCN-FND-QDP-008` | The adaptive researcher uses development feedback but cannot retrieve hidden cases, answer-bearing traces, or hidden aggregate results. |
| `SCN-FND-QDP-009` | A broken tool, invalid credential, flaky reset, and incorrect evaluator are each classified and routed without training around the defect. |
| `SCN-FND-QDP-010` | Bounded prompt/skill loop creates immutable children, retains rejected attempts, and stops on budget/plateau. |
| `SCN-FND-QDP-012` | A development winner that fails hidden qualification receives a failed decision and no QDP; evidence is retained. |

### 4.2 Programme scenarios

| Scenario | Dry-walk result |
| --- | --- |
| `SCN-FND-TOOL-003` | A write that may have completed and then throws/times out/is cancelled/loses stream stays `outcome_unknown`, is not automatically repeated, and exposes reconciliation evidence without rewriting original state. |
| `SCN-FND-TOOL-004` | Candidate tool code attempting hidden cases, credentials, Kubernetes, Docker socket, customer networks, or publisher tokens is blocked and the security failure retained. |
| `SCN-FND-RSCH-001` | A bounded campaign mutates only allowlisted surfaces, cannot access hidden qualification, and stops on its preregistered budget/plateau. |
| `SCN-FND-RSCH-002` | A visible-set winner that fails hidden qualification yields no QDP or deployment claim. |
| `SCN-FND-DEP-001` | Foundry emits an exact deployment and rollback proposal but cannot change Git, registries, Linear, HF, OCI, or the target cluster. |
| `SCN-FND-DEP-002` | The deployed customer configuration does not resolve to the evaluated digests; acceptance stops and the mismatch becomes a qualification delta. |
| `SCN-FND-ACC-001` | The exact deployed configuration runs preregistered cases/traffic in the customer environment, producing an accepted/conditional/rejected decision with retained business-state and SLO evidence. |
| `SCN-FND-ACC-002` | A customer accepts an engagement-specific limitation; Foundry records the decision without silently widening the reusable QDP/support catalogue. |
| `SCN-FND-LEARN-001` | Eligible production evidence is scoped, redacted or privately retained, verified, versioned, and used only to create an offline candidate that must pass the normal gates. |

### 4.3 Inference-runtime scenarios, trust separation only

| Scenario | Trust-boundary continuity | Still unapproved |
| --- | --- | --- |
| `SCN-FND-INF-003` | Candidate cannot forge build/dispatch receipts; trusted build and independent dispatch evidence are separate from candidate. | Exact reproducibility and dispatch proof. |
| `SCN-FND-INF-004` | A faster incorrect/unsafe candidate is rejected before integrated performance; the failure is retained and candidate cannot conceal it. | Correctness/safety gates. |
| `SCN-FND-INF-005` | Faulted or uncertain accelerator capacity is quarantined; candidate cannot clear it or force retry. | Reset/reboot/clearance mechanism. |
| `SCN-FND-INF-006` | Supported cases pass and unsupported cases fail closed or use the exact declared fallback; candidate cannot overclaim the envelope. | Envelope matrix/fallback mechanics. |
| `SCN-FND-INF-007` | A microbenchmark winner that fails later gates produces no profile/QDP/support/publication/deployment claim. | Evaluation ladder implementations. |
| `SCN-FND-INF-008` | Claimed multimodal/long-context evidence proves real residency and accepted tokens; draft/config-limited/single-sequence claims fail. | Workload/long-context/speculation gates. |
| `SCN-FND-INF-009` | Trusted rebuild/replay/deployed executable differing from evaluated identity stops promotion/acceptance and fails closed. | Trusted rebuild/deployed-identity proof. |

## 5. Authority and trust-boundary summary

| Boundary | Authority | Candidate/worker overlap |
| --- | --- | --- |
| Customer/tenant/work/runtime/consequence/artifact identity | Control-plane | None |
| V2 human/service identity and action decisions | Control-plane | None |
| Campaign/dataset/evaluator-reference/qualification/proposal/support/acceptance metadata | Foundry | None |
| Hidden cases/oracles/evaluator assets | Hidden-suite custodian | None |
| Evaluator decision/report disclosure | Evaluator | None |
| Publication (Git/HF/OCI/artifact/overlay) | Separate publisher | None |
| Production promotion/release | Overlay/protected release | None |
| Customer deployment/acceptance | Engagement acceptance authority | None |
| Inference build/lease/health/quarantine/recovery | Trusted builder/broker (mechanisms HOR-536) | Candidate has none |

No single owner in the row above may let a candidate approve or deploy itself.

## 6. Failure, recovery, and authority validation

| Case | Required convergence/evidence | Owning boundary |
| --- | --- | --- |
| Duplicate delivery/effect | Same key+digest converges; no duplicate effect or spend; started effects never replayed. | Foundry + control-plane idempotency. |
| Compromised trusted authority | A compromise of a trusted evaluator/custodian/builder/broker/publisher/qualifier/support authority cannot change its decisions, hidden set, receipts, or promotion; decisions fail closed to the next independent authority and evidence reflects the last trusted state. | Trusted authorities rotate independently; Foundry + control-plane audit. |
| Partial publication/deployment | A partially completed external operation is never reported as full success; convergence/rollback evidence is retained and the owning authority owns the outcome without claim of undo. | Foundry proposal/engagement + external publisher/deployment. |
| Revoked credential/reference | A revoked credential, identity, or reference is re-validated and denied at every downstream use; prior evidence is not silently re-validated or reused. | Control-plane V2 identity + Foundry policy projection. |
| Stale identity across the chain | A stale candidate/source/artifact/publisher/deployment/acceptance reference fails closed and triggers a fresh explicit requalification or new candidate; no borrowed near-cell evidence. | Foundry reference/evidence + release/overlay authorities. |
| Ambiguous external effect | Retain `outcome_unknown`; no automatic retry/undo/failure inference. | Owning effect/ledger authority. |
| Worker/evaluator/publisher/coordinator loss | Typed durable state; no unsafe retry or fabricated success. | Foundry + broker. |
| Hidden-set leakage attempt | Candidate cannot read hidden cases/answers/evaluator secrets; failure retained. | Hidden-suite custodian. |
| Evaluator/gate mutation | Candidate cannot alter evaluators, gates, thresholds, or acceptance criteria. | Evaluator/qualification authority. |
| Receipt/evidence tampering | Append-only/content-addressed; candidate cannot overwrite. | Foundry evidence. |
| Unauthorized publication/deployment | Proposal/approval records create no Git/registry/release/overlay/cluster/support change. | Foundry + external authorities. |
| Production-origin data misuse | Raw usage never auto-flows into prompts/training/weights; export is separately authorized. | Control-plane export + Foundry policy. |
| Customer-credential/customer-network misuse | Candidates get no customer credentials or broad customer network access. | HOR-533 seam + worker authority. |
| Inference receipt/quarantine forgery | Candidate cannot forge build/evaluation/health receipts or clear quarantine. | Trusted builder/broker (HOR-536). |
| Candidate self-registration | Candidate never registers/routes as `ModelBackend` or gateway identity. | Control-plane + inference-gateway. |
| Deployment identity drift | Stop acceptance; mismatch becomes an explicit new delta/requalification input. | Foundry engagement references. |
| Acceptance catalogue widening | Customer acceptance never changes QDP/support state automatically. | Foundry engagement-delivery. |
| Statistical retry | Earlier failed/rejected/inconclusive attempt remains immutable and visible. | Foundry qualification; exact policy HOR-524/HOR-526. |

## 7. Follow-on gate summary

- **HOR-524:** first-QDP thresholds, workload, budget, and support decision.
- **HOR-531:** Tool Contract v2, SDK, result/certainty, and effect-observation contract.
- **HOR-532:** ToolCandidate trust domain and trusted promotion boundary.
- **HOR-533:** customer-environment connectivity and credential brokerage.
- **HOR-534:** deployment-proposal and customer-acceptance persistence/authority mechanisms.
- **HOR-535:** initial exact coverage graph and supported-profile governance mechanisms.
- **HOR-536:** all `InferenceRuntimeCandidate` build, accelerator lease/health/quarantine/recovery, evaluation, trusted-rebuild, and deployed-identity-proof mechanisms.

No deferred implementation candidate becomes engineering-ready merely because `DES-HOR-526-01` fixes its trust boundary. Each follow-on must re-read repository state, obtain its own required decisions, and preserve the independent module, owner-local test, affected-target release, protected promotion, and evidence-custody contracts in this record.
