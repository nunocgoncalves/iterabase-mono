# Monorepo continuous integration

The stable branch-protection contexts are:

- `CI / required`
- `E2E / required`

Both run for every pull request. `CI / required` preserves path-aware static,
unit, integration, lint, build, image-smoke, protobuf, chart, harness-isolation,
and Forge checks. `E2E / required` is plan-driven: unselected capacity creates no
job, while every selected scenario and artifact must produce complete evidence.
A skipped, canceled, missing, extra, or superseded selected result cannot pass.

## Exact source authority

For a pull request, every planning, artifact, and scenario job checks out
`pull_request.head.sha` explicitly and verifies `git rev-parse HEAD` before doing
work. The synthetic merge ref is not artifact source authority. Scheduled and
complete manual runs bind the same checks to the exact scheduled `github.sha`.
Temporary artifacts are retained only as GitHub Actions artifacts and Docker
state on the disposable runner; they are not semantically published.

`.github/scripts/collect_changed_paths.py` retains deletion paths and both sides
of moves. `.github/scripts/select_ci.py` remains the static/unit owner selector.
E2E selection is owned only by `.github/scripts/e2e.py` and the compiled owner
catalogue.

## One compiled execution plan

Every runnable owner registration declares, in compiled Go metadata:

- scenario ID, owner, and Make target;
- PR, nightly, and candidate routing;
- source/candidate fixture support;
- required runtime artifacts and release targets;
- timeout, capacity, and mandatory-capacity semantics; and
- the complete declared stage DAG.

`make e2e-catalogue` compiles this metadata from the real `TestE2E` entrypoints.
There is no hand-maintained scenario matrix. Contract validation fails when a
runnable registration lacks artifact requirements, any workflow route, a
supported fixture path, runtime metadata, candidate coverage, or a valid stage
graph.

The same schema represents three intents:

- **PR:** map every changed/deleted/moved path to affected artifacts, then select
  the conservative union of scenarios requiring those artifacts. Owner-suite,
  workflow, and shared-testkit changes fan out conservatively.
- **Nightly/manual complete:** select every runnable F2/F3 registration exactly
  once and build every non-published runtime artifact once from the scheduled
  source SHA.
- **Candidate:** retain explicit founder-requested release targets, then select
  their conservative scenario union from the same compiled metadata. CI never
  chooses semantic release intent.

The retained plan includes the exact source SHA, catalogue hash, selected
scenario IDs, stage-graph hashes, artifact custody, build matrix, scenario
matrices, and capacity groups.

## Candidate-equivalent artifact recipes

`release/targets.json` is the single reviewed artifact and recipe authority for
both temporary validation and immutable candidates. It defines:

- Docker context, Dockerfile, build arguments, OCI labels, repository, and
  target/version authority for every product and validation image;
- Helm dependency-build and package inputs, including the platform companion;
- the pinned GoReleaser version/config and Forge platform contract; and
- explicit immutable published baselines and chart transition inputs.

PR/nightly builders create each affected image, chart, companion, Forge binary,
and source-only runtime fixture once. Candidate builders use the same recipe
fields and add immutable candidate custody. External Helm repository indexes and
locked dependency archives are acquired only by static checks or that single chart
builder, with bounded command-level transport attempts. Product chart scenario
composition and execution do not reacquire those dependency repositories; they
consume the already checksummed chart artifacts. Contract tests fail on context,
Dockerfile, argument, label,
dependency/package, companion, GoReleaser, version-authority, or recipe-hash
drift.

An affected PR artifact or founder-selected candidate target may never resolve
to a published baseline. Unselected dependencies may use only the explicit
published baseline in the recipe contract. Baseline resolution records and
later verifies image digests, chart checksums, and Forge archive checksums;
bumped-but-unpublished repository versions are not inferred as baselines.

## One runtime composer

Every F2/F3 job invokes `e2e.py compose` before its owner Make target. The
composer rejects:

- missing, extra, or duplicate artifact identities;
- wrong source SHA, recipe hash, digest, or checksum;
- selected-target or affected-artifact baseline substitution;
- unresolved or changed published baselines;
- incomplete control-plane, harness, tool-runner, inference, deterministic
  runtime, chart/companion, transition, or Forge evidence; and
- a plan or checkout that does not match the exact selected source.

It pulls or loads the verified bytes, composes selected nested charts into the
verified platform archive, materializes every resolved image archive/reference
plus real-machine chart archives and the exact Forge binary, and emits one
runtime-bundle manifest plus environment bindings. Every image-consuming F2 DAG
then creates Kind and runs the required `import-runtime-images` stage, which
restores each resolved archive and transports its exact reference into the new
cluster before any install stage. Loading the runner daemon before Kind exists
is not cluster transport. Fixture mode controls custody and identity only;
source, candidate, and explicit baselines use the same post-create import stage.
Owner scenarios no longer build artifacts or choose a different stage DAG in
source versus candidate mode. Image identity keeps three non-interchangeable
proofs: the temporary artifact or registry/index digest, the archive's image
config digest, and the post-import single-platform runtime manifest digest.
Import verifies the config and source-revision label and separately records the
imported tag's runtime manifest digest. Workload assertions always verify the
exact composer request reference and the immutable identity CRI exposes for that
substrate: Kind reports the imported manifest digest in Pod `imageID`, while
K3s reports the already-verified image config digest. The Forge assertion binds
that config identity to the same tag-to-manifest mapping established during
import; config and manifest digests are never compared as interchangeable.
Harness-bearing Kind DAGs additionally establish their AgentPool storage
substrate after platform-default claims bind and before worker creation. The
shared helper applies and verifies the Forge-owned non-default,
parameter-free `iterabase-agentpool-local-path` contract and dedicated
synthetic path; using Kind's default local-path class is not an accepted
fallback in any fixture mode.

