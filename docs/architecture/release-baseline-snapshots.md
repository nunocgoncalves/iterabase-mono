# Release baseline snapshots

Status: approved and implemented by HOR-554.

## Approved design decision

- **DES-HOR-554-01 — Latest-anchored complete immutable baseline snapshots**
  - Approved by: Nuno Gonçalves
  - Approved on: 2026-09-15
  - Scope: repository-wide PR/E2E/candidate planning, candidate evidence,
    release-manifest generation, promotion, Latest designation, audit, retry,
    and protected rollback.
  - Consequences: one complete snapshot replaces hand-maintained baseline maps;
    candidate Release manifests become schema v3; every consumer and promotion
    fails closed on incomplete or inconsistent custody; promotion has one final
    complete-vector visibility handoff; rollback can select only an exact
    immutable ancestor vector.
  - Evidence: Linear [HOR-554](https://linear.app/horizonshift/issue/HOR-554/release-tooling-protect-published-baseline-ledger-and-missing-baseline).

This record is the bounded architecture authority. `release/targets.json` still
owns the six logical targets, complete target membership, and reviewed producer
and consumer recipes. It no longer owns published identities. The
`published_baselines` map and recipe-level fixture references/checksums are
removed.

## Authority model

The sole active selection authority is
`GET /repos/nunocgoncalves/iterabase-mono/releases/latest`. Latest is mutable
presentation state, not artifact identity. A planning run calls it once and
captures the complete Release object. The run then uses only captured exact
Release database IDs, tags, source SHAs, snapshot/manifest hashes, asset IDs and
digests, annotated tag objects, immutable registry digests, protected Release
URLs, and attestation subjects.

A valid anchor is:

- non-draft and non-prerelease;
- GitHub-immutable;
- the deterministic highest selected target in fixed repository order;
- backed by one canonical `baseline-snapshot.json` schema-v1 envelope;
- complete for all targets, artifacts, variants, companions, and fixtures; and
- consistent with every exact target Release, manifest, downloaded byte set,
  tag object, release attestation, and per-asset attestation.

Release listings, registry listings, mutable aliases, semver maxima, source
versions, filenames, chart dependency values, and ambient workflow state cannot
select or reconstruct a baseline. After the initial pointer capture, movement of
Latest cannot alter the serialized plan.

## Snapshot schema v1

The file is a canonical JSON envelope:

```json
{
  "schema_version": 1,
  "snapshot_sha256": "<sha256 of canonical snapshot object>",
  "snapshot": {
    "schema_version": 1,
    "repository": "nunocgoncalves/iterabase-mono",
    "generator": {"name": "iterabase-release-baseline", "version": 1},
    "mode": "published",
    "cohort_id": "<candidate cohort>",
    "parent_anchor": {"release_id": 1, "tag": "...", "snapshot_sha256": "..."},
    "candidate": {"run_id": "...", "run_attempt": "...", "source_sha": "...", "control_sha": "..."},
    "selected_targets": ["..."],
    "anchor_target": "...",
    "anchor": {"target": "...", "release_id": 1, "tag": "...", "source_sha": "..."},
    "targets": ["exactly six ordered target cohorts"],
    "fixtures": ["exactly five ordered named fixture records"]
  }
}
```

The six target cohorts are, in order:

1. `control-plane` — control-plane, harness, and tool-runner images;
2. `inference-gateway` — inference-gateway image;
3. `forge` — Linux/macOS × amd64/arm64 archives;
4. `control-plane-chart`;
5. `inference-gateway-chart`; and
6. `iterabase-platform-chart` — platform, certificate substrate, and LVM storage
   substrate archives.

The separate fixtures are `certificate-migration-chart`,
`supported-platform-predecessor`, `supported-substrate-predecessor`,
`metallb-platform-predecessor`, and `metallb-substrate-predecessor`.

A target cohort binds one target, semantic version, source provenance, candidate
run/attempt, Release pin, and complete artifact set. An artifact repeats its
target/version boundary and records producer recipe identity where available.
Images require an authorized repository plus digest-qualified semantic reference.
Charts and companions require exact authorized OCI reference/digest and archive
filename/size/SHA-256. Forge requires GoReleaser/config identity and all four
exact filenames/sizes/SHA-256 values and protected-Release URLs. Fixtures use the
same exact OCI/archive contract but remain separate from current target cohorts.

Missing, extra, duplicate, reordered, malformed, mutable, unauthorized, mixed-
cohort, stale-parent, or cross-manifest data is invalid.

## Bounded bootstrap

The only accepted v2 root is:

- Latest/anchor `iterabase-platform-0.4.0`, Release ID `386705918`;
- source `b4b32b14d6ab89a85db24fc198879fdfe9621d2a`;
- candidate run `34541902001`, attempt `1`;
- plan SHA-256
  `c7f460fae540f17bfe06198bfaac9621f08260b6ba57744dc54b23d89b420746`;
- evidence SHA-256
  `c0bcd898dd9d1501b715f50c75508f0cf5049a4369a2eaf7c46b4644b806cbf3`;
- exact immutable siblings `control-plane-v0.0.33` ID `386705747`,
  `forge-v0.9.0` ID `386705816`, and `control-plane-0.5.0` ID `386705691`.

The bootstrap verifies the four exact v2 manifests, complete Release member sets,
all downloaded bytes, annotated tags, immutable state, release/per-asset
attestations, semantic image/chart identities, all Forge variants, inherited
inference image `0.2.7`, inherited inference chart `0.2.13`, and every named
fixture. The LVM row is `lvm-storage-substrate-0.4.0.tgz`, 14,569 bytes, SHA-256
`6b1233c2c27cab8597f0a1b77de1d445f5a77ea696df75d30de7bc1747eda7dd`.

The inherited inference target fields use
`bootstrap-inherited-and-attested`; unavailable historical producer Release/source
fields remain absent rather than being invented. Any other legacy Latest or any
drift in the exact root fails pending explicit review. Bootstrap constructs the
snapshot in memory and creates or mutates no source, ref, Release, tag, asset, or
external ledger.

## Planning and custody

PR and candidate planning resolve the complete published snapshot before build
matrices exist. Every planned scenario artifact maps exactly once:

- a selected candidate target uses `selected-candidate`;
- an intentionally affected PR artifact or explicit `temporary_only` recipe may
  use `selected-temporary`; and
- every other release-capable artifact uses `published-baseline` from the pinned
  snapshot.

There is no missing-baseline fallback. In particular, Forge-only planning resolves
LVM `0.4.0` from the snapshot and never requests
`lvm-storage-substrate-source.tgz`. Removing that row makes planning fail before
execution.

Composition and result reconciliation retain the snapshot hash and exact planned
reference/digest/checksum/filename/size. They do not call Latest or discover a
substitute. Candidate execution builds selected artifacts once, executes the
compiled F2/F3 matrix once, and retains exact stage/runtime evidence. Promotion
never reruns E2E.

After selected assets exist, candidate evidence contains:

- a complete candidate snapshot with exact candidate digests/archives and planned
  semantic destinations; and
- a complete planned-final snapshot.

Every unselected target cohort and every fixture is copied byte-for-byte from the
resolved parent snapshot.

## Manifest v3 and promotion

Promotion re-verifies the successful retained candidate and unchanged bytes. It
requires the exact parent ID to remain Latest before semantic mutation. Images,
charts, and protected tags are published or verified idempotently.

All selected GitHub Release drafts are created explicitly non-Latest first so
their database IDs can be pinned into one final snapshot. Drafts are replaceable
in full; published immutable Releases are verification-only. Each complete draft
receives its target assets, candidate plan/evidence, identical
`baseline-snapshot.json`, and a schema-v3 target manifest. Manifest v3 binds the
Release ID, target/version/tag/source, candidate run/attempt, cohort, parent,
deterministic anchor, snapshot hash, governed metadata, and complete exact asset
set.

Every selected Release is published using REST `make_latest: "false"`. Promotion
then rechecks the parent immediately before designation. If Latest is neither the
exact parent nor the exact intended anchor on retry, promotion fails. Otherwise
one REST `make_latest: "true"` update selects the deterministic anchor, followed
by an exact Latest reread and complete resolver verification. Consumers therefore
select either the prior complete vector or the new complete vector; partially
published members are never selected.

Failure before designation leaves the parent authoritative. Retry re-verifies
published members, replaces drafts in full, and creates no rebuilt member. A
retry after designation accepts only the exact intended anchor and succeeds after
complete re-verification.

## Rollback and concurrency

Normal promotion and protected rollback share literal non-canceling concurrency
group `release-promotion`. Rollback requires exact current and destination anchor
Release IDs, a Linear identifier, a non-empty reason, and founder approval in the
`release` environment.

The rollback resolver verifies the current snapshot, follows only exact parent
Release IDs/tags/snapshot hashes, verifies the destination complete snapshot, and
requires it to be an ancestor. It performs one `make_latest: "true"` update and
re-verifies the destination after the handoff. Partial target rollback is denied;
it requires a new candidate. No Release, tag, manifest, asset, or evidence is
edited, deleted, retagged, or overwritten.

## Security boundary

Repository default workflow permissions remain read-only. Contents-write is
scoped to founder-approved protected promotion, rollback, and the existing
retained immutability gate. Static and live release-security audit checks ensure
that `make_latest:true` exists only at the final promotion handoff and protected
rollback mutation, both under `release-promotion` concurrency and the `release`
environment.

The founder remains the sole repository writer and release-environment approver.
GitHub does not technically prevent that trusted administrator from manually
changing mutable presentation state. This is the explicit residual trust
boundary; any arbitrary, partial, mutable, non-anchor, stale, unattested, or
incomplete Release still fails every automated consumer.

## Rejected alternatives

The following remain explicitly rejected:

- post-release source PR activation;
- a custom immutable ledger Release or protected ledger branch;
- S3 or another external ledger service;
- per-artifact latest lookup, Release/registry listing discovery, or semver-max;
- mutable tags, source versions, or filenames as baseline authority;
- post-release `master` mutation;
- source custody when a published baseline row is missing; and
- partial-target pointer rollback.
