# Project operating instructions — iterabase-charts

## Git, CI/CD, and source-of-truth workflow

- Direct pushes to `master` are prohibited.
- Each Linear ticket must be scoped to its own branch.
- Branch names, commit messages, and pull request titles must include the Linear ticket identifier, for example `HOR-239-umbrella-chart`, `HOR-239 add redis subchart`, and `HOR-239 — Add redis subchart`.
- Commit to the ticket branch as work progresses and as commits make sense.
- When work is ready for review, open a pull request; do not merge it yourself.
- Pull request descriptions must be valid Markdown with real line breaks, not escaped `\n` text; when using `gh`, write the body to a file and use `--body-file` for both create/edit operations.
- Pull request descriptions should use this structure: `## Summary`, `## Validation`, `## Production impact`, and `## Ticket state`; include concise bullets under each heading and mark non-applicable sections as `None` or `N/A`.
- A ticket is not considered complete/closable until its branch has been merged to `master` and required workflows have passed.
- Linear is the source of truth for ticket state, ownership, sequencing, and completion status.
- A pull request is not considered addressable-complete (review addressed, ready to merge) until CI is green on the PR's branch. After pushing changes that should resolve review findings or CI failures, re-check that CI passes before declaring the work done or asking the user to merge; if CI is red, fix it before finishing. Never mark a review round or a ticket complete while CI is failing.

## Charts development

- Charts live under `charts/<chart-name>/`. The umbrella is `charts/iterabase-platform/`.
- Local prerequisites: `helm` and `kubeconform`.
- `make check` runs `helm lint` (every chart) + `helm template` (umbrella) + `kubeconform` on the rendered output. Run it before pushing.
- `make build-deps` resolves the umbrella's `file://` local subcharts + the external `ingress-nginx` dep into `charts/iterabase-platform/charts/` (gitignored). CI rebuilds it.
- Every subchart wraps its resources in `{{- if .Values.enabled }}`; the umbrella controls them via `condition: <chart>.enabled`.
- Secrets are chart-generated and stable across upgrades (helm `lookup` pattern) — never commit real secrets.
- Releasing: per-chart tags `<chart>-<semver>` publish to `oci://ghcr.io/nunocgoncalves/<chart>` (umbrella + the 3 service charts; data-dep subcharts are bundled only).
