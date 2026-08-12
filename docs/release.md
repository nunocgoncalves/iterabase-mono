# Build-once candidates and protected release promotion

HOR-473 makes `master` an integration branch, not a publication trigger. The repository has three manual workflows:

- **Release candidate** (`release-candidate.yml`) builds and validates one target from one exact master SHA.
- **Promote release** (`release-promote.yml`) verifies a successful candidate run and publishes it after founder approval in the protected `release` environment.
- **Release system rehearsal** (`release-rehearsal.yml`) exercises and cleans up disposable package, protected-tag, and prerelease operations after release-system or permission changes.

No push to `master` or tag push publishes an artifact or GitHub Release.

## Independently versioned targets

| Target | Version authority | Published outputs |
| --- | --- | --- |
| `control-plane` | `control-plane/VERSION` | control-plane, harness, and tool-runner images |
| `inference-gateway` | `inference-gateway/VERSION` | inference-gateway image |
| `forge` | `forge/VERSION` | Linux/macOS × amd64/arm64 archives in one GitHub Release |
| `control-plane-chart` | chart `Chart.yaml` | control-plane OCI chart |
| `inference-gateway-chart` | chart `Chart.yaml` | inference-gateway OCI chart |
| `iterabase-platform-chart` | chart `Chart.yaml` | platform chart and same-version certificate-substrate companion |

The protected Git tags remain `control-plane-v<version>`, `inference-gateway-v<version>`, `forge-v<version>`, and `<chart>-<version>`. One candidate and promotion run handles one logical target. Forge's platform matrix is one target, not four releases.

## Candidate flow

Dispatch **Release candidate** from `master` with:

- one target from the table above; and
- one full commit SHA contained in `master`.

The workflow infers the version from source. A caller cannot supply a second, conflicting version.

1. Preflight validates the repository release contract, exact source membership, version authority, and production-tag uniqueness.
2. Image targets build once and push a canonical digest to the existing GHCR package. The workflow creates a full-source-SHA alias only when it is absent or already resolves to the same digest. Tests consume `repository:<full-sha>@sha256:<digest>`.
3. Chart targets package the final archive once. Forge produces its four final GoReleaser archives once. Archives, checksums, SBOMs, and metadata remain GitHub Actions artifacts; candidate runs do not create persistent run-specific GHCR package repositories.
4. Only the selected target's owner, chart runtime, Kind, and mandatory real-machine suites run. Missing mandatory capacity is incomplete.
5. A generated candidate bill of materials records source SHA, target/version, exact artifact identities, native chart dependencies, fixture inputs, and validation result. There is no hand-maintained global compatibility manifest.
6. The final `release-candidate` Actions artifact contains the plan, evidence, exact chart/Forge files, checksums, SBOMs, and image digest metadata. It is retained for 90 days pending promotion or expiry.

## Promotion flow

Dispatch **Promote release** from `master` with the successful candidate workflow run ID.

Before approval, the workflow verifies:

- the run belongs to this repository's `Release candidate` workflow;
- it was manually dispatched from `master` and concluded successfully;
- its source SHA remains contained in `master`;
- it represents exactly one known target;
- its plan and every artifact checksum match the candidate evidence.

The publication job then waits for founder approval in the protected `release` environment. After approval it:

- adds semantic image tags to the tested digests;
- pushes unchanged chart archives to `oci://ghcr.io/nunocgoncalves/iterabase-charts`;
- creates or verifies the protected namespaced Git tag at the exact source SHA; and
- creates one GitHub Release with the exact candidate files and evidence.

Nothing is rebuilt. Existing identities are accepted only when byte/digest/source identical, allowing a single-target retry without a coordinated partial-promotion ledger.

## Generated compatibility evidence

Compatibility evidence answers “what exact combination did this candidate run prove?” It is generated from actual inputs:

- component `VERSION` files;
- chart versions, `appVersion`, and dependency versions;
- E2E fixture constants;
- image digests and archive checksums;
- source SHA and selected scenarios.

Bundle compatibility remains expressed where it is real: the platform chart's native dependency metadata. Independent targets do not share a synthetic lockstep compatibility version.

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

No overlay deploys automatically. Consumers continue pinning immutable versions. Disable or revert the manual workflows to stop publication. Never overwrite or delete an immutable production release to roll back behavior; publish a corrected version and update consumers deliberately.
