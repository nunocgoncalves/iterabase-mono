# Protected candidate validation and release promotion

HOR-473 makes `master` an integration branch rather than a release trigger. The
only semantic publication path is the manually dispatched **Protected release**
workflow in `.github/workflows/release.yml`. Pushes to `master` and pushes of any
tag create no semantic package, production `latest`, tag-derived artifact, or
GitHub Release.

## Release identities

Artifact names remain unchanged. New monorepo Git tags are namespaced:

| Target | Git tag | Published artifact |
| --- | --- | --- |
| control-plane | `control-plane-v<semver>` | `ghcr.io/nunocgoncalves/control-plane`, `control-plane-harness`, and `control-plane-tool-runner` |
| inference-gateway | `inference-gateway-v<semver>` | `ghcr.io/nunocgoncalves/inference-gateway` |
| Forge | `forge-v<semver>` | GitHub Release archives for Linux/macOS and amd64/arm64 |
| control-plane chart | `control-plane-<semver>` | `oci://ghcr.io/nunocgoncalves/iterabase-charts/control-plane` |
| inference-gateway chart | `inference-gateway-<semver>` | `oci://ghcr.io/nunocgoncalves/iterabase-charts/inference-gateway` |
| platform chart | `iterabase-platform-<semver>` | platform plus its same-version `cert-manager-substrate` companion |

Versions are independent. A coordinated request may select several targets, but
it does not create a lockstep platform version.

## Repository authority

`release/compatibility.json` is the reviewed compatibility authority at each
master commit. It records exact component versions, chart versions and
`appVersion` relationships, the platform companion version, and current E2E
fixture baselines. `release/targets.json` is the conservative temporary mapping
from release target to required owner/Kind/real-machine suites. HOR-476 must
replace that mapping with compiled scenario metadata while preserving or
strengthening its coverage.

A version change requires a normal reviewed PR that updates implementation
version metadata and the compatibility manifest together. A release request is
valid only when every selected input exactly matches the manifest at the
requested full master SHA.

Validate these contracts locally with:

```bash
python3 .github/scripts/test_release.py
python3 .github/scripts/release.py validate
```

## Candidate and promotion flow

1. Dispatch the workflow **from `master`** with a full commit SHA contained in
   `master`, at least one exact version, and optional dry-run mode.
2. Preflight rejects malformed versions, manifest/chart/fixture drift,
   non-master commits, conflicting tags, and conflicting semantic package
   versions. Existing state is accepted only for an idempotent retry of the same
   source and exact digest/checksum.
3. Images are built and pushed once under
   `candidate-<sha-prefix>-<run-id>`. Validation references them as
   `<candidate-tag>@sha256:<digest>` and verifies the resulting pod image IDs;
   mutable tags alone are never the validation identity. Charts are packaged
   once and staged under the isolated run-addressed
   `iterabase-release-candidates` OCI namespace. Forge archives are built once
   and retained as Actions artifacts.
4. The target-driven union of owner, chart runtime, candidate-backed Kind, and
   mandatory CPU/GPU suites runs. Selected image and chart candidates replace
   their manifest entries across the coordinated union, including the
   real-machine chart values; unselected dependencies use explicit versions
   from the compatibility manifest. Missing mandatory capacity or credentials
   are incomplete, never a release pass.
5. The workflow records image digests, archive checksums, SPDX SBOMs, BuildKit
   provenance, GitHub keyless attestations, compatibility data, and validation
   results.
6. Only after every required job passes does the promotion job wait for founder
   approval in the protected `release` environment.
7. Approval adds semantic image tags to the tested digests, pushes the unchanged
   chart archives, creates protected namespaced Git tags at the requested SHA,
   and creates GitHub Releases with the exact evidence. Nothing is rebuilt.
   Existing Release assets are reused only when byte-identical; missing assets
   may be completed, but mismatches are never overwritten.
8. Every promotion phase appends its target identity and outcome to a durable
   promotion ledger. An immutable ledger snapshot is retained as an Actions
   artifact even on failure and attached to every GitHub Release already created
   by a partially completed coordinated request.

A rejected environment deployment creates no semantic tag, production chart,
production image alias, or GitHub Release. Run-addressed candidates are
non-production and may be garbage-collected.

### Coordinated failure behavior

