# HOR-480 pre-demo quality report — 2026-08-18

- **Ticket:** [HOR-480](https://linear.app/horizonshift/issue/HOR-480)
- **Product source:** *Atomic Platform Delivery and Pre-Production Confidence — Engineering Plan*
- **Baseline source:** `7c6f835deb01e5d265eb7f9bd2bdcac70d2f62bf` (`master` after HOR-486)
- **Remediated source:** `2ddf8cc300d6687e881651e706d00180edd53408` (HOR-497 on the HOR-480 branch)
- **Decision at HOR-480 review:** deterministic source validation is green; the pre-demo release decision remains **blocked only on HOR-497's merge, exact control-plane candidate, protected publication, and acceptance**.
- **Final disposition:** HOR-497 subsequently completed; [`FOUNDATION-ACCEPTANCE-2026-08.md`](FOUNDATION-ACCEPTANCE-2026-08.md) records the final HOR-482 foundation decision while this report preserves the earlier quality-gate evidence.

## Executive result

The complete compiled F2/F3 catalogue passed from clean fixtures twice: first on the exact master baseline and then on the remediated HOR-480 branch. Both successful plans selected 15 required scenarios exactly once (`charts=8`, `control-plane=5`, `forge=2`) in explicit `source` mode. Both mandatory DigitalOcean CPU/GPU scenarios ran rather than skipping, including the `emptyDir` GPU driver transition. No scenario used automatic retry or pass-on-retry state.

The first post-remediation dispatch remains recorded as a failed infrastructure incident: Playwright dependency installation hung on the GitHub runner's Ubuntu mirror before Kind/browser provisioning and hit the job bound. It was triaged as HOR-502 before one new complete clean run was dispatched; the failed run was not rerun or overwritten.

Security inspection found a real production harness dependency blocker. HOR-497 upgrades Pi and its pinned vulnerable runtime dependencies while preserving the credentialless IPC model boundary. The source fix passes the full catalogue and repository matrix, but it changes the harness image. HOR-480 therefore cannot make a final demo/release decision until HOR-497 is merged, validated as an exact control-plane candidate, promoted without rebuild through founder approval, and accepted. No release was dispatched or promoted by HOR-480 validation.

## Compiled catalogue and audit reconciliation

`make e2e-catalogue` and the retained plans agree on the following current authority:

| Owner | Required scenarios | Result on baseline | Result after remediation |
| --- | ---: | --- | --- |
| Charts | 8 F2 | Pass | Pass |
| Control-plane/browser | 5 F2 | Pass | Pass |
| Forge | 2 mandatory F3 | Pass | Pass |
| **Total** | **15** | **Pass** | **Pass** |

Scenario IDs:

- Charts: `certificate-ownership-migration`, `feature-enable-upgrade`, `fresh-install`, `internal-tls`, `n-minus-one-upgrade`, `observability`, `observability-tls`, `reapply-rollback-recovery`.
- Control-plane: `deployed-artifact-durability`, `deployed-browser-journeys`, `deployed-execution-contracts`, `deployed-identity-api`, `deployed-work-recovery`.
- Forge: `digitalocean-cpu`, `digitalocean-gpu` (mandatory named capacity).

The one-time audit in [`AUDIT-2026-08.md`](AUDIT-2026-08.md) remains historical rather than a second catalogue. Reconciliation found no silent loss:

- Forge's former direct-chart `kind-controlplane-identity` maps to control-plane `deployed-identity-api`.
- Forge's former inference and tool-runner Kind contracts map to `deployed-execution-contracts`.
- Certificate, TLS, observability, installation, upgrade, feature-enable, reapply, and rollback authority maps to the eight chart scenarios.
- Forge retains the CPU/GPU substrate assertions, including apply/migration/idempotency/Flux/secret handling, cleanup, GPU readiness, driver transition, and dependent serving smoke.
- Component F0/F1, chart static, harness isolation, and owner tests remain required under `make check`; migration to the F2/F3 catalogue did not delete their authority.

## Clean-run evidence

### Exact master baseline

[Complete-catalogue run 32085802020](https://github.com/nunocgoncalves/iterabase-mono/actions/runs/32085802020) passed on exact source `7c6f835deb01e5d265eb7f9bd2bdcac70d2f62bf`.

- Retained plan: schema 1, `fixture_mode=source`, 15 scenarios, owner totals `8/5/2`.
- All 13 Kind/browser jobs passed.
- Mandatory CPU and GPU jobs passed; missing credentials/capacity did not skip.
- `E2E / required` passed.
- No failure-diagnostics artifact was produced.

### First post-remediation run: retained infrastructure incident

[Run 32087107192](https://github.com/nunocgoncalves/iterabase-mono/actions/runs/32087107192) used exact source `2ddf8cc300d6687e881651e706d00180edd53408` and selected the same 15 scenarios. Fourteen owner scenarios, including CPU/GPU and `deployed-execution-contracts`, passed.

`deployed-browser-journeys` never reached its scenario. At `01:17:04Z`, `playwright install --with-deps chromium` entered apt setup; Azure Ubuntu indexes were unavailable, fallback `archive.ubuntu.com` fetched only InRelease metadata, and apt then produced no progress until the 50-minute job bound canceled it at `02:06:38Z`. No cluster diagnostics existed because Kind had not been created. The required aggregate failed honestly. HOR-502 owns the incident and preserves its logs/timestamps.

### Triaged clean rerun

After HOR-502 was recorded, [complete-catalogue run 32090806323](https://github.com/nunocgoncalves/iterabase-mono/actions/runs/32090806323) ran from the same exact remediated SHA.

- The retained plan again selected all 15 scenarios exactly once in source mode.
- The browser job completed in 5m13s and passed the real Chromium journey once with no scenario retry.
- All other chart/control-plane jobs passed.
- Mandatory CPU passed in 11m18s and GPU passed in 29m03s.
- `E2E / required` passed.

This clean run is separate post-triage evidence; it does not relabel run 32087107192 as passing.

## Candidate and immutable artifact evidence

The retained candidate records for the currently published pre-remediation artifacts were downloaded and independently rechecked with `python3 .github/scripts/release.py verify-candidate --directory ...`:

| Candidate | Exact source and selected targets | Relevant immutable evidence | Result |
| --- | --- | --- | --- |
| [31953550536](https://github.com/nunocgoncalves/iterabase-mono/actions/runs/31953550536) | `7cf6723b4d093bf30207e62d5f09c4a66367245a`; control-plane `0.0.27`, control-plane chart `0.4.9`, platform chart `0.3.11` | Plan SHA-256 `6e1fc8b26b336ac2a7149da8417bf0ea3f162ee58df0cfbd038288421b936e9d`; control-plane chart `fea58c293eac6b8c3293af0864c99c266177e2e6dbb06052c8b0af080b9fef12`; platform `0709ad01d6170a7bd6433e7e95c3872999000ed7e9475b1bd915b8f0bf77a406`; substrate `2074fc3f033ef34593a6ce8c9334bd2d86865bc8c0621519b46817abfc6f20e2` | Pass |
| [32027902362](https://github.com/nunocgoncalves/iterabase-mono/actions/runs/32027902362) | `6f37ad120f1d0e54a6f9c31ffc85ee8543a9b692`; control-plane `0.0.28`, Forge `0.8.3` | Plan SHA-256 `4bcdbddbdbc1bab84100566ddd3fe6eb19146783a6ba2b48c7a2698118ca02eb`; control-plane `sha256:030a411fc8ff87b7a41acdb31a35380779a2175b94038a644d790b208d7f1a87`; harness `sha256:dd676cffd0577f9035cef7513be25ff9441ca6e39044a94bc830344529e6ad4a`; tool-runner `sha256:9826b658be2da54b02c484da401a48ba0bfc9185f59a1a737f310c4702a634f4`; published chart checksums above; inference baseline `0.2.5@sha256:088f2523940bd547a97dbc59e22b5664e8946dca22eeedcf10fa71593897cda9`; transition baselines checksum-pinned at `0.3.10` | Pass |
| [32071137289](https://github.com/nunocgoncalves/iterabase-mono/actions/runs/32071137289) | `34476dd86b0824c71f2412ed6ce2f27174fb2bd6`; Forge `0.8.4` | Plan SHA-256 `157790fd6c48dbfb35c8c5e99e14aead46ce760da46062152b8c64575347afd7`; archive SHA-256: Darwin amd64 `f535d79a1b519af3608a645b4da0370f7eaabf4c9e94cfbaecb663623396268f`, Darwin arm64 `19a3aae9f4a5f3dc30f33a41f5dfd55ff34dded5115936cbb1f365b0deb2f194`, Linux amd64 `9d5c22b93464eb847ab047220e57c53c3d33447110eef03196ba090ebda6ec2d`, Linux arm64 `7e85a99c25adff9c86d0d1327bb9115b8e541dd78ca86d17cfc26a1136dda891`; exact SBOMs; immutable published image/chart baselines | Pass |

A local plan generated from master for `control-plane,forge,control-plane-chart,iterabase-platform-chart` selected the complete current 15-scenario union, the exact chart transition checksums, and immutable inference-gateway `0.2.5` baseline identity. Those semantic versions are already protected/published from their earlier exact candidates, so the candidate preflight correctly refuses rebuilding them from a later SHA. HOR-497 changes the harness bytes and therefore requires a new control-plane version and a new exact post-merge candidate under `release-ticket`; prior control-plane candidate evidence cannot accept the remediation.

HOR-480 did not dispatch promotion, did not enter the protected release environment, and did not exercise founder approval. Existing publications above belong to their originating accepted tickets.

## Failure and risk triage

| Finding | Classification | Owner / decision |
| --- | --- | --- |
| Harness runtime included vulnerable `undici`, `brace-expansion`, and `protobufjs` through Pi's shrinkwrap | Security defect; pre-demo blocker | **HOR-497**. Source-fixed in `2ddf8cc300d6`; exact candidate, publication, and acceptance pending. |
| Browser setup hung against Ubuntu mirrors before scenario provisioning | Infrastructure incident | **HOR-502**. First run retained; subsequent complete clean run passed; resolved without retry/timeout/assertion changes. |
| Vite/PostCSS development trees retain the Nano ID zero-size-generator advisory | Build/dev dependency; no direct import or production dependency path | **HOR-498**, explicitly deferred. |
| Moby advisories are reachable only through golang-migrate's test dependency; no production Docker import/archive API path | Test dependency | **HOR-499**, explicitly deferred with trusted-image/no-archive mitigation. |
| cel-go advisory is linked through secure metrics authorization, but the vulnerable `NativeTypes(ParseStructTag("json"))` function is not called and metrics users cannot submit CEL expressions | Unreachable production transitive path | **HOR-501**, explicitly deferred pending a supported Kubernetes dependency update. |
| Pinned first-party Actions target deprecated Node 20 and GitHub forces Node 24 | CI maintenance risk; current jobs pass | **HOR-500**, explicitly deferred before compatibility forcing is removed. |

No E2E product/chart/Forge assertion failed. No required flake is accepted. No retry converted an assertion failure to green.

## Approved blocking categories

- **Security/data integrity:** blocked only by HOR-497's required exact publication/acceptance. The source remediation and production audit are green.
- **Consequence/idempotency:** `deployed-execution-contracts` and `deployed-work-recovery` passed on baseline and remediated source, including duplicate idempotency, outcome-unknown, exact consequence confirmation, concurrent starts, and recovery.
- **XBS path:** HOR-438 is Done with red-before/green-after, protected publication, and OPO1 late-Secret/tool evidence; its unchanged recovery scenario passed in both successful complete runs.
- **Required deployment/recovery:** all chart lifecycle scenarios and Forge CPU/GPU scenarios passed, including feature enablement, reapply/rollback/recovery, host migration/idempotency, and the `emptyDir` driver transition.

## Local validation

The complete repository matrix passed on both baseline and remediated source:

```text
make check
```

This includes workspace freshness, formatting, vet, production builds, testkit/catalogue checks, control-plane/inference-gateway/Forge tests, all 166 harness tests, Linux isolation, lint, protobuf freshness, chart checks, release contracts, source-authority checks, and all production Docker builds.

Focused HOR-497 evidence also passed:

```text
make -C control-plane harness-deps harness-lint harness-test harness-isolation-test
npm --prefix control-plane/harness audit --omit=dev  # found 0 vulnerabilities
```

## Production, rollback, and publication

HOR-480 itself changes no production deployment and promotes no artifact. Complete runs used temporary isolated Kind fixtures and temporary CPU/GPU machines under existing cleanup/reaper controls.

HOR-497 changes the production harness image. Its publication classification is **required for HOR-497 and therefore HOR-480 acceptance**: merge, choose the founder-approved `control-plane` release target in `release-ticket`, bump source version authority, build/validate the exact candidate once, promote that exact digest without rebuild, and record the protected release evidence. Rollback is the previous immutable control-plane target; AgentPool workers must preserve durable session/PVC state during rollout.

All other findings are evidence-backed deferrals with no HOR-480 semantic publication requirement.
