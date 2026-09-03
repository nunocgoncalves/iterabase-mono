# Build-once affected-target bundles and protected promotion

`master` is an integration branch, not a publication trigger. Publication uses
three manual workflows:

- **Release candidate** builds and validates an explicit affected-target bundle
  from one exact SHA contained in `master`.
- **Promote release** verifies a successful candidate run and publishes its
  unchanged members after one founder approval in the protected `release`
  environment.
- **Release system rehearsal** exercises disposable package, protected-tag, and
  prerelease operations after release-control or permission changes.

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
   chart/companion and Forge outputs remain retained Actions artifacts. Locked
   external Helm dependencies are acquired with bounded command-level transport
   attempts only while packaging those chart artifacts; candidate product chart
   scenarios do not reacquire those repositories. Required validation-only
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
   candidate evidence record.

Selected targets may never resolve to baselines. Unselected dependencies use
only explicit published references whose image digests, chart checksums, or
Forge archive checksum are resolved into the plan before execution. A bumped but
unpublished repository version is not inferred as an available baseline.

Temporary and candidate artifact custody differs; recipes and assertions do not.
Temporary artifacts expire and have no semantic names. Candidate identities are
immutable and retained for no-rebuild promotion.

## Promotion flow

Dispatch **Promote release** from `master` with the successful candidate run ID.
Before approval it verifies:

- repository/workflow identity, manual event, master branch, and successful run;
- source SHA containment in `master`;
- normalized plan and all retained asset checksums;
- exact selected target/version/alias identities; and
- the complete reconciled scenario/stage/runtime result set.

The publication job then waits once for founder approval in the protected
`release` environment. After approval it re-verifies the candidate and preflights
every semantic image, chart, tag, and GitHub Release destination before the first
mutation. It then:

- adds semantic image tags to the exact tested digests;
- pushes unchanged chart/companion archives;
- creates or verifies protected namespaced tags at the exact source SHA; and
- creates or completes one GitHub Release per selected target using exact
  retained candidate files and shared evidence.

Nothing is rebuilt. GitHub and GHCR do not provide a cross-package transaction,
so promotion is resumable rather than falsely atomic: an identical completed
member is verified and skipped, a missing member continues, and any conflicting
identity fails closed.

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

## Release-system rehearsal

The rehearsal is manual and separate from product candidates. It runs through
the protected environment, creates a unique temporary image manifest, protected
`dry-run/rehearsal-<run-id>` tag, and prerelease, verifies them, and removes all
three. Run it only after release workflow, environment, package permission,
deploy-key, or ruleset changes.

## Protection and operator audit

The `release` environment must require founder review, permit only `master`, and
hold `RELEASE_TAG_SSH_KEY`. The active ruleset protects production namespaces
and `dry-run/**`. The release deploy key must remain the repository's only write
deploy key.

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
