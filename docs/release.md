# Build-once affected-target bundles and protected promotion

`master` is an integration branch, not a publication trigger. Publication uses
three manual workflows:

- **Release candidate** builds and validates an explicit affected-target bundle
  from one exact SHA contained in `master`.
- **Promote release** verifies a successful candidate run and publishes its
  unchanged members after one founder approval in the protected `release`
  environment.
- **Release immutability gate** verifies the one retained, non-semantic
  draft-first publication against its exact tag, assets, and release attestation.

No push to `master`, tag push, merge, acceptance step, or rehearsal implicitly
publishes a semantic artifact.

## Ticket acceptance and release intent

Every ticket classifies semantic publication as:

- **Required for ticket acceptance:** after merge, the founder explicitly
  selects release targets for the exact current master SHA; successful candidate,
  promotion, and any named-environment deployment evidence block Done.
- **Deferred to product release review:** engineering can be accepted without
  publication; the product release gate later authorizes the target set.
- **None:** no semantic artifact is required.

Path selection may inform a proposal but never chooses semantic release intent.
HOR-540 itself is **none**: its all-target candidate is a non-promoted validation
rehearsal.

## Independently versioned targets

| Target | Version authority | Published outputs |
| --- | --- | --- |
| `control-plane` | `control-plane/VERSION` | control-plane, harness, and tool-runner images |
| `inference-gateway` | `inference-gateway/VERSION` | inference-gateway image |
| `forge` | `forge/VERSION` | Linux/macOS × amd64/arm64 archives |
| `control-plane-chart` | chart `Chart.yaml` | control-plane OCI chart |
| `inference-gateway-chart` | chart `Chart.yaml` | inference-gateway OCI chart |
| `iterabase-platform-chart` | chart `Chart.yaml` | platform chart plus same-version certificate substrate companion |

Tags remain namespaced (`control-plane-v<version>`,
`inference-gateway-v<version>`, `forge-v<version>`, and `<chart>-<version>`).
Targets keep independent versions even when validated and promoted together.

## Single artifact and E2E authority

`release/targets.json` owns target/version identity and every reviewed production
recipe: Docker context/file/arguments/labels, Helm dependency/package steps,
companion membership, and Forge GoReleaser config/version. The same recipe hashes
are consumed by temporary PR builders and candidate builders.

The candidate's execution plan is produced by `.github/scripts/e2e.py`, the same
planner used by PR E2E. Explicit requested targets
select a conservative union from compiled owner registrations. Each selected
scenario retains the same ID, owner Make target, timeout/capacity metadata, and
stage DAG used in source mode. There is no chart-only matrix, pre-catalogue
fallback, or duplicate candidate planner.

## Candidate bundle flow

Dispatch **Release candidate** from `master` with:

- a non-empty comma-separated target set; and
- one full SHA contained in `master`.

The workflow trims, validates, deduplicates, and canonicalizes targets in
repository order. Versions are read from source authority; callers cannot supply
conflicting versions.

For an explicit pre-merge validation rehearsal, dispatch the same workflow from
the ticket branch with `rehearsal: true` and the exact dispatch-head SHA. That
mode skips only existing semantic destination availability checks; it still
builds, composes, executes, reconciles, and retains the complete candidate
bundle. A rehearsal is structurally non-promotable: promotion accepts only a
successful candidate workflow whose recorded head branch is `master` and whose
source is contained in `master`. Rehearsal does not publish or promote semantic
artifacts.

1. **Preflight** verifies exact checkout/membership, recipes, version authorities,
   candidate alias uniqueness, semantic destination availability, compiled
   candidate routing, and immutable published baselines.
2. **Build once.** Selected product images are pushed by digest and receive one
   immutable run-scoped alias `<source-sha>-<run-id>-<run-attempt>`. Selected
   chart/companion and Forge outputs remain retained Actions artifacts. Every
   external Helm archive is downloaded directly through
   `.github/inputs/remote-content.json`, and its reviewed SHA-256 is verified
   before packaging; mutable repository indexes are not authority. A bounded
   retry recovers transport only and never accepts changed bytes. Candidate
   product chart scenarios do not reacquire those inputs. Required validation-only
   artifacts are exact-source temporary Actions artifacts and can never be
   promoted.