## Strict scenario results

The shared runner writes one result record for every selected scenario. It binds:

- scenario ID and terminal status;
- plan, catalogue, runtime-bundle, source, and stage-graph identities;
- the complete observed artifact identity set;
- for F3, the permanent fixture capacity, pinned host-key hash, workspace by-id
  device, and changed pre/post-cleanup boot IDs;
- for GPU F3, the distinct model-cache device/mount/UUID and repository-pinned
  public model revision/content hash; and
- exactly one terminal status for every declared stage.

A passed scenario must record one observed post-import runtime manifest digest
for every image in its bundle; result assembly rejects a missing/extra identity
and retains artifact, config, and runtime digests separately. Each scenario
artifact also retains the composer-authored runtime bundle and the independent
post-import observation map. The aggregate hashes that retained bundle, compares
every plan-known reference/digest/checksum and every result artifact identity to
it, and requires each result runtime digest to match its retained observation.
A direct skip, blocked/not-run stage, missing report/bundle/observation,
missing/extra stage, mode-dependent successful no-op, or failed stage makes
required execution incomplete. Diagnostics and cleanup still run. Scenario jobs
upload results even on failure; the aggregate downloads the exact result-artifact
set and reconciles it against the plan rather than trusting matrix-job success
alone.

Failure diagnostics are separate, redacted artifacts retained for seven days.
PR/nightly plans and results are retained for 30 days. Candidate results and
artifact identities are retained in the 90-day immutable candidate record.

## Permanent fixture capacity and concurrency

Every compiled F3 registration names mandatory `cpu` or `gpu` capacity.
Selected execution binds founder-configured repository variables and one
fixture-scoped private-key secret to a fixed address, SSH user, pinned host key,
and exact workspace by-id device. Address, host identity, and devices are not
workflow-dispatch inputs. Missing credentials, unreachable hosts, reboot/purge
failure, or identity drift fails with retained diagnostics; it never calls
`t.Skip`. Unselected capacity creates no job.

All PR, nightly/manual, candidate, intentional-red, and cleanup/recovery fixture
work uses the one literal `iterabase-permanent-fixtures` concurrency group with
`cancel-in-progress: false`. CPU and GPU therefore cannot overlap. Build,
static, unit, fresh F2 Kind/browser, and other non-fixture exact-head work retain
safe parallelism.

Every selected scenario starts and ends with `forge destroy
--purge-workspace --reboot --yes`. The harness proves SSH disconnect/reconnect,
a changed boot ID, blank authorized workspace state, absence of stale K3s/run
state, and strict host-key verification. GPU readiness remains at the
NFD-published node-label/operator boundary: Forge pins the GPU Operator v26.3.3
subchart to the compatible NFD v0.19.0 image and a 30-second master full-resync
period, then requires the rendered image/argument and the normal operator/node
readiness evidence. GPU execution also validates the separate `/data/hf-cache`
volume and the model authority in
`forge/test/e2e/model-cache.json`; workspace purge never targets that device.
Actions has no provider API credential. An SSH-unrecoverable fixture stops F3
until founder-operated provider quarantine/recovery restores the documented
baseline.

## Cache and setup contract

All third-party actions use immutable commit SHAs. Go/Node/tool versions are
exact. Tool downloads are checksum verified. Caches contain only dependency or
build inputs and use content-addressed keys without broad fallback keys.

Kind clusters, databases, mutable fixtures, runtime bundles, results,
credentials, customer data, and release evidence are never cached. There are no
automatic scenario retries, pass-on-retry semantics, or accepted flakes.

## Local contract validation

```bash
python3 .github/scripts/test_select_ci.py
python3 .github/scripts/test_e2e.py
python3 .github/scripts/test_release.py
python3 .github/scripts/e2e.py validate-contract
make testkit-test
make release-check
```

Infrastructure scenarios still require Docker/Kind or the founder-configured
permanent CPU/GPU fixtures. The commands above validate selection, recipes,
composition contracts, strict result reconciliation, and compiled owner
entrypoints without contacting a fixture.

## Branch-protection audit

```bash
gh api repos/nunocgoncalves/iterabase-mono/commits/HEAD/check-runs \
  --jq '.check_runs[].name' | sort -u
gh api repos/nunocgoncalves/iterabase-mono/branches/master/protection/required_status_checks || true
```

The active `master` ruleset requires the two stable aggregate names with strict
up-to-date-branch enforcement. `make source-authority-audit` separately verifies
the archived legacy-source boundary.
