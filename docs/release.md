# Build-once affected-target bundles and protected promotion

HOR-473 makes `master` an integration branch, not a publication trigger. The repository has three manual workflows:

- **Release candidate** (`release-candidate.yml`) builds and validates an explicit affected-target bundle from one exact master SHA.
- **Promote release** (`release-promote.yml`) verifies a successful candidate bundle and publishes its exact members after one founder approval in the protected `release` environment.
- **Release system rehearsal** (`release-rehearsal.yml`) exercises and cleans up disposable package, protected-tag, and prerelease operations after release-system or permission changes.

No push to `master` or tag push publishes an artifact or GitHub Release.

## Ticket acceptance and release intent

Engineering acceptance and semantic publication are separate decisions. Every
ticket's production-impact evidence must classify publication as one of:

- **Required for ticket acceptance:** the acceptance contract names the affected
  release targets and requires published artifacts. After merge, run a candidate
  from the exact current master SHA, promote that successful run through the
  founder-approved environment, and record the run, release, tag, digest, and
  checksum evidence before marking the ticket Done.
- **Deferred to the product release gate:** the merged implementation can be
  accepted as engineering delivery without publishing it. The containing
  outcome's `product-release-review` decides when the coherent product release
  is ready.
- **None:** documentation, tests, source-control operations, or other work does
  not alter a semantic artifact.

Acceptance must never dispatch a release merely because a PR merged. When
publication is required, target selection is explicit founder-reviewed release
intent; path selection may inform the proposal but cannot choose it.

## Independently versioned targets

| Target | Version authority | Published outputs |
| --- | --- | --- |
| `control-plane` | `control-plane/VERSION` | control-plane, harness, and tool-runner images |
| `inference-gateway` | `inference-gateway/VERSION` | inference-gateway image |
| `forge` | `forge/VERSION` | Linux/macOS × amd64/arm64 archives in one GitHub Release |
| `control-plane-chart` | chart `Chart.yaml` | control-plane OCI chart |
| `inference-gateway-chart` | chart `Chart.yaml` | inference-gateway OCI chart |
| `iterabase-platform-chart` | chart `Chart.yaml` | platform chart plus same-version certificate substrate companion |

The protected Git tags remain `control-plane-v<version>`, `inference-gateway-v<version>`, `forge-v<version>`, and `<chart>-<version>`. Targets keep independent versions and namespaced releases, but one product change may release any coherent subset together. Forge's platform matrix is one target, not four releases.

## Candidate bundle flow

Dispatch **Release candidate** from `master` with:

- a comma-separated target set such as `control-plane,control-plane-chart,forge`; and
- one full commit SHA contained in `master`.

The workflow trims and validates the explicit target set, rejects empty, unknown, or duplicate members, and canonicalizes it in repository target order. CI may suggest affected targets, but it does not silently choose release intent. Every selected version is inferred from source; callers cannot supply conflicting versions.

1. Preflight validates the repository release contract, exact source membership, target set, version authorities, production-tag uniqueness, and absence of every planned semantic image/chart identity. Existing semantic artifacts or an unavailable registry fail before builds and validation begin.
2. Every selected target is built exactly once. Image targets push canonical digests plus immutable run-scoped aliases in the existing GHCR packages. The alias format is `<source-sha>-<run-id>-<run-attempt>` and the exact value is recorded in the plan and image metadata separately from the source SHA and digest. Re-dispatches and GitHub run attempts therefore cannot collide. Existing aliases are never deleted or retargeted, and no run-specific package name or candidate package namespace is created. Chart and Forge archives remain Actions artifacts.
3. Validation consumes all selected candidates together. For example, a selected control-plane chart installs the selected control-plane image digest, and selected Forge validation runs with both. Shared owner, chart, Kind, and real-machine suites are deduplicated into one union.
4. Any unselected dependency used by validation resolves to an explicit, reviewed, already-published baseline. Candidate evidence distinguishes selected candidate identities from baseline dependencies; it never treats a bumped but unpublished repository version as an available baseline.
5. A generated bundle bill of materials records source SHA, selected target/version pairs, exact artifact identities, published baselines, native chart dependencies, fixture inputs, and validation result. There is no hand-maintained global compatibility manifest.
6. The final `release-candidate` Actions artifact contains the canonical plan, evidence, exact chart/Forge files, checksums, SBOMs, and image digest metadata. It is retained for 90 days pending promotion or expiry.