3. **Compose once per scenario.** The shared composer verifies every selected or
   baseline digest/checksum/source/recipe identity, composes selected nested
   charts into the exact platform archive, and supplies the same runtime-bundle
   schema used by PR execution.
4. **Execute the compiled union.** F2 and mandatory F3 jobs invoke the same owner
   scenario and stage graph. Each CPU/GPU path shares its literal
   `iterabase-permanent-fixture-<capacity>` non-canceling lock across PR, master,
   and candidate workflows. Independent CPU and GPU hosts may run concurrently.
5. **Reconcile actual evidence.** Candidate validation requires exactly one
   machine-readable result per planned scenario and one passed terminal result
   per declared stage. The aggregate reads the retained plan and a compact,
   explicit job-result map from files rather than expanding the full plan and
   dependency graph into the process environment. Missing/extra/skipped/blocked/
   canceled results or identity mismatches fail even when a matrix job itself
   appears successful.
6. **Retain promotion trust.** The 90-day `release-candidate` artifact contains
   the normalized plan, complete per-scenario/stage/runtime identity records,
   selected candidate files and metadata, checksums, SBOMs, and the generated
   candidate evidence record. Plan and evidence bind the repository, workflow
   path, manual event, workflow-control SHA, run ID, and run attempt in addition
   to the independently selected source SHA.

Selected targets may never resolve to baselines. Unselected dependencies use
only explicit published references whose image digests, chart checksums, or
Forge archive checksum are resolved into the plan before execution. A bumped but
unpublished repository version is not inferred as an available baseline.

Temporary and candidate artifact custody differs; recipes and assertions do not.
Temporary artifacts expire and have no semantic names. Candidate identities are
immutable and retained for no-rebuild promotion.

## Promotion flow

Dispatch **Promote release** from `master` with the successful candidate run ID.
Both jobs check out and assert the immutable workflow-dispatch control SHA;
`master` is never resolved again as executable promotion code. The candidate
source remains independently allowed to be any explicit full SHA contained in
`master`. Before approval the workflow verifies:

- repository and head-repository identity, exact workflow path, manual event,
  workflow-control SHA, run ID/attempt, master branch, and successful run;
- source SHA containment in `master`;
- normalized plan and all retained asset checksums;
- exact selected target/version/alias identities; and
- the complete reconciled scenario/stage/runtime result set.

The publication job then waits once for founder approval in the protected
`release` environment. After approval it reasserts the control checkout and
workflow SHA; re-queries candidate workflow/run/source authority; re-verifies
candidate bytes, source containment, environment, collaborator, and common
protected-tag authority; and preflights every semantic image, chart, tag, and
GitHub Release destination—including governed published metadata, complete bytes,
and immutable state—before the first mutation. It then:

- adds semantic image tags to the exact tested digests;
- pushes unchanged chart/companion archives;
- creates or verifies protected namespaced tags at the exact source SHA; and
- stages each target's exact files plus a manifest binding target, version, tag,
  source SHA, candidate run, release title/body/target/prerelease state, filename,
  size, and SHA-256 as an unpublished draft, verifies all metadata and the complete
  draft, and publishes it exactly once.

Nothing is rebuilt. An unpublished draft may be replaced in full on retry. An
existing published Release is verification-only: its governed metadata,
immutable state, complete member set, sizes, and bytes must already match, and
promotion never uploads a late member or changes a published asset. Other identical completed image/chart/tag members are verified
and skipped; conflicts fail closed.

## Permanent fixtures and incomplete candidates

Every selected F3 scenario is mandatory. Candidate execution uses the same fixed
CPU/GPU addresses, pinned SSH identities, workspace devices, explicit
pre/post-test purge/reboot lifecycle, and GPU model-cache authority as source
execution. Its result retains those fixture identities alongside exact artifact
and stage evidence.

