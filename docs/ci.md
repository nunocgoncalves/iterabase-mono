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
| control-plane `ci / harness` | `CI / harness` | Ported |
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

Component release workflows are intentionally not activated from their nested
locations. Candidate construction, promotion, and namespaced release triggers
belong to HOR-473. Forge's scheduled droplet reaper remains active in the legacy
Forge repository until source-authority cutover in HOR-474; it is operational
cleanup rather than a source quality gate. These are documented deferrals, not
silent CI removals.

## Path ownership

`.github/scripts/collect_changed_paths.py` is the shared diff collector for both
workflows. It includes deletions and disables rename collapsing so both sides of
a move retain their owners. `.github/scripts/select_ci.py` is the owner-mapping
source of truth, and its tests prove deletion-only, cross-owner move, docs-only,
single-component, shared-contract, and cross-component changes.

- Root workflow, root Makefile, and Go workspace metadata fan out to every owner.
- Control-plane UI, harness, tool-runner, and protobuf contracts select their
  focused checks. Protobuf changes fan out to both generated Node consumers.
- Forge source/E2E changes retain the existing Forge unit, real-machine, and
  deterministic Kind gates.
- Chart changes run chart static/runtime checks and the current local-chart Kind
  scenarios. This is source composition from one checkout; no matching-branch
  lookup or cross-repository checkout exists.
- Markdown and repository documentation alone select no expensive owner job.

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

Only the user should update branch protection. The intended required contexts
are the two aggregate names at the top of this document.