Real-machine assertions remain bounded rather than relying on fixed timing. Scenarios that deploy product workloads wait up to 10 minutes for every control-plane Deployment to report its current generation Available before inspecting requested digests and CRI image IDs. The dedicated-workspace exact-candidate scenario uses full product workloads and owns fixed mount/class/RWO evidence; the complete CPU scenario also owns migration and exact product-image handoff for coordinated candidates. AgentPool readiness retains its 10-minute bound; a timeout emits the AgentPool condition/message, worker/PVC/PV hostPath, workspace mount/capacity state, and recent platform/storage events. When the mandatory GPU gate exhausts all offered DigitalOcean regions/sizes, the candidate remains failed and is classified as blocked by an evidenced external-capacity dependency. It is retried when capacity returns; it is never converted to a skip under `FORGE_E2E_REQUIRE_CAPACITY=true`.

## Promotion flow

Dispatch **Promote release** from `master` with the successful candidate workflow run ID.

Before approval, the workflow verifies:

- the run belongs to this repository's `Release candidate` workflow;
- it was manually dispatched from `master` and concluded successfully;
- its source SHA remains contained in `master`;
- every selected target is known and source-versioned;
- its plan and every artifact checksum match the candidate evidence.

The publication job then waits once for founder approval in the protected `release` environment. After approval it re-verifies the bundle and preflights **every** semantic image, chart, protected tag, and GitHub Release destination before the first mutation. It then:

- verifies each exact tested run-scoped alias again, then adds semantic image tags to those digests;
- pushes unchanged chart archives;
- creates or verifies each protected namespaced Git tag at the exact source SHA; and
- creates one GitHub Release per selected target, attaching that target's exact candidate files plus the shared bundle plan and evidence.

Nothing is rebuilt. GitHub and GHCR do not provide a cross-package transaction, so promotion is resumable rather than falsely atomic: an identical already-published member is verified and skipped, a missing member continues, and any conflicting digest, archive, tag, or Release asset fails closed.

## Generated compatibility evidence

Compatibility evidence answers “what exact combination did this bundle prove?” It is generated from actual inputs:

- selected component `VERSION` files and chart metadata;
- selected chart dependencies;
- explicit published baseline identities for unselected dependencies;
- E2E fixture constants;
- image digests and archive checksums;
- source SHA and deduplicated selected scenarios.

The scenario set is compiled from each owner `TestE2E` entrypoint via `make e2e-catalogue`. Scenario metadata—not `release/targets.json` or changed-file narrowing—associates release targets with Kind/real-machine Make targets, bounds, and mandatory CPU/GPU capacity. A coordinated request takes the conservative union. The release target file retains artifact/version authority only.

Targets retain independent versions. A bundle records a tested combination without imposing a synthetic lockstep platform version.

## Release-system rehearsal

The rehearsal is manual and is not part of normal candidate or promotion execution. It runs through the real protected `release` environment and environment-scoped credentials, creates a unique temporary image manifest in the existing control-plane package, creates a protected `dry-run/rehearsal-<run-id>` tag and prerelease, verifies them, and removes all three before completing.

Run it only when commissioning or changing release workflow code, environment permissions, package permissions, deploy keys, or tag rulesets. Workflow logs provide the durable audit; it leaves no release catalogue or candidate-package namespace.

## Protection and operator audit

The `release` environment must require founder review, permit only `master`, and hold `RELEASE_TAG_SSH_KEY`. The active tag ruleset protects production namespaces plus `dry-run/**`. Because GitHub grants deploy-key bypass by actor class, the release key must remain the repository's only write deploy key.

Before release and after repository-key or ruleset changes:

```bash
make release-security-audit

test "$(gh api repos/nunocgoncalves/iterabase-mono/keys \
  --jq '[.[] | select(.read_only == false)] | length')" -eq 1
```

The sole write key must be `iterabase protected release tags (validated)`. Repository default workflow permissions remain read-only; only candidate image jobs receive package write, and only the approved promotion/rehearsal job receives publication permissions.

## Local validation

```bash
make release-check
python3 .github/scripts/test_select_ci.py
```

Release-only implementation changes are handled by these focused contract checks. Changes to affected-target selection or shared CI/test infrastructure deliberately fan out broadly because they can invalidate every selection decision.

## Rollback

No overlay deploys automatically. Consumers continue pinning immutable versions. Disable or revert the manual workflows to stop publication. Never overwrite, retarget, or delete an immutable candidate alias or production release to roll back behavior; publish a corrected candidate/version and update consumers deliberately. If publication stops between bundle members, resume the exact verified candidate rather than rebuilding it.