A missing fixture-scoped key, unreachable host, host-key/device drift, corrupt
model cache, or failed cleanup/reboot produces a failing classification and
retained redacted diagnostics. It never becomes a skip, retry, or pass-on-retry.
Actions has no provider credential and cannot power-cycle, rescue, reimage,
replace, or delete a fixture. Founder-operated quarantine/recovery must restore
the runbook baseline before a new candidate dispatch.

## Post-merge immutable-release gate

After the controlling change merges, the founder approves **Release immutability
gate**. Its least-privilege protected `GITHUB_TOKEN` never calls the admin-only
repository immutable-release setting endpoint. The workflow instead audits every
accessible invariant, including the protected environment, environment deploy-key
identity, common tag-ruleset shape, founder-only writer set, fixture callers,
provider-authority absence, and immutable control checkout.

The gate resolves only retained Release database ID `382723775` and tag
`dry-run/immutable-release-gate-v1`; it has no Release-create, Release-delete,
or replacement path. Before any presentation repair, it fails closed unless the
Release reports immutable and matches the retained source commit, annotated
tag object, exact two asset IDs/names/sizes/digests/downloaded bytes, governed
body/prerelease state,
and GitHub-generated release attestation. `gh release verify` must bind the exact
tag object and complete asset digest set, and `gh release verify-asset` must
validate each downloaded member. The title may be restored from only the known
failed-probe value `forbidden` to its governed tag value after those checks; an
already-restored title is accepted.

GitHub immutable Releases cryptographically lock the associated tag and assets
and attest that set. Presentation metadata such as title, notes, prerelease/latest
state remains governed expected state but is mutable, and Release existence is
not an immutability guarantee. The live gate therefore probes only late asset
upload, retained-asset deletion, and tag movement. Every operation must fail with
an immutable-release-specific denial. It then re-verifies the attestation and
each asset, downloads and compares the exact bytes, and proves the release ID,
immutable response, tag object/target, complete asset identities, attestation
subjects, and governed presentation are unchanged. Redacted evidence retains
those distinct immutable-authority and presentation fields for 90 days without
claiming that presentation or Release deletion is cryptographically locked.

After the behavioral gate succeeds, an authenticated administrator runs
`make release-security-audit` from the resulting `master` authority. That audit
is the sole direct proof that the repository immutable-release setting is exactly
enabled and retains the admin-only deploy-key, bypass-actor, workflow-permission,
secret, and variable checks.

## Protection and operator audit

The `release` environment must require founder review, permit only `master`, and
hold `RELEASE_TAG_SSH_KEY`. The active ruleset protects production namespaces
and `dry-run/**`. The release deploy key must remain the repository's only write
deploy key. The complete push/maintain/admin collaborator set must remain exactly
`nunocgoncalves`; repository secrets must remain exactly the CPU/GPU fixture keys;
and immutable releases must remain enabled. Protected workflow invocations set
`AUDIT_ADMIN_ENDPOINTS=false`; they preserve accessible checks but neither call
nor claim direct evidence from the immutable-release setting or other admin-only
endpoints. The authenticated admin invocation is fail-closed when the setting is
disabled, malformed, or unavailable.

```bash
make release-check
make release-security-audit

test "$(gh api repos/nunocgoncalves/iterabase-mono/keys \
  --jq '[.[] | select(.read_only == false)] | length')" -eq 1
```

Repository default workflow permissions remain read-only. Only candidate image
jobs receive package write, and only approved promotion/rehearsal jobs receive
publication permissions.

## Rollback

No overlay deploys automatically. Consumers continue pinning immutable versions.
Disable or revert manual workflows to stop publication. Never overwrite,
retarget, or delete immutable candidate aliases or production releases to roll
back behavior; publish a corrected version and update consumers deliberately.
If publication stops between members, resume the exact verified candidate rather
than rebuilding it.
