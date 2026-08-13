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

## Source authority

This is the sole writable public source for the four product components. Clone
and branch from this repository for every product change:

```bash
git clone https://github.com/nunocgoncalves/iterabase-mono.git
cd iterabase-mono
git switch -c HOR-123-short-description origin/master
```

The former standalone component repositories are public read-only archives at
the exact heads imported into this history. Do not open product pull requests or
run releases there. Existing image and chart package names remain unchanged so
overlays continue consuming the same immutable artifact identities. See
[`docs/source-authority.md`](docs/source-authority.md) for the authority audit,
historical evidence, and emergency boundary.

## Go workspace

The committed [`go.work`](go.work) supports atomic changes while preserving four
independently buildable modules:

| Module directory | Module path |
| --- | --- |
| `control-plane/` | `github.com/nunocgoncalves/iterabase-mono/control-plane` |
| `inference-gateway/` | `github.com/nunocgoncalves/iterabase-mono/inference-gateway` |
| `forge/` | `github.com/nunocgoncalves/iterabase-mono/forge` |
| `forge/test/e2e/` | `github.com/nunocgoncalves/iterabase-mono/forge/test/e2e` |

Run the atomic local matrix from the repository root:

```bash
make workspace-check  # go work sync freshness + go list for every module
make build            # production binaries
make test             # module tests; Docker required for integration tests
make lint
make codegen-check    # protobuf lint/regeneration freshness
make charts-check     # Helm and rendered-resource checks
```

Use `make -C <component> <target>` for a narrower component command. See the
root [`AGENTS.md`](AGENTS.md) ownership map before exploring code and the scoped
`AGENTS.md` in the owning component for prerequisites and architecture rules.

Docker builds retain their runtime identities and use component-scoped contexts:

```bash
make docker-build  # control-plane, harness, isolation, tool-runner, and inference-gateway
```

Forge's source-composed E2E inputs are the local `control-plane/` and `charts/`
directories. Cross-repository matching-branch checkouts are not part of local
monorepo development. Published artifact names, binaries, charts, images,
configuration, and runtime behavior remain unchanged.

A merge to `master` does not release. If ticket acceptance requires semantic
publication, use the explicit affected-target candidate and founder-approved
promotion flow documented in [`docs/release.md`](docs/release.md).
