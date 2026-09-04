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
work. The synthetic merge ref is not artifact source authority. Master runs
bind the same checks to the exact `github.sha`.
Temporary artifacts are retained only as GitHub Actions artifacts and Docker
state on the disposable runner; they are not semantically published.

`.github/scripts/collect_changed_paths.py` validates the event and full reachable
source/base commits, retains deletion paths and both sides of moves, and emits a
typed path record. `.github/scripts/select_ci.py` rejects empty, duplicated,
non-canonical, traversing, and unknown input and classifies exactly one of
`docs-only`, `release-only`, `selected`, or explicit manual `all`. Only a verified
non-empty docs-only record can produce the ordinary zero-work result. CI and E2E
aggregates re-check every selector boolean, matrix, selected/skipped job, and
`needs` member against that record or generated plan; missing or malformed output
fails closed. E2E scenario routing remains owned by `.github/scripts/e2e.py` and
the compiled owner catalogue.

## One compiled execution plan

Every runnable owner registration declares, in compiled Go metadata:

- scenario ID, owner, and Make target;
- PR and candidate routing;
- source/candidate fixture support;
- required runtime artifacts and release targets;
- timeout, capacity, and mandatory-capacity semantics; and
- the complete declared stage DAG.

`make e2e-catalogue` compiles this metadata from the real `TestE2E` entrypoints.
There is no hand-maintained scenario matrix. Contract validation fails when a
runnable registration lacks artifact requirements, any workflow route, a
supported fixture path, runtime metadata, candidate coverage, or a valid stage
graph.

The same schema represents two intents:

- **PR:** map every changed/deleted/moved path to affected artifacts, then select
  the conservative union of scenarios requiring those artifacts. Owner-suite,
  workflow, and shared-testkit changes fan out conservatively.
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

PR builders create each affected image, chart, companion, Forge binary,
and source-only runtime fixture once. Candidate builders use the same recipe
fields and add immutable candidate custody. `.github/inputs/remote-content.json`
inventories covered remote content: digest-pinned base and third-party runtime
images (including vLLM, Flux, NFD, and GPU Operator operands), checksummed
Go/Node/Buildx/GoReleaser/Syft and Kubernetes CI tools, Playwright's
browser/headless/FFmpeg archives, immutable BuildKit, SHA-256-pinned external
Helm archives, and the Forge K3s/Helm/Flux executable archives plus the K3s
airgap image set and service installer. It also names repository Go/npm/model
lock authorities. Chart builders do not trust mutable repository indexes: they
download an exact archive, fail
immediately on changed bytes, and only retry transport. Dockerfiles require
reviewed tag-plus-digest identities; Go tools install from the checked-in tools
module; third-party Actions use full commits. Product chart composition consumes
the already verified packages. Contract tests substitute bytes behind the same
URL and prove verification fails before materialization, extraction, packaging,
or execution.

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
PR plans and results are retained for 30 days. Candidate results and
artifact identities are retained in the 90-day immutable candidate record.

## Permanent fixture capacity and concurrency

Every compiled F3 registration names mandatory `cpu` or `gpu` capacity.
Selected execution first re-queries the live push/maintain/admin collaborator set
and requires the same-repository actor and complete writer set to equal only
founder `nunocgoncalves`; this gate runs before a fixture secret is materialized
or any host contact. Fork pull requests use `pull_request`, never
`pull_request_target`, receive no fixture key, and fail the repository/actor gate.
The authenticated security audit additionally requires exactly the two
fixture-scoped SSH secrets and rejects alternate provider or credential authority.
Only then does execution bind founder-configured variables and one key to a fixed
address, SSH user, pinned host key, and exact workspace device. Missing authority,
credentials, connectivity, reboot/purge, or identity fails with diagnostics and
never becomes a skip.

PR, master, and candidate work for a capacity use its literal
`iterabase-permanent-fixture-<capacity>` concurrency group with
`cancel-in-progress: false`. Work targeting the same host is serialized across
workflows; the independent CPU and GPU hosts may run concurrently. Build,
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

All third-party actions use immutable commit SHAs. Go/npm dependency locks,
repository Go-tool module sums, exact executable/archive checksums, base and
runtime image digests, external chart archive checksums, and Forge tool/runtime
checksums are validated bidirectionally through the remote-content inventory.
Tool-installing Actions are not delegated download authority: repository scripts
verify Go, Node, Buildx, GoReleaser, Syft, and Playwright runtime bytes before use. Caches contain only verified
dependency or build inputs and use content-addressed keys without broad fallback
keys.

Kind clusters, databases, mutable fixtures, runtime bundles, results,
credentials, customer data, and release evidence are never cached. There are no
automatic scenario retries, pass-on-retry semantics, or accepted flakes.

## Local contract validation

```bash
python3 .github/scripts/test_select_ci.py
python3 .github/scripts/test_e2e.py
python3 .github/scripts/test_release.py
python3 .github/scripts/test_remote_content.py
python3 .github/scripts/remote_content.py validate
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
