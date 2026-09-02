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
| 1 | `33650935838` / `100318535111` | `c8db97c5e846d8fd80c0ccd5be512ed2c4b1bc04` | `forge-digitalocean-cpu.json` in `e2e-result-capacity-cpu`; plan `c8ecc395387ad84377f2e72484fdc179f94ef35c12cb0a98adebf9ad73e7ebe8`; bundle `08a28d05337c03db3e63d45129b572f602a81135cff3c89fe46ffc70adf49ad4`; stages `2be258a3d81b155ae93d1577cc4414a4a853248e5a1652b0a8576047d5728d2b` | `a8c17485-86f6-4aa3-accc-6375c148bdd2` → `ad7a3b6b-4de4-4a8a-92ea-42b6dd806824` → `018afb2f-108a-4a8a-bad5-62a747178b0f` | `/dev/disk/by-id/scsi-0DO_Volume_iterabase-ci-cpu-workspace`; host key SHA-256 `8e4798d8da643e7c872bc96ddb44f718311d1cf650c4eb11a62b62d7deb75dbe` | Passed: exact platform, edge, and migration-source assertions passed on the resized fixture; post-test purge/reboot passed. |
| 2 | `33650935838` / `100318535111` | `c8db97c5e846d8fd80c0ccd5be512ed2c4b1bc04` | `forge-digitalocean-workspace.json` in `e2e-result-capacity-cpu`; plan `c8ecc395387ad84377f2e72484fdc179f94ef35c12cb0a98adebf9ad73e7ebe8`; bundle `0d26af7ab69f8dd41a578a652f92a711a8631973e26d061f3270704dac2bc2b8`; stages `0bb8a6e3a987e58652eeee5a2b22e1106dd5713b3c3b7a4ceb271655b7088adf` | `018afb2f-108a-4a8a-bad5-62a747178b0f` → `0bc84740-69bb-43f4-811b-29ec33d8d84f` → `0c63fbff-4513-482d-8aab-504fe8e96ce5` | `/dev/disk/by-id/scsi-0DO_Volume_iterabase-ci-cpu-workspace`; host key SHA-256 `8e4798d8da643e7c872bc96ddb44f718311d1cf650c4eb11a62b62d7deb75dbe` | Passed: two authenticated concurrent workers reached the shared-workspace barrier and retained isolation; post-test purge/reboot passed. |
| 3 | `33657855724` / `100341858839` | `e756f5f3e5426cbbe4cee2662b53a7430eaca11d` | `forge-digitalocean-cpu.json` in `e2e-result-capacity-cpu`; plan `c43ef9e05b0ca97c3229136bd6cb60b1962457f3a8aeaaeefbe76945cdab3ebc`; bundle `a30030bba3f44eae2844195a382ab55158f6699289bfa2e694abbf85b0c585a5`; stages `2be258a3d81b155ae93d1577cc4414a4a853248e5a1652b0a8576047d5728d2b` | `0c63fbff-4513-482d-8aab-504fe8e96ce5` → `129537b2-6a3c-453b-a60e-d952fd52c54e` → `bfec9c3a-030a-4c3d-99c9-ce60b1deb6a5` | `/dev/disk/by-id/scsi-0DO_Volume_iterabase-ci-cpu-workspace`; host key SHA-256 `8e4798d8da643e7c872bc96ddb44f718311d1cf650c4eb11a62b62d7deb75dbe` | Passed: exact platform, edge, and migration-source assertions plus post-test purge/reboot passed. |
| 4 | `33657855724` / `100341858839` | `e756f5f3e5426cbbe4cee2662b53a7430eaca11d` | `forge-digitalocean-workspace.json` in `e2e-result-capacity-cpu`; plan `c43ef9e05b0ca97c3229136bd6cb60b1962457f3a8aeaaeefbe76945cdab3ebc`; bundle `11134aea3e21ede49b8a605c7d6ef975b73dd184ae4c701d14e760541e4c2c11`; stages `0bb8a6e3a987e58652eeee5a2b22e1106dd5713b3c3b7a4ceb271655b7088adf` | `bfec9c3a-030a-4c3d-99c9-ce60b1deb6a5` → `f6b174db-f283-45eb-82f8-ecef313f8819` → `23199196-ca98-49d6-a36a-d871bd3ae29e` | `/dev/disk/by-id/scsi-0DO_Volume_iterabase-ci-cpu-workspace`; host key SHA-256 `8e4798d8da643e7c872bc96ddb44f718311d1cf650c4eb11a62b62d7deb75dbe` | Passed: authenticated concurrent workspace barrier/isolation and post-test purge/reboot passed. |

