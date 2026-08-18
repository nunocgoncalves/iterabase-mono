# HOR-482 atomic source and E2E foundation acceptance — 2026-08-18

- **Ticket:** [HOR-482](https://linear.app/horizonshift/issue/HOR-482)
- **Product source:** *Atomic Platform Delivery and Pre-Production Confidence — Engineering Plan*
- **Accepted foundation source:** `ce8ca3103605ace9998a6ffbf52336c95836b431` (`master` after HOR-497)
- **Decision:** **Accept.** The atomic source, deterministic E2E, HOR-438 recovery, and protected release foundations have no remaining product or release blocker.
- **HOR-482 semantic publication:** **None.** This ticket records evidence and corrects historical verification guidance; it changes no semantic artifact.

## Executive result

Every approved foundation deliverable and blocker preceding HOR-482 is Done. The compiled owner catalogue contains 15 required F2/F3 scenarios exactly once (`charts=8`, `control-plane=5`, `forge=2`). Complete clean source runs passed all 15, including mandatory CPU/GPU capacity and the representative `emptyDir` driver transition, with no automatic scenario retry or accepted flake.

HOR-438 has durable red-before/green-after evidence on the repository-owned `deployed-execution-contracts` scenario. Its exact affected-target candidate passed the complete relevant candidate union, founder-approved promotion published the tested artifacts without rebuild, and OPO1 recovered the same AgentPool after only its missing Secrets were created. Gateway pool, grant, and binding rows materialized atomically before Ready; the bound workflow discovered and invoked its tool.

The security blocker found by the complete run, HOR-497, is also fixed, candidate-validated, founder-promoted, and accepted at the final foundation source. Four remaining material findings are outside the approved blocking categories and remain Todo with explicit deferral decisions. The Platform V2 implementation dependency gate may be released, but this decision does not claim that any future V2 behavior is implemented or tested.

## Delivery and dependency audit

The accepted project sequence is complete:

| Foundation slice | Completed authority |
| --- | --- |
| Exact history, module/workspace boundaries, path-aware CI, release foundation, and source cutover | HOR-472, HOR-471, HOR-470, HOR-473, HOR-474 |
| One-time assertion audit and compiled shared test mechanics | HOR-479, HOR-476 |
| Fresh integration/isolation and real-GPU regression gates | HOR-484, HOR-485 |
| Chart owner migration and lifecycle coverage | HOR-416, HOR-475 |
| Control-plane identity/work/artifact/execution/browser coverage | HOR-478, HOR-477, HOR-483 |
| Forge ownership cleanup and complete scheduled catalogue | HOR-481, HOR-486 |
| Complete clean run, defect triage, and HOR-438 repair/rollout | HOR-480, HOR-438 |
| Complete-run security remediation and exact publication | HOR-497 |

The other defects discovered while completing HOR-480 were resolved under HOR-487 through HOR-496 and HOR-502, or explicitly deferred below. The accepted HOR-438 and HOR-497 evidence was reconciled with Linear by completing both acceptance workflows and moving both issues to Done. No dependency relation on HOR-482 remains incomplete.

## Source, history, workspace, and cutover

[`history-import.md`](../history-import.md) records each unchanged source head, relocation commit, and unrelated-history merge:

| Destination | Preserved source head | Relocation | Merge |
| --- | --- | --- | --- |
| `control-plane/` | `c63eea9d21c367a3e5fd91431bedc853fb15a16b` | `cbed62e1596aeee913e00afe4b46a5b3d4ead874` | `1997552a87cb8a1feeff472bbb3c4d4744aedfae` |
| `inference-gateway/` | `cf093df2cdca30e916cb340d3e5dc1ab29c49989` | `47acdd90cff468bc921456b87f95032c57c87f89` | `57eeee901d57f5188cd4b6836d3339c51688530d` |
| `forge/` | `56afae7b21f97a1c40c81705954756ef16f46674` | `bcf52a3bb088eb1ae06e78951e4721970ee32269` | `627c2e02baacd384b5bb870d369b3222b6e0a639` |
| `charts/` | `0d97d50962afcd03aa474f096a8948f0e1dcd8b5` | `f609428b137a486c04da4bdca45159f43abc3f3b` | `7579abd5434f0cebba07cdb6da99037015c74cec` |

The HOR-482 audit rechecked every source object and current ancestry, direct relocation parent, two-parent merge shape, exact source-tree identity at the relocation commit, and representative `log --follow`/blame continuity. `git fsck --full --no-dangling` passed and no raw `v*` tag exists. The historical guide now compares source trees at their relocation fixed point rather than incorrectly comparing them with a later modified component tip.

`make workspace-check` passed every independent module: control-plane, inference-gateway, Forge, Forge E2E, shared testkit, control-plane E2E, and charts E2E. Root and owner `AGENTS.md` boundaries preserve scoped context. Product modules remain independent; the approved nested owner E2E modules consume the shared testkit without merging component modules.

The artifact-backed archived-state audit passed with `CHECK_ARTIFACTS=true`. `iterabase-mono` remains the sole writable public product source; all four legacy repositories remain archived at the exact heads above with repository Actions, authored workflows, Dependabot writers, and Actions secrets disabled. Historical commits, PRs, releases, GHCR images, and chart artifacts remain accessible. Overlays remain independent repositories consuming unchanged immutable artifact names.

## CI, cache, and release controls

Live and local audits established the following:

- `master` strictly requires the stable `CI / required` and `E2E / required` contexts. Selected owner failures, cancellations, or missing prerequisites fail their aggregate.
- CI selection fixtures passed seven cases covering docs-only, component, shared-contract, deletion, and cross-owner behavior. Shared workspace/testkit/selector changes fan out; release-only changes remain focused.
- Full control-plane Go tests use `-count=1`, and the selected harness gate executes real Linux UID/process/filesystem isolation. Restored build caches cannot substitute Go test results or a merely built isolation image.
- Cache keys include exact tool/runtime and dependency authority without fallback restore keys. Kind clusters, databases, mutable fixtures, test results, credentials, customer data, and release evidence are never cached; cold-cache correctness remains mandatory.
- All 36 release contract fixtures and repository release validation passed. Candidate intent must be explicit and non-empty, exact source must be contained in `master`, selected targets build once, unselected dependencies are immutable baselines, and compiled metadata selects the deduplicated required suite.
- `make release-security-audit` passed against the live protected `release` environment, release tag ruleset `20699187`, and sole write deploy key `iterabase protected release tags (validated)`.
- Merging or pushing a raw tag cannot publish. Candidate and promotion are separate manual workflows; promotion verifies the successful candidate, waits for founder approval once, preflights every destination, and publishes exact candidate bytes without rebuild.

The release system was commissioned at source `a9bd171a1d3f63d361846edf86fa5eab049720b0`: rehearsal `31727406655`, all-six-target candidate `31727479627`, protected promotion `31728824116`, and idempotent resume `31729156506` all passed. This proves image, chart, Forge matrix, dependency-heavy platform chart, permission, protected-tag, and resumable exact-publication behavior without claiming cross-provider transactionality.

## Audit, catalogue, and complete-suite evidence

[`AUDIT-2026-08.md`](AUDIT-2026-08.md) remains the one-time migration inventory; [`STRATEGY.md`](STRATEGY.md) and compiled `TestE2E` registrations are current authority. Product and lifecycle assertions remain with control-plane, charts, and Forge; `testkit/e2e` owns mechanics only. Forge's replaced chart/product Kind checks were removed only after their owner replacements were green.

At final source `ce8ca3103605ace9998a6ffbf52336c95836b431`, `make e2e-catalogue` compiled:

| Owner | Required tier | Count |
| --- | --- | ---: |
| Charts | F2 | 8 |
| Control-plane/browser | F2 | 5 |
| Forge CPU/GPU | F3 | 2 |
| **Total** |  | **15** |

`make testkit-test` passed race-enabled shared mechanics, all owner examples/unit fixtures, and JSON/Markdown catalogue generation at the same source.

Durable execution evidence:

| Run | Exact source / mode | Evidence | Result |
| --- | --- | --- | --- |
| [`32082730427`](https://github.com/nunocgoncalves/iterabase-mono/actions/runs/32082730427) | `7c6f835deb01e5d265eb7f9bd2bdcac70d2f62bf`, source | Scheduled/manual complete-catalogue rehearsal selected all 15 exactly once; mandatory CPU/GPU ran | Pass |
| [`32085802020`](https://github.com/nunocgoncalves/iterabase-mono/actions/runs/32085802020) | `7c6f835deb01e5d265eb7f9bd2bdcac70d2f62bf`, source | Clean pre-demo baseline, 13 Kind/browser plus mandatory CPU/GPU | Pass |
| [`32087107192`](https://github.com/nunocgoncalves/iterabase-mono/actions/runs/32087107192) | `2ddf8cc300d6687e881651e706d00180edd53408`, source | Browser setup stalled on an Ubuntu mirror before scenario provisioning; retained as HOR-502 | Fail, honestly retained |
| [`32090806323`](https://github.com/nunocgoncalves/iterabase-mono/actions/runs/32090806323) | same `2ddf8cc300d6687e881651e706d00180edd53408`, source | Separate post-triage clean run selected and passed all 15; no failed job was rerun | Pass |
| [`31953550536`](https://github.com/nunocgoncalves/iterabase-mono/actions/runs/31953550536) | `7cf6723b4d093bf30207e62d5f09c4a66367245a`, candidate | HOR-438 bundle: all 8 chart scenarios, 7 then-current exact-candidate Kind contracts, mandatory CPU/GPU, owner checks, immutable evidence | Pass |
| [`32096362453`](https://github.com/nunocgoncalves/iterabase-mono/actions/runs/32096362453) | `ce8ca3103605ace9998a6ffbf52336c95836b431`, candidate | Final control-plane images, complete owner checks, all 5 control-plane scenarios plus chart internal TLS | Pass |

The retained release-candidate artifacts for runs `31953550536` and `32096362453` were downloaded during HOR-482 and independently passed `release.py verify-candidate`. The first binds HOR-438's exact affected-target bundle and mandatory real-machine evidence; the second proves the late-Secret recovery stage remains green in the final accepted control-plane candidate after HOR-497.

No required scenario has a known flake, automatic retry, pass-on-retry result, silent capacity skip, or accepted assertion failure. HOR-502 preserves the infrastructure failure rather than reclassifying it.

## HOR-438 red-before/green-after and exact publication

The final one-CR `control-plane/deployed-execution-contracts` fixture captured the pre-fix failure before the repair:

```text
missing-Secret dependency advanced readiness/observed generation: "<same-uid>|1||1|validation: caSecretRef: secret iterabase-system/late-platform-ca not found"
```

That is the precise broken sequence: generation 1 was observed after missing-CA validation, no gateway state had materialized, and the generation gate could suppress every later materialization attempt.

With commit `3d57e99` (merged as `1b64982` and released from source `7cf6723b4d093bf30207e62d5f09c4a66367245a`), the same fixture proved:

- structural validation completes before external dependency checks;
- absent CA and credential Secrets leave `observedGeneration` unadvanced, Ready false, and gateway rows absent;
- creating only those Secrets and waiting for the existing uncached 30-second cadence recovers the original CR UID and generation without recreation or mutation;
- pool, grant, and binding rows materialize atomically as `1|1|1` before worker Ready;
- the bound workflow discovers and invokes its declared read tool, while an ungranted write capability remains absent and fails closed;
- focused controller tests preserve valid-pool idempotency and make exactly one post-dependency materialization call.

Candidate `31953550536` bound control-plane `0.0.27`, control-plane chart `0.4.9`, and platform/substrate charts `0.3.11` to exact tested artifacts. Founder-approved promotion [`31954803774`](https://github.com/nunocgoncalves/iterabase-mono/actions/runs/31954803774) published without rebuild. Protected tag `control-plane-v0.0.27` peels to the exact source. Relevant image digests are:

- control-plane: `sha256:f3f3cbf372efb9fcc9f3c2d7d6a91fc2eab344797dab05408a9ee765c5688977`
- harness: `sha256:24e1af0848c698f26fcac7c6d34e8c4fd197fa0f734ba9d48c9ff4de88bbc184`
- tool-runner: `sha256:25f98a073b6ac093b6dfe907348a19c12ecb82d616a8489e3c686649a349cbe5`

## OPO1 confirmation

Accepted HOR-438 production evidence records OPO1 overlay merge `9d1da62ed6e7405c2861f79cc3bc65f0557f2f0e`, platform/substrate chart `0.3.11`, and the exact released control-plane/tool-runner/harness digests above. OPO1 remained an independent deployment repository; no overlay behavior was copied into this source tree.

The live proof created AgentPool UID `86d9be53-4812-485d-8909-204dc432b52a`, generation `1`, with both declared Secrets absent. It initially showed `observedGeneration=0`, Ready false, and zero gateway pool rows. Creating only the CA and credential Secrets recovered that same UID/generation/spec digest, atomically produced `1|1|1` pool/grant/binding rows, and made the worker Ready.

Authenticated `manual_api` work item `a8f8eee9-0967-4d11-a4ba-a22aa3342391`, attempt `df430555-55ba-4451-a684-230638535f86`, discovered and invoked `platform.validation.echo`; its durable invocation ledger succeeded. Existing `general-pool` remained Ready through staged upgrade and idempotent final reapply, preserving the valid-pool path. Temporary resources and keys were cleaned up; non-secret durable evidence remains. There is no OPO1 acceptance exception.

## Final security release evidence

HOR-480 found the production harness dependency blocker HOR-497. The complete clean source run passed after its fix, then final source `ce8ca3103605ace9998a6ffbf52336c95836b431` received a separately approved exact `control-plane` `0.0.29` candidate and protected promotion:

- candidate [`32096362453`](https://github.com/nunocgoncalves/iterabase-mono/actions/runs/32096362453);
- founder-approved no-rebuild promotion [`32097287450`](https://github.com/nunocgoncalves/iterabase-mono/actions/runs/32097287450);
- protected release [`control-plane-v0.0.29`](https://github.com/nunocgoncalves/iterabase-mono/releases/tag/control-plane-v0.0.29), whose tag peels to the exact source;
- harness digest `sha256:26b1f6909cfa9f8801842d8045c20c6eb1dbda7a8b2c3752dbc1c27bb2c7eae2`, with zero production npm audit findings.

This closes the sole security/data-integrity blocker found by the complete pre-demo run. Publication changed immutable release identities only and triggered no overlay deployment.

## Acceptance criteria disposition

| Criterion | Disposition |
| --- | --- |
| All project deliverables and mandatory suite evidence are green with no known required flake | **Pass.** Every prerequisite is Done; complete source, nightly, exact-candidate, CPU, and GPU evidence above is green. |
| HOR-438 has durable red-before/green-after evidence on the merged framework | **Pass.** Exact pre-fix generation failure and same-fixture green behavior are recorded above. |
| OPO1 recovers late Secrets, materializes gateway state before Ready, and discovers/invokes the tool without CR recreation | **Pass.** Same UID/generation, atomic `1|1|1`, Ready gating, and durable invocation are recorded. |
| Promotion remains founder-gated and publishes only exact tested artifacts | **Pass.** Live security audit and HOR-438/HOR-497 candidate/promotion pairs prove the protected no-rebuild path. |
| Legacy sources are archived and overlays remain independent/operational | **Pass.** Artifact-backed archived-state audit passed; stable artifact identities and external overlay ownership are unchanged. |
| Remaining defects are resolved or explicitly deferred outside blocking categories | **Pass.** Resolved blockers are Done; the four accepted deferrals below have owners and evidence. |
| V2 implementation can unblock without claiming future behavior is tested | **Pass.** This foundation validates source/test/release mechanics and current v1 recovery only; V2 tickets retain their own requirements and acceptance. |

## Remaining deferrals and exceptions

The approved defect policy permits these material non-blockers to remain open as Todo issues with explicit deferral decisions:

- **HOR-498:** development/build-only Nano ID advisory; absent from the production harness install.
- **HOR-499:** test-only Moby advisories; no production archive/import path.
- **HOR-500:** first-party Action metadata still targets deprecated Node 20 while current GitHub execution succeeds on forced Node 24.
- **HOR-501:** cel-go advisory is linked but its vulnerable API is unreachable from the secured metrics path.

HOR-502 is resolved and retained only as honest infrastructure-incident evidence. No security/data-integrity, consequence/idempotency, XBS-path, or required deployment/recovery blocker remains.

Several completed delivery tickets retain founder-approved review-protocol exceptions for omitted zero-finding terminal markers. Those records waive no product assertion, acceptance criterion, required CI, candidate, promotion, or deployment evidence. HOR-480 and HOR-438 have no acceptance exception, and HOR-482 introduces none.

## HOR-482 validation and production impact

The final audit ran:

```text
# Imported object/ancestry/relocation/merge/tree/log-follow/blame checks for all four sources
git fsck --full --no-dangling
CHECK_ARTIFACTS=true make source-authority-audit SOURCE_AUTHORITY_STATE=archived
make check
make workspace-check
make testkit-test
make e2e-catalogue
make release-check
python3 .github/scripts/test_select_ci.py
make release-security-audit
python3 .github/scripts/release.py verify-candidate --directory <run-31953550536-artifact>
python3 .github/scripts/release.py verify-candidate --directory <run-32096362453-artifact>
```

All passed. The exact run and artifact evidence above was also checked against GitHub and Linear acceptance records.

HOR-482 changes documentation only. It causes no runtime, migration, chart, image, release, or overlay change. Rollback is a normal source revert. Semantic publication is **None**.
