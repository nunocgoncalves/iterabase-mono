# Product source authority and legacy archives

`nunocgoncalves/iterabase-mono` is the sole writable public source for the
Iterabase control plane, inference gateway, Forge, and Helm charts. Product
changes, pull requests, CI, and semantic releases start here. Deployment
overlays and the marketing site remain independently owned repositories.

The one-way cutover was approved in HOR-474. There is no dual write, mirror,
subtree synchronization, matching-branch lookup, or routine path back to a
standalone component repository.

## Authority map

| Concern | Canonical location |
| --- | --- |
| Control-plane source | [`control-plane/`](../control-plane/) |
| Inference-gateway source | [`inference-gateway/`](../inference-gateway/) |
| Forge source and E2E cleanup | [`forge/`](../forge/) and [root reaper workflow](../.github/workflows/reaper.yml) |
| Chart source | [`charts/`](../charts/) |
| Pull-request CI | [root CI and E2E workflows](ci.md) |
| Candidate and promotion | [root release workflows](release.md) |
| Deployment/customer intent | Independent overlay repositories |

Clone one source tree and branch from `master`:

```bash
git clone https://github.com/nunocgoncalves/iterabase-mono.git
cd iterabase-mono
git switch -c HOR-123-short-description origin/master
```

Do not clone a legacy component repository to make a product change. Published
image names and the `ghcr.io/nunocgoncalves/iterabase-charts` OCI namespace are
artifact identities, not source-authority links, and remain unchanged.

## Frozen legacy repositories

The historical repositories remain public archives so old commit, pull-request,
tag, release, and package links continue to resolve. Their `master` branches are
frozen at the exact heads imported by HOR-472:

| Historical repository | Frozen `master` head | Representative PR | Representative release |
| --- | --- | --- | --- |
| `nunocgoncalves/control-plane` | `c63eea9d21c367a3e5fd91431bedc853fb15a16b` | [#1](https://github.com/nunocgoncalves/control-plane/pull/1) | [`v0.0.25`](https://github.com/nunocgoncalves/control-plane/releases/tag/v0.0.25) |
| `nunocgoncalves/inference-gateway` | `cf093df2cdca30e916cb340d3e5dc1ab29c49989` | [#1](https://github.com/nunocgoncalves/inference-gateway/pull/1) | [`v0.2.5`](https://github.com/nunocgoncalves/inference-gateway/releases/tag/v0.2.5) |
| `nunocgoncalves/forge` | `56afae7b21f97a1c40c81705954756ef16f46674` | [#1](https://github.com/nunocgoncalves/forge/pull/1) | [`v0.8.1`](https://github.com/nunocgoncalves/forge/releases/tag/v0.8.1) |
| `nunocgoncalves/iterabase-charts` | `0d97d50962afcd03aa474f096a8948f0e1dcd8b5` | [#1](https://github.com/nunocgoncalves/iterabase-charts/pull/1) | [`iterabase-platform-0.3.9`](https://github.com/nunocgoncalves/iterabase-charts/releases/tag/iterabase-platform-0.3.9) |

The legacy descriptions and homepages point here. Their historical READMEs are
left byte-for-byte at the imported heads: adding a README commit would violate
the exact-head archive contract. GitHub's archive banner plus repository
metadata provide the active-source notice. Each legacy repository had zero
standalone GitHub issues at cutover; the audit records that fact rather than
inventing an issue sample, while preserving all historical pull requests.

## Protected cutover procedure

Archival happens only after the HOR-474 change is merged to `master` and its
required checks pass.

1. Verify `master` requires `CI / required` and `E2E / required`, the protected
   release-tag ruleset is active, and the `release` environment retains founder
   approval and the validated release deploy key.
2. Confirm the current legacy heads, imported ancestry, no open legacy pull
   requests, and representative historical evidence. Include public GHCR and
   chart pulls:

   ```bash
   CHECK_ARTIFACTS=true make source-authority-audit \
     SOURCE_AUTHORITY_STATE=pre-archive
   ```

3. Manually run **Forge E2E reaper** on monorepo `master`. It must succeed on the
   current master SHA before disabling the legacy Forge reaper. Record the run
   ID.
4. Dry-run the guarded archive operation, then explicitly apply it:

   ```bash
   .github/scripts/archive_legacy_repositories.sh \
     --reaper-run-id <run-id>
   .github/scripts/archive_legacy_repositories.sh \
     --reaper-run-id <run-id> --apply
   ```

   The script rechecks both required contexts on current `master`, the exact
   successful root-reaper run, imported heads and ancestry, and historical
   evidence. It then disables every legacy workflow, adds canonical metadata
   pointers, and archives the repositories.
5. Verify the final state and sample the preserved artifacts again:

   ```bash
   CHECK_ARTIFACTS=true make source-authority-audit \
     SOURCE_AUTHORITY_STATE=archived
   ```

The root scheduled reaper is the only post-cutover cleanup authority. Repository
CI and release credentials stay only in the monorepo. Existing GHCR image and
chart names remain unchanged, so overlays continue consuming the same immutable
artifact identities.

## Commissioning evidence before archive

The release foundation was commissioned from exact master SHA
`a9bd171a1d3f63d361846edf86fa5eab049720b0` before archival:

- release-system rehearsal run `31727406655` passed;
- all-six-target candidate run `31727479627` passed;
- protected promotion run `31728824116` passed without rebuild;
- idempotent promotion verification run `31729156506` passed;
- namespaced monorepo releases exist for all six targets;
- the legacy heads are unchanged and contained in monorepo ancestry.

HOR-474's merged pull request supplies the representative atomic change: it
touches root automation, Forge-owned behavior, chart documentation, and shared
source/release guidance under one branch and one pair of required aggregate CI
contexts.

## Emergency unarchive boundary

Unarchive is allowed only for catastrophic cutover repair—for example, GitHub
makes the monorepo unavailable and preserving source or release history requires
legacy repository administration. It is never a way to continue normal work,
accept a component-only hotfix, or maintain two writable sources.

Before unarchiving:

1. Record explicit founder approval and the catastrophic condition in Linear.
2. Disable monorepo merges/releases if concurrent mutation could occur.
3. Identify one legacy repository, one bounded repair, and the exact stop
   condition. Do not unarchive all repositories by default.
4. Preserve the imported head and historical refs before any administrative
   repair. Product code changes still target the monorepo unless source recovery
   is impossible.
5. Reconcile any unavoidable repair forward into the monorepo, rerun the source
   authority and release-security audits, disable legacy workflows, and
   re-archive immediately.

A routine rollback is a revert or corrective monorepo pull request. No procedure
syncs monorepo commits back to a legacy repository.
