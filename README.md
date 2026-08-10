# iterabase-mono

Public monorepo for Iterabase product source. HOR-472 imported the exact `master`
histories of four repositories without rewriting their existing commits.

| Path | Imported repository | Purpose |
| --- | --- | --- |
| [`control-plane/`](control-plane/) | [`nunocgoncalves/control-plane`](https://github.com/nunocgoncalves/control-plane) | Product API, operator, dashboard, harness, and tool runner |
| [`inference-gateway/`](inference-gateway/) | [`nunocgoncalves/inference-gateway`](https://github.com/nunocgoncalves/inference-gateway) | OpenAI-compatible inference gateway |
| [`forge/`](forge/) | [`nunocgoncalves/forge`](https://github.com/nunocgoncalves/forge) | Host and k3s substrate installer |
| [`charts/`](charts/) | [`nunocgoncalves/iterabase-charts`](https://github.com/nunocgoncalves/iterabase-charts) | Helm chart source repository |

Generic and customer overlays—including the Iterabase deployment overlay—and the
marketing site are intentionally excluded. Their content and history remain in
separate repositories.

See [`docs/history-import.md`](docs/history-import.md) for the pinned source heads,
import procedure, verification, tag policy, and source-authority boundary.
