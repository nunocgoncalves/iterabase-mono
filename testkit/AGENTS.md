# Shared E2E testkit instructions

Read the root [`AGENTS.md`](../AGENTS.md) first. Its Git, ticket, validation, release, and architecture-approval rules apply here.

## Scope

`testkit/e2e` owns only reusable Iterabase test mechanics: typed suite/stage execution, exact fixture records, process/Kind/Kubernetes/Helm/HTTP orchestration, diagnostics/redaction, component artifact policy, the Playwright process seam, and compiled catalogue discovery. Product, chart, browser, and Forge assertions remain in their owning directories.

The module is `github.com/nunocgoncalves/iterabase-mono/testkit/e2e`. Owner suites consume it from `<owner>/test/e2e`; do not move owner behavior into this module.

## Commands

```bash
make testkit-test       # shared race tests, owner examples, JSON/Markdown catalogue
make testkit-kind-example # explicit real Kind lifecycle validation; requires Docker
make e2e-catalogue      # emit JSON from compiled TestE2E registrations
make e2e-catalogue-check
```

## Invariants

- No automatic scenario/assertion retry or pass-on-retry behavior.
- Every process and poll is bounded; observation errors fail immediately.
- Fixture mode is explicit (`source`, `candidate`, or `published`) and floating `latest` is rejected.
- Stage dependencies are acyclic and backward-declared; failed/skipped prerequisites block only their dependents.
- Diagnostics and cleanup continue after failures.
- Text evidence passes through shared redaction. Opaque evidence is rejected unless explicitly declared safe synthetic content.
- The catalogue is emitted from compiled owner registrations; never add a parallel manual coverage mapping.
- Release target selection remains a conservative union of compiled metadata. CPU/GPU capacity marked mandatory cannot pass by skipping.

Changes to these failure, fixture, security, ownership, or release-selection semantics are architectural and require explicit user approval.
