# Shared E2E execution contract

`testkit/e2e` owns the typed suite/stage runner, compiled catalogue, explicit
fixture records, runtime-bundle identity, and strict result schema. Product,
chart, browser, and Forge assertions remain in their owner modules.

## Compiled metadata

Every runnable F2/F3 scenario declares artifact requirements; PR, nightly, and
candidate routes; source/candidate fixture support; owner Make target; timeout;
release targets; mandatory capacity where applicable; and its stage DAG.
`make e2e-catalogue` compiles the real owner `TestE2E` registrations. A missing
route, artifact, target, fixture mode, timeout, or stage fails catalogue
validation.

## Required execution

Required CI sets:

- `ITERABASE_E2E_PLAN`
- `ITERABASE_E2E_RUNTIME_BUNDLE`
- `ITERABASE_E2E_SCENARIO_ID`
- `ITERABASE_E2E_RESULT`
- `ITERABASE_E2E_REQUIRED=true`

The runtime bundle must bind the exact source, plan and catalogue hashes, and the
scenario's exact artifact set. Selected temporary/candidate artifacts carry the
planned source SHA and recipe hash. Published baselines carry an immutable
reference plus verified digest/checksum and cannot claim selected-source
custody. Every image artifact also resolves to an archive/reference pair. F2
scenarios with image requirements must declare `create-kind` followed by
`import-runtime-images`; every later stage depends transitively on that import.
The shared Kind helper restores the downloaded archive and transports the exact
reference into the created nodes, so pre-cluster runner-daemon state cannot
silently satisfy an install. It verifies the archive's config digest and returns
the imported single-platform manifest digest. Owners then prove that workloads
requested the composer reference and bind it to the immutable identity exposed
by that CRI. Kind Pod status reports the imported manifest digest; K3s Pod status
reports the already-verified config digest, so Forge also proves the imported
tag-to-manifest mapping separately. Registry/index, config, and runtime-manifest
digests remain distinct identities rather than being compared as if they were
interchangeable.

Kind scenarios that create an `AgentPool` use the shared post-platform storage
helper before worker creation. It configures Kind's pinned provisioner onto the
synthetic dedicated workspace path, applies the Forge-owned non-default
`iterabase-agentpool-local-path` class, and verifies its parameter-free
`rancher.io/local-path` / `WaitForFirstConsumer` / `Delete` / no-expansion
contract. The helper never aliases the default class; production disk and
per-class path isolation remain Forge-owned.

The runner records exactly one terminal status for every declared stage. Failed
or skipped prerequisites block only dependents, so independent work,
diagnostics, and cleanup continue. In required execution any direct skip,
blocked/not-run stage, missing result, or incomplete artifact identity fails the
scenario and aggregate.

The result record binds scenario status, source/plan/catalogue/runtime/stage-graph
hashes, fixture mode, artifact identities, and every stage terminal status. F3
results additionally bind the permanent fixture capacity, pinned host-key hash,
configured workspace by-id device, and pre/post-cleanup boot IDs. GPU results
also bind the separate model-cache by-id device/mount/UUID plus the
repository-pinned public model revision and content hash; that device may not
alias the Forge workspace. Only the workflow aggregate reconciles the complete
result-artifact set against the generated plan.

## Validation

```bash
make testkit-test
python3 .github/scripts/test_e2e.py
python3 .github/scripts/e2e.py validate-contract
```