Current consecutive count: **4**.

## GPU consecutive-success evidence

Pinned model authority:

- model: `Qwen/Qwen3.5-0.8B`
- revision: `2fc06364715b967f1860aea9cf38778875588b17`
- selected weight SHA-256:
  `04b1c301231dd422b8860db31311ab2721511346a32cb1e079c4c4e5f1fe4696`

| Streak | Workflow run / job | Source SHA | Plan/result artifact | Pre/post boot IDs | Workspace + model-cache identity | Result |
| --- | --- | --- | --- | --- | --- | --- |
| Prior 1 (reset) | `33639133006` / `100278539694` | `edfd32f303a47742d28cc70b2150c1898443c020` | `e2e-result-capacity-gpu`; plan `b8250447acb04e41da1a19c11985a59b629da63662f9779106cca469d8ecd4fc`; bundle `6f4ebc24a5660b576ec647075441c8aae8133971d5ce236c73cc4b679c9dc478`; stages `4d83e55aa22cc91e78a4b796d26dba774ced34dfa2a388fe1b6a41a4f4eb468e` | `55f7e57a-8b10-49d2-a8b8-a45d6566124d` → `3367515b-5e86-4c2a-bb80-65f81c2f7a01` → `5a63bd48-7c91-453c-874f-fd0ec920620a` | workspace `/dev/disk/by-id/virtio-5b0889ae-a1d9-4e0d-b`; host key SHA-256 `9460d21d576b58e898a0bf68d8c1f7690e3101b4b0da5d467aa29cea0e906678`; cache `/dev/disk/by-id/virtio-43ff9b5b-1f97-49ba-9`, UUID `2eb63d10-3d60-418e-bced-cae2f3a26f08`, revision `2fc06364715b967f1860aea9cf38778875588b17`, content `04b1c301231dd422b8860db31311ab2721511346a32cb1e079c4c4e5f1fe4696` | Passed: Forge restored deliberately absent `dkms`; baseline and candidate driver/runtime transitions, exact tool runner, and real model inference passed; post-test purge/reboot passed. |
| Prior 2 (reset) | `33650935838` / `100318534005` | `c8db97c5e846d8fd80c0ccd5be512ed2c4b1bc04` | `forge-digitalocean-gpu.json` in `e2e-result-capacity-gpu`; plan `c8ecc395387ad84377f2e72484fdc179f94ef35c12cb0a98adebf9ad73e7ebe8`; bundle `44b4675fe89bf3ab8baf089af6395939740d573a023e1da06329b07bd8d1c7af`; stages `4d83e55aa22cc91e78a4b796d26dba774ced34dfa2a388fe1b6a41a4f4eb468e` | `5a63bd48-7c91-453c-874f-fd0ec920620a` → `45f55799-f2a4-4675-8d42-652e732f341b` → `a09a234f-3349-42bd-b43a-5ae4cc2fa62a` | workspace `/dev/disk/by-id/virtio-5b0889ae-a1d9-4e0d-b`; host key SHA-256 `9460d21d576b58e898a0bf68d8c1f7690e3101b4b0da5d467aa29cea0e906678`; cache `/dev/disk/by-id/virtio-43ff9b5b-1f97-49ba-9`, UUID `2eb63d10-3d60-418e-bced-cae2f3a26f08`, revision `2fc06364715b967f1860aea9cf38778875588b17`, content `04b1c301231dd422b8860db31311ab2721511346a32cb1e079c4c4e5f1fe4696` | Passed: Forge again restored deliberately absent `dkms`; both driver/runtime transitions, exact tool runner, real model inference, and post-test purge/reboot passed. |
| 1 | Pending | Pending | Pending | Pending | Pending | Pending |
| 2 | Pending | Pending | Pending | Pending | Pending | Pending |
| 3 | Pending | Pending | Pending | Pending | Pending | Pending |