Publication across Git, GHCR, and GitHub Releases is not transactional. The
workflow preflights and validates the whole selected set, then promotes targets
idempotently. If a later target fails, already-published immutable targets remain
valid and are never deleted or retagged automatically. The promotion ledger
records every completed identity, the failed phase, and targets not yet attempted;
unreleased targets are retried after the fault is fixed. A retry may reuse
existing registry state or Release assets only when source SHA and artifact
digest/checksum/content match the prior evidence exactly.

## Dry-run / release rehearsal

Dry-run is a retained commissioning and regression mode for the release system,
not a prerequisite for every release. It exercises the real candidate,
validation, approval, deploy-key, package, tag, and Release APIs, but uses only:

- image tags `dry-run-<version>-<run-id>`;
- chart namespace `iterabase-release-dry-run/<run-id>`;
- protected Git tags `dry-run/<production-tag>-<run-id>`;
- GitHub prereleases.

It never creates a production semantic tag/chart alias, mutable `latest`, overlay
change, or deployment. Dry-run artifacts may be removed after evidence review.

## GitHub protection setup

Repository configuration is part of the release security boundary and must be
audited after changes.

### Protected environment

The `release` environment must:

- require the founder as reviewer;
- allow self-review because this is a solo-founder operation;
- permit deployments only from `master`;
- contain the `RELEASE_TAG_SSH_KEY` environment secret.

The secret is a dedicated write-enabled repository deploy key. GitHub models
ruleset bypass for the `DeployKey` actor class rather than a specific key ID, so
the release key must remain the repository's **only write deploy key**. Branch
rules do **not** give deploy keys bypass rights, so the release key cannot modify
`master`.

### Tag ruleset

Protect creation, update, deletion, and non-fast-forward changes for:

```text
control-plane-v*
inference-gateway-v*
forge-v*
control-plane-*
inference-gateway-*
iterabase-platform-*
dry-run/**
```

The ruleset grants bypass to the GitHub `DeployKey` actor class. Operationally,
only the release deploy key may be writable; all other deploy keys are prohibited
(or must be read-only). GitHub Actions itself has no bypass. `GITHUB_TOKEN`
receives package write in candidate/promotion jobs and contents write in
promotion only; repository default workflow permissions remain read-only.

GitHub does not expose repository Administration-read permission to
`GITHUB_TOKEN`, so the workflow cannot enumerate deploy keys without a separate
long-lived administrative PAT. To avoid that secret, the founder must run this
mandatory operator preflight before dispatch and after any repository-key change:

```bash
.github/scripts/audit_release_security.sh

test "$(gh api repos/nunocgoncalves/iterabase-mono/keys \
  --jq '[.[] | select(.read_only == false)] | length')" -eq 1
gh api repos/nunocgoncalves/iterabase-mono/keys \
  --jq '.[] | select(.read_only == false) | {id,title,verified,enabled}'
```

The sole result must be `iterabase protected release tags (validated)`. A second write key
blocks release until removed or made read-only.

### Audit commands

```bash
gh api repos/nunocgoncalves/iterabase-mono/environments/release
gh api repos/nunocgoncalves/iterabase-mono/rulesets --paginate
gh api repos/nunocgoncalves/iterabase-mono/actions/permissions/workflow
gh workflow view release.yml
```

Confirm there is no root workflow with a tag-push publication trigger:

```bash
rg -n 'tags:|action-gh-release|goreleaser.*release|helm push' .github/workflows
```

The protected workflow may contain promotion commands; its only trigger must
remain `workflow_dispatch`.

## Threat model

The gate is designed to prevent:

- publishing from an unreviewed or non-master commit;
- bypassing validation with a manually pushed tag;
- rebuilding between test and promotion;
- mutable `latest` becoming a production release boundary;
- chart/component version drift hidden by floating dependencies;
- a skipped required real-machine suite becoming green;
- broad Actions tag-ruleset bypass;
- provenance or evidence being substituted after validation.

The remaining accepted risks are explicit: candidate artifacts exist before
approval in isolated non-production namespaces; the founder can approve their
own environment deployment; and coordinated cross-registry publication can
partially complete. Immutable identities, least-privilege job tokens, deploy-key
scope, evidence, and idempotent recovery bound those risks.

## Rollback and operations

No overlay deploys automatically. Consumers continue pinning immutable released
versions. A bad workflow change is rolled back by reverting its source PR;
published immutable versions are not overwritten or deleted. Corrected content
uses a new version. Candidate and dry-run package cleanup is operational
housekeeping and must never delete an artifact referenced by release evidence.
