# HOR-540 permanent-fixture lifecycle acceptance — 2026-09-01

Status: **qualification in progress on PR #71; legacy removal is not yet
authorized by this record**.

Authority: HOR-540, `DES-HOR-540-01`, and `DES-HOR-540-02`. Qualification source
began from PR #71 head `886890d48e1190030302727eef3c0e4196eaa5fe` after required
run `33530234287` proved exact bundle composition but dynamic GPU capacity
failed honestly.

## Acceptance rule

Before ephemeral provisioning or tagged reaping is deleted, this record must
contain at least three consecutive successful CPU and three consecutive
successful GPU cycles. Each cycle must execute, under the literal
`iterabase-permanent-fixtures` non-canceling lock:

```text
destroy --purge-workspace --reboot --yes
→ exact apply
→ complete selected fixture test
→ destroy --purge-workspace --reboot --yes
```

Any failed, canceled, skipped, superseded, or incomplete cycle resets that
fixture's consecutive-success count. A rerun does not convert the failed attempt
into part of the streak. Each retained result must bind source SHA, plan/runtime
bundle/artifact identities, stage graph, pinned host-key hash, workspace by-id
device, before/after boot IDs, and—on GPU—the separate model-cache
by-id/mount/UUID/revision/content hash.

## CPU consecutive-success evidence

| Streak | Workflow run / job | Source SHA | Plan/result artifact | Pre/post boot IDs | Workspace identity | Result |
| --- | --- | --- | --- | --- | --- | --- |
| 1 | Pending | Pending | Pending | Pending | Pending | Pending |
| 2 | Pending | Pending | Pending | Pending | Pending | Pending |
| 3 | Pending | Pending | Pending | Pending | Pending | Pending |

Current consecutive count: **0**.

## GPU consecutive-success evidence

Pinned model authority:

- model: `Qwen/Qwen3.5-0.8B`
- revision: `2fc06364715b967f1860aea9cf38778875588b17`
- selected weight SHA-256:
  `f0140d845aced424f17b1c75ebc5a67ef75fe309c68d2f613acda2eb551db7dd`

| Streak | Workflow run / job | Source SHA | Plan/result artifact | Pre/post boot IDs | Workspace + model-cache identity | Result |
| --- | --- | --- | --- | --- | --- | --- |
| 1 | Pending | Pending | Pending | Pending | Pending | Pending |
| 2 | Pending | Pending | Pending | Pending | Pending | Pending |
| 3 | Pending | Pending | Pending | Pending | Pending | Pending |

Current consecutive count: **0**.

## Required negative evidence

| Proof | Run/job or local contract | Result |
| --- | --- | --- |
| Wrong/root/system/partition/in-use/workspace identity refuses purge | Forge unit + fake-SSH suite | Pending final SHA |
| Ordinary `forge destroy` preserves workspace and never implies reboot | Forge lifecycle suite | Pending final SHA |
| Corrupt/missing/wrong-device GPU cache fails closed | Forge harness unit suite | Pending final SHA |
| Forced scenario assertion retains diagnostics and fails selected gate | Pending live run | Pending |
| Forced cleanup/reboot failure fails selected gate | Pending live run | Pending |
| Interrupted run recovers through next serialized preflight when SSH is healthy | Pending live run | Pending |
| Intentional HOR-411 policy mutation remains red-detecting | Pending live run | Pending |

## Legacy-removal and final audit gate

Do not mark this section complete before both streak tables are green.

- [ ] Delete DigitalOcean SDK/provisioning/capacity discovery and provider tests.
- [ ] Delete tagged reaper code and `.github/workflows/reaper.yml`.
- [ ] Remove `FORGE_E2E_KEEP`/dirty-host retention semantics.
- [ ] Remove every `DIGITALOCEAN_TOKEN` workflow/configuration reference.
- [ ] Founder removes the GitHub Actions secret after diff and streak review.
- [ ] `gh secret list` proves no provider token remains.
- [ ] Repository scan proves no provider API mutation path remains.
- [ ] Exact-head `CI / required` and `E2E / required` pass on the final head.
- [ ] Complete-catalogue permanent-fixture run passes after legacy removal.
- [ ] Non-promoted all-target candidate rehearsal passes and retains fixture,
      stage, and exact artifact identities.

## Publication and rollback

Semantic publication is **none** for HOR-540 acceptance. The all-target candidate
is validation-only and must not be promoted.

Rollback stops fixture-backed dispatch, verifies cleanup where pinned SSH is
healthy, and quarantines fixtures. Provider recovery/reimage is founder-operated.
Rollback must not restore `DIGITALOCEAN_TOKEN`, dynamic provisioning, tagged
reaping, insecure host-key behavior, or destructive ordinary destroy semantics.
