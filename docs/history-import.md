# Source history provenance

HOR-472 assembled this repository from four public product-source repositories.
Every original commit object remains unchanged, including its SHA, identities,
timestamps, message, parents, merges, tree, and ancestry. Each source has one new
relocation commit followed by an unrelated-history merge.

## Import record

| Destination | Source (`master`) | Source head | Relocation | Merge |
| --- | --- | --- | --- | --- |
| `control-plane/` | `nunocgoncalves/control-plane` | `c63eea9d21c367a3e5fd91431bedc853fb15a16b` | `cbed62e1596aeee913e00afe4b46a5b3d4ead874` | `1997552a87cb8a1feeff472bbb3c4d4744aedfae` |
| `inference-gateway/` | `nunocgoncalves/inference-gateway` | `cf093df2cdca30e916cb340d3e5dc1ab29c49989` | `47acdd90cff468bc921456b87f95032c57c87f89` | `57eeee901d57f5188cd4b6836d3339c51688530d` |
| `forge/` | `nunocgoncalves/forge` | `56afae7b21f97a1c40c81705954756ef16f46674` | `bcf52a3bb088eb1ae06e78951e4721970ee32269` | `627c2e02baacd384b5bb870d369b3222b6e0a639` |
| `charts/` | `nunocgoncalves/iterabase-charts` | `0d97d50962afcd03aa474f096a8948f0e1dcd8b5` | `f609428b137a486c04da4bdca45159f43abc3f3b` | `7579abd5434f0cebba07cdb6da99037015c74cec` |

Representative evidence:

| Destination | Metadata sample | `log --follow` commit | Blame commit | Path |
| --- | --- | --- | --- | --- |
| `control-plane/` | `c63eea9d21c367a3e5fd91431bedc853fb15a16b` | `e3a87548d8813b835a0a00fce7ceb0c5674f43ab` | `160d25d0a2a8c1a81d65444891afe867f2d10337` | `README.md` |
| `inference-gateway/` | `984700bcd62dd81dfb895690f438555cfb2c5fb7` | `d315d36d7303e9578f0acff18b689c6c8219bc3f` | `9a5df576ec06f029349ac5555545bd042560344b` | `README.md` |
| `forge/` | `56afae7b21f97a1c40c81705954756ef16f46674` | `4692f83846383bee9d5e56fab1f90101aa329b4e` | `7f408695028f5e42f8af5eced8a2fdb1cbbffd3e` | `README.md` |
| `charts/` | `0d97d50962afcd03aa474f096a8948f0e1dcd8b5` | `276f4194c6b3a56f0a7d0398682355b8868fa414` | `6587609c583b29446ecee7bac2e435457064b610` | `README.md` |

## Reproduce

From a clean integration branch containing the root documentation, repeat the
following for each import-record row:

```bash
repository=control-plane
destination=control-plane
expected_head=c63eea9d21c367a3e5fd91431bedc853fb15a16b
integration_branch=$(git branch --show-current)
remote_url="https://github.com/nunocgoncalves/${repository}.git"
relocation_branch="import/HOR-472-${destination}"

# Fetch only master and refuse a changed source head.
git fetch --no-tags "$remote_url" master
test "$(git rev-parse FETCH_HEAD)" = "$expected_head"

# Relocate every tracked root entry in one commit. The staging name also handles
# iterabase-charts, whose source tree already contains a top-level charts/ path.
git switch --create "$relocation_branch" "$expected_head"
mkdir .history-relocation
while IFS= read -r -d '' entry; do
  git mv -- "$entry" .history-relocation/
done < <(git ls-tree -z --name-only HEAD)
mv .history-relocation "$destination"
git add -A
git commit -m "HOR-472: relocate ${repository} under ${destination}/"

# Preserve the relocated source graph as the second parent of an explicit merge.
git switch "$integration_branch"
git merge --no-ff --allow-unrelated-histories "$relocation_branch" \
  -m "HOR-472: merge relocated ${repository} history"
```

Use `inference-gateway`, `forge`, and `iterabase-charts` with their recorded heads
for the remaining rows; the `iterabase-charts` destination is `charts`. New
relocation and merge SHAs vary with commit identity and time, while every original
source SHA remains unchanged.

## Verify

For each import-record row, substitute the recorded values below:

```sh
tip=HEAD
destination=control-plane
source_head=c63eea9d21c367a3e5fd91431bedc853fb15a16b
relocation=cbed62e1596aeee913e00afe4b46a5b3d4ead874
merge=1997552a87cb8a1feeff472bbb3c4d4744aedfae
sample=c63eea9d21c367a3e5fd91431bedc853fb15a16b
follow=e3a87548d8813b835a0a00fce7ceb0c5674f43ab
blame=160d25d0a2a8c1a81d65444891afe867f2d10337

# Unchanged objects, current ancestry, relocation, merge shape, and the exact
# imported tree before later monorepo changes modified the destination.
git cat-file -e "${source_head}^{commit}"
git cat-file -e "${sample}^{commit}"
git merge-base --is-ancestor "$source_head" "$tip"
git merge-base --is-ancestor "$sample" "$source_head"
test "$(git rev-parse "${relocation}^")" = "$source_head"
test "$(git rev-list --parents -n 1 "$merge" | wc -w | tr -d ' ')" = 3
test "$(git rev-parse "${merge}^2")" = "$relocation"
test "$(git rev-parse "${source_head}^{tree}")" = \
  "$(git rev-parse "${relocation}:${destination}")"

# Representative history and attribution still cross the relocation at the
# current tip.
git log --follow --format='%H' "$tip" -- "${destination}/README.md" \
  | grep -Fx "$follow"
git blame --line-porcelain "$tip" -- "${destination}/README.md" \
  | grep -E "^${blame} "
```

A Git commit SHA identifies the complete commit object, so resolving a sampled SHA
unchanged also proves its original metadata, message, tree, and parents. Display it
with `git show --no-patch --format=fuller <sample-sha>`.

At the accepted HOR-472 import fixed point, the root tree contained only
`.gitleaks.toml`, `README.md`, `docs/`, and the four component destinations.
Later project slices deliberately added root workspace, automation, testkit, and
release files. Verify the historical boundary and scan all currently reachable
history with:

```sh
import_acceptance=f54994e3936bb2162966365ae23138565b201dbb
test "$(git ls-tree --name-only "$import_acceptance" | sort)" = \
"$(printf '%s\n' .gitleaks.toml README.md charts control-plane docs forge inference-gateway | sort)"
git fsck --full --no-dangling
test -z "$(git tag --list 'v*')"
gitleaks git --config .gitleaks.toml .
```

The Gitleaks allowlist covers only the literal `your-admin-secret` and
`ml-YOUR_KEY_HERE` documentation placeholders in preserved inference-gateway
history.

## Boundaries and tags

Deployment overlays and marketing content and history were not imported. The
source repositories remained writable release authorities at import time; their
existing tags and releases remain there. No conflicting raw `v*` refs were
imported. HOR-474 subsequently froze and archived those repositories at the
recorded source heads. They now preserve history only; [`source-authority.md`](source-authority.md)
defines the sole writable source and verifies that historical links remain
accessible.

New monorepo releases use `control-plane-v<semver>`,
`inference-gateway-v<semver>`, and `forge-v<semver>`. Charts retain
`<chart>-<semver>`, such as `iterabase-platform-0.3.9`.
