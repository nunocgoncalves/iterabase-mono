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
custody.

The runner records exactly one terminal status for every declared stage. Failed
or skipped prerequisites block only dependents, so independent work,
diagnostics, and cleanup continue. In required execution any direct skip,
blocked/not-run stage, missing result, or incomplete artifact identity fails the
scenario and aggregate.

The result record binds scenario status, source/plan/catalogue/runtime/stage-graph
hashes, fixture mode, artifact identities, and every stage terminal status. Only
the workflow aggregate reconciles the complete result-artifact set against the
generated plan.

## Validation

```bash
make testkit-test
python3 .github/scripts/test_e2e.py
python3 .github/scripts/e2e.py validate-contract
```
