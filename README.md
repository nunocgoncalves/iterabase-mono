# iterabase-mono

Public monorepo for Iterabase product source.

| Path | Component | Purpose |
| --- | --- | --- |
| [`control-plane/`](control-plane/) | Control plane | Product API, operator, dashboard, harness, and tool runner |
| [`inference-gateway/`](inference-gateway/) | Inference gateway | OpenAI-compatible inference gateway |
| [`forge/`](forge/) | Forge | Host and k3s substrate installer |
| [`charts/`](charts/) | Charts | Helm chart sources |

Deployment overlays and the marketing site remain separate repositories and are not
part of this monorepo.