Current consecutive count: **0**. Run `33657855724` reset the prior two-cycle streak.

## Non-qualifying/reset evidence

These attempts remain failures; none starts or advances a streak:

| Workflow run / job | Source SHA | Capacity | Reset reason |
| --- | --- | --- | --- |
| `33609868347` | `a6ce9b8203e100db151b759139caa6d773c47d14` | CPU + GPU | Both pinned sessions failed closed on host-key algorithm negotiation. |
| `33611404151` / `100187936569` | `4c32f315ab8776182877611b6f90582d3ae801e2` | CPU | The reserved-IP NAT path did not expose the edge listener and the concurrent workspace assertion never reached its two-arrival barrier. |
| `33611404151` / `100187937276` | `4c32f315ab8776182877611b6f90582d3ae801e2` | GPU | GPU Operator driver installation could not resolve the running kernel; dependent runtime and inference stages did not run. |
| `33620416814` / `100216730899` | `332fbb718df81deef85ff5592ff854da4156f461` | CPU | The primary public interface fixed the edge path and the complete `digitalocean-cpu` scenario passed, but `digitalocean-workspace` again failed its two-arrival barrier on the 4 GiB fixture. |
| `33620416814` / `100216731899` | `332fbb718df81deef85ff5592ff854da4156f461` | GPU | Forge restored the deliberately removed `dkms` package, but the driver container could not resolve retired `linux-headers-6.11.0-26-generic`; no driver/runtime/inference assertion passed. |
| `33626838744` / `100237361949` | `8aca830a29ee464e80cc126ba6ea1b994ab34c18` | CPU | `digitalocean-workspace` again failed its two-arrival barrier on the pre-resize 4 GiB fixture. |
| `33626838744` / `100237362539` | `8aca830a29ee464e80cc126ba6ea1b994ab34c18` | GPU | Forge restored `dkms`; exact kernel resolution, GPU driver 580.126.20, runtime toolkit, device plugin, validator, and ClusterPolicy readiness passed. The first client-go smoke then failed because the provider does not expose public port 6443; dependent workload/upgrade/inference stages did not run. |
| `33632117921` / `100254864975` | `fe3161b45ba8d5e9a03df54319f1925903b211af` | CPU | `digitalocean-workspace` again failed its two-arrival barrier on the pre-resize 4 GiB fixture. |
| `33632117921` / `100254863954` | `fe3161b45ba8d5e9a03df54319f1925903b211af` | GPU | The pinned SSH API tunnel passed the real GPU smoke and the complete 580.126.20 → 595.71.05 driver/runtime transition. Platform apply then failed closed because the scenario had not selected/imported the chart's tool-runner image and its obsolete published fallback was unavailable; model inference did not run. |
| `33657855724` / `100341857789` | `e756f5f3e5426cbbe4cee2662b53a7430eaca11d` | GPU | SSH and purge/reboot succeeded, but the permanent harness handed Forge the host before `cloud-final` completed. The first operating-system probe was not usable, Forge reported an empty OS identity, and the run failed before restoring `dkms`; post-failure purge/reboot succeeded. This resets the prior GPU streak to zero. |

Before `33626838744`, the Ubuntu HWE baseline was updated and rebooted to
`7.0.0-30-generic`, whose exact header package remains available from the
configured Ubuntu archive. Before each qualifying GPU run, empty `dkms status`
and absence of any host-managed NVIDIA DKMS module were reverified. After each
qualifying run, only the Forge-installed `dkms` package was removed (no
`apt autoremove`);
matching headers and `build-essential` were retained. `dkms` is deliberately
absent so the next exact Forge apply must restore and verify it before GPU
Operator reconciliation.

Before `33650935838`, founder-approved provider maintenance resized the permanent
CPU fixture from `s-2vcpu-4gb` to `s-4vcpu-8gb` with the root disk left at 80 GiB.
The fixed primary address, pinned SSH identity, dedicated workspace volume, and
clean baseline were reverified after power-on. The temporary local-only Full
Access token was removed from mode-0600 `forge/.env`; the founder then permanently
deleted the named token server-side on 2026-09-02 and verified its absence from
the provider token inventory.

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
