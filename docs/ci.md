# Monorepo continuous integration

HOR-470 replaces the inactive component-local source workflows with root-owned,
path-aware workflows. The stable branch-protection contexts are:

- `CI / required`
- `E2E / required`

Both aggregate jobs run for every pull request. Owner jobs that are not selected
are skipped; a selected job that fails or is canceled fails its aggregate.
Branch protection therefore does not depend on a changing matrix job name.

## Source-workflow parity

| Former source workflow/job | Monorepo owner | Status |
| --- | --- | --- |
| control-plane `ci / ci` (format, lint, build, unit, integration, envtest) | `CI / control-plane` | Ported |
| control-plane `ci / ui` | `CI / dashboard` | Ported |
| control-plane `ci / harness` | `CI / harness` | Ported; includes required Linux setpriv/UID/process isolation |
| control-plane `ci / tool-runner` | `CI / tool-runner` | Ported |
| control-plane `ci / proto` | `CI / protobuf` | Ported |
| inference-gateway `ci / ci` (format/imports, vet, lint, build, testcontainers tests) | `CI / inference-gateway` | Ported |
| Forge `ci / ci` (format, lint, GoReleaser check, build, fake-SSH tests) | `CI / forge` | Ported |
| Forge `e2e / harness-unit` | `E2E / harness-unit` | Ported |
| Forge `e2e / digitalocean-cpu` | `E2E / digitalocean-cpu` | Ported; Forge source PRs/manual runs |
| Forge `e2e / digitalocean-gpu` | `E2E / digitalocean-gpu` | Ported; Forge source PRs/manual/nightly |
| Forge five source-composed Kind jobs | `E2E / kind-*` | Ported; Forge and chart contract paths |
| Forge five nightly published-compatibility jobs | `E2E / published-latest-*` | Ported |
| charts `ci / lint` | `CI / charts-static` | Ported |
| charts `ci / certificate-ownership-migration` | `E2E / charts-runtime / certificate-ownership-migration` | Ported |
| charts `ci / install` | `E2E / charts-runtime / install` | Ported |
| charts `ci / install-observability` | `E2E / charts-runtime / install-observability` | Ported |
| charts `ci / install-observability-tls` | `E2E / charts-runtime / install-observability-tls` | Ported |
| component Docker build smoke | `CI / image-*` | Explicit per-image BuildKit jobs |

The former nested component release workflows have been removed. Root-owned
manual candidate, protected promotion, and disposable rehearsal workflows are
the sole publication path: one explicit affected-target bundle and exact master
SHA, build each selected target once, validate the coherent bundle, obtain one
founder approval, and promote without rebuild. See
[`release.md`](release.md). Forge's scheduled droplet reaper is now root-owned
by `.github/workflows/reaper.yml`; it uses the monorepo E2E source and secret and
is operational cleanup rather than a source quality gate. At archival, every
repository-authored legacy workflow and Dependabot security update is disabled,
Dependabot version updates must remain unconfigured, and repository Actions are
disabled. GitHub-managed `dynamic/*` entries may remain visible in
the workflow-list API; read-only dependency-graph entries can remain, while the
Dependabot writer is disabled explicitly because its jobs bypass repository
Actions disablement.

## Path ownership

`.github/scripts/collect_changed_paths.py` is the shared diff collector for both
workflows. It includes deletions and disables rename collapsing so both sides of
a move retain their owners. `.github/scripts/select_ci.py` is the owner-mapping
source of truth, and its tests prove deletion-only, cross-owner move, docs-only,
single-component, shared-contract, and cross-component changes.

- Root Makefile, Go workspace metadata, affected-target selector code, shared setup actions, and PR/E2E workflow contracts fan out to every owner.
- Release candidate/promotion/rehearsal implementation, target metadata, and component `VERSION` files run focused release contract checks and do not select unrelated product images, Kind scenarios, or CPU/GPU suites.
- Control-plane UI, harness, tool-runner, and protobuf contracts select their
  focused checks. The selected harness job always executes the Linux
  setpriv/UID/process isolation container; missing Docker/kernel prerequisites
  fail rather than skip. `control-plane/Makefile` selects both control-plane and
  harness gates so edits to the required target cannot bypass execution.
  Protobuf changes fan out to both generated Node consumers.
- Forge source/E2E changes retain the existing Forge unit, real-machine, and
  deterministic Kind gates.
- Chart changes run chart static/runtime checks and the current local-chart Kind
  scenarios. This is source composition from one checkout; no matching-branch
  lookup or cross-repository checkout exists.
- Markdown and repository documentation alone select no expensive owner job.
- `.github/scripts/select_ci.py` remains the PR affected-target authority. `release/targets.json` temporarily maps each explicitly requested release target to its required candidate suites; the candidate workflow deduplicates their union until HOR-476 replaces scenario lists with compiled metadata. It is not a second PR path selector and does not choose release intent.

Run the fixture matrix locally with:

```bash
python3 .github/scripts/test_select_ci.py
```

## Cache and setup contract

All third-party actions use immutable commit SHAs. Go and Node are exact patch
versions. Helm, Kind, kubectl, and kubeconform archives are pinned in
`.github/tools/checksums.txt`; archives are checksum-verified after both download
and cache restore.

- Go module/build caches use OS, architecture, exact Go version, and all four
  workspace `go.sum` files. There are no fallback restore keys.
- npm caches contain downloads only and use exact Node version plus the owning
  `package-lock.json`. `node_modules` is always rebuilt by `npm ci`.
- Helm caches contain only `~/.cache/helm` downloads and use exact Helm version
  plus every `Chart.lock` and `Chart.yaml`. Vendored chart directories are
  rebuilt and never cached.
- BuildKit uses a distinct GHA scope per image. BuildKit's content graph includes
  each Dockerfile and complete build context, so source or lock changes
  invalidate affected layers.
- Cache steps print the exact key/scope and whether the source was an exact hit
  or a cold population.

Kind clusters, databases, mutable fixtures, vendored/generated outputs, test
results, credentials, customer data, and release evidence are never cached.
Full control-plane Go validation uses `-count=1`, so restored build caches cannot
substitute cached test results. The root `make test` matrix also runs the Linux
harness isolation container and therefore requires Docker.
Failure logs remain in job output. Cluster state and credentials are not retained
as cache or artifact data.

## Branch-protection dry run

After the workflows have run on a pull request, verify discoverable contexts
without changing protection:

```bash
gh api repos/nunocgoncalves/iterabase-mono/commits/HEAD/check-runs \
  --jq '.check_runs[].name' | sort -u
gh api repos/nunocgoncalves/iterabase-mono/branches/master/protection/required_status_checks || true
```

The active `master` ruleset requires the two aggregate names at the top of this
document with strict up-to-date-branch enforcement. After the one-way cutover,
audit that live archived-state contract without changing it via:

```bash
make source-authority-audit
```
