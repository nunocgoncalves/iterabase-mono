# Exact source-history import

HOR-472 bootstraps this repository from four public product-source repositories.
The import preserves every existing source commit object: SHA, author and committer
identity, timestamps, message, parents, merges, tree, and ancestry are unchanged.
Only one new relocation commit is added above each pinned source head, followed by
an unrelated-history merge into the integration history.

The machine-readable record is [`history-import.tsv`](history-import.tsv). It pins
the source repository and branch, source head, relocation commit, import merge,
metadata sample, and representative path-history evidence.

## Reproduce the import

Start with an initialized `iterabase-mono` integration branch containing this root
map and the scripts, then run:

```sh
./scripts/import-history.sh
```

The script:

1. fetches only each public source repository's `master` branch with `--no-tags`;
2. refuses to continue if a source branch no longer equals its approved pinned head;
3. creates one branch directly from that unchanged head;
4. moves every tracked top-level entry below its bounded destination in one commit;
5. merges that relocated branch with `--no-ff --allow-unrelated-histories`.

Relocation and merge commit SHAs depend on the reproducer's commit identity and
time. The original source SHAs do not: the script verifies each pinned head before
adding any commits.

## Verify the import

Run the committed verification against the ticket branch or `master`:

```sh
./scripts/verify-history-import.sh HEAD
```

The verifier checks:

- each source head and metadata sample is an unchanged reachable commit object;
- each relocation directly parents its source head;
- each recorded import is a two-parent merge of its relocation branch;
- each current destination tree has the exact Git tree object ID of its source head;
- `git log --follow` and blame retain representative pre-relocation commits;
- only the four bounded component paths and root import documentation exist;
- all non-source ancestors are HOR-472 integration commits;
- no conflicting raw `v*` tag refs exist; and
- the reachable object graph passes `git fsck`.

Useful direct checks include:

```sh
# Exact ancestry
git merge-base --is-ancestor <source-head> master

# Exact current tree identity
test "$(git rev-parse <source-head>^{tree})" = \
  "$(git rev-parse master:<destination>)"

# Relocation-aware history and attribution
git log --follow -- <destination>/README.md
git blame master -- <destination>/README.md
```

Because a Git commit SHA identifies the complete commit object, resolving a sampled
source SHA unchanged also proves its original metadata, message, tree, and parents
are unchanged. `git show --no-patch --format=fuller <sample-sha>` displays that
evidence directly.

## Repository and release boundaries

No generic overlay, customer overlay, Iterabase deployment overlay, or marketing
site content or history is imported. The source repositories remain public,
writable, and authoritative until the later cutover ticket; this import neither
archives them nor changes release authority.

Existing source tags and releases remain in their original repositories. Raw
conflicting `v*` refs are not imported. New monorepo releases use these namespaces:

- `control-plane-v<semver>`
- `inference-gateway-v<semver>`
- `forge-v<semver>`
- charts retain `<chart>-<semver>`, such as `iterabase-platform-0.3.9`

This ticket makes no module, dependency, behavior, CI, release-workflow, workspace,
shared-testkit, or E2E ownership change.
