# Permanent CPU/GPU E2E fixture operations

Authority: `DES-HOR-540-02`. These hosts are dedicated, reimageable CI fixtures;
they contain no customer data. All fixture-backed work is globally serialized
by `iterabase-permanent-fixtures` with cancellation disabled.

## Security and ownership boundary

- Actions receives one fixture-scoped SSH key per host. It receives no provider
  account/API credential.
- Repository-controlled code has root-equivalent authority on only these
  dedicated hosts. This is not a malicious-same-repository-code isolation
  guarantee.
- The founder alone provisions, quarantines, power-cycles, rescues, reimages,
  replaces, or deletes fixtures through the provider.
- The CPU and GPU Forge workspace disks contain disposable test state. The GPU
  model-cache disk contains only the reviewed public model below. No customer
  secrets or data may be placed on any fixture disk.

## Required host baseline

Provision one Ubuntu 24.04 CPU host and one Ubuntu 24.04 NVIDIA GPU host. For
each host:

1. Create a dedicated `forge`-style user with passwordless sudo and a unique
   Ed25519 public key. Do not reuse a personal, overlay, provider-account, or
   other fixture key.
2. Attach one non-root whole disk for Forge AgentPool workspaces. Record its
   stable `/dev/disk/by-id/...` identity. Leave it blank; Forge owns its
   filesystem only after apply.
3. Confirm the selected disk does not back `/`, `/boot`, `/boot/efi`, `/var`,
   swap, `/var/lib/rancher/k3s`, or `/var/lib/kubelet` and has no partitions,
   holders, mounts, or signatures.
4. Install the baseline packages required by Forge and the lifecycle probe:
   `curl`, `git`, `psmisc` (`fuser`), `util-linux`, and the applicable ext4/XFS
   tools. The GPU host must also satisfy Forge's NVIDIA/Ubuntu preflight.
5. Obtain the host public key through a trusted provider console or first-boot
   channel. Compare it independently before recording the exact one-line
   OpenSSH public key. Do not trust an unauthenticated first `ssh-keyscan` result.

The GPU host additionally receives a second non-root whole disk, physically and
logically distinct from the Forge workspace disk:

```bash
# Example only: substitute the founder-verified model-cache by-id device.
DEVICE=/dev/disk/by-id/<gpu-model-cache>
sudo mkfs.ext4 -F -L iterabase-model-cache "$DEVICE"
sudo install -d -o root -g root -m 0755 /data/hf-cache
UUID=$(sudo blkid -p -s UUID -o value "$DEVICE")
printf 'UUID=%s /data/hf-cache ext4 nodev,nosuid 0 2\n' "$UUID" | sudo tee -a /etc/fstab
sudo mount /data/hf-cache
```

Populate only the repository-pinned public model:

```bash
python3 -m pip install --user 'huggingface_hub[cli]'
huggingface-cli download Qwen/Qwen3.5-0.8B \
  --revision 2fc06364715b967f1860aea9cf38778875588b17 \
  --cache-dir /data/hf-cache
sha256sum /data/hf-cache/hub/models--Qwen--Qwen3.5-0.8B/snapshots/\
2fc06364715b967f1860aea9cf38778875588b17/\
model.safetensors-00001-of-00001.safetensors
# must equal f0140d845aced424f17b1c75ebc5a67ef75fe309c68d2f613acda2eb551db7dd
```

Do not mount this disk at
`/var/lib/iterabase/agentpool-workspaces`. Do not place its by-id identity in
`spec.agentPoolWorkspace.device`. Forge purge never targets `/data/hf-cache`.

## GitHub repository configuration

Set these repository **variables** from founder-verified values:

| CPU | GPU |
| --- | --- |
| `FORGE_E2E_CPU_ADDRESS` | `FORGE_E2E_GPU_ADDRESS` |
| `FORGE_E2E_CPU_SSH_USER` | `FORGE_E2E_GPU_SSH_USER` |
| `FORGE_E2E_CPU_SSH_HOST_KEY` | `FORGE_E2E_GPU_SSH_HOST_KEY` |
| `FORGE_E2E_CPU_WORKSPACE_DEVICE` | `FORGE_E2E_GPU_WORKSPACE_DEVICE` |
| — | `FORGE_E2E_GPU_MODEL_CACHE_DEVICE` |
| — | `FORGE_E2E_GPU_MODEL_CACHE_UUID` |

Set two repository **secrets**:

- `FORGE_E2E_CPU_SSH_KEY`
- `FORGE_E2E_GPU_SSH_KEY`

No address, host key, device, or key may come from `workflow_dispatch` input.
`DIGITALOCEAN_TOKEN` or another provider credential must not exist in repository
Actions secrets after cutover.

Audit without exposing values:

```bash
gh variable list --repo nunocgoncalves/iterabase-mono
gh secret list --repo nunocgoncalves/iterabase-mono
```

## Normal lifecycle

Every selected scenario performs this lifecycle before apply and again after
diagnostics, regardless of success, failure, or interruption recovery:

```bash
forge destroy --config forge.yaml --purge-workspace --reboot --yes
```

Expected evidence:

1. existing Flux/platform/GPU/K3s cleanup completes;
2. the exact Forge receipt/device/filesystem/mount/fstab identity is revalidated;
3. the workspace is unmounted and its filesystem signatures are erased;
4. reboot is requested only after successful purge;
5. SSH disconnects, then reconnects under the same pinned host key;
6. `/proc/sys/kernel/random/boot_id` changes;
7. workspace receipt/mount/fstab/signatures, K3s, run-scoped overlays,
   transferred artifacts, and stale test processes are absent;
8. on GPU, `/data/hf-cache` still resolves to its distinct by-id device/UUID and
   its pinned model file still matches revision and SHA-256 authority.

Ordinary `forge destroy` remains data-preserving. `--reboot`, CI mode,
environment, or prior fixture state never implies `--purge-workspace`.

## Key rotation and host-key replacement

Perform rotation only while fixture-backed dispatch is stopped and no job holds
the global concurrency group.

1. Quarantine the target fixture.
2. Add the new fixture-scoped public key through the trusted provider channel.
3. Verify a direct pinned SSH session, then replace only the matching GitHub
   private-key secret.
4. Remove the old authorized key and run one full lifecycle cycle.
5. Record the rotation date and validating run in the operational ticket.

A changed SSH host key is not routine key rotation. Treat it as possible host
replacement or compromise: quarantine, verify through provider console, restore
or reimage the baseline, then update the matching repository variable and
record why the identity changed.

## Failure, quarantine, and manual provider recovery

If SSH remains healthy, leave the failed run red. The next globally serialized
preflight executes the same purge/reboot and may recover interrupted state.
Never rerun a failed assertion to launder it into a pass; qualification streaks
reset on any failed or incomplete cycle.

If SSH, purge, or reboot cannot recover the host:

1. Stop fixture-backed workflow dispatch and let the active job fail.
2. Mark the fixture quarantined in the active Linear incident/ticket.
3. Use the provider console manually to inspect power/network/disk state. Actions
   has no authority here.
4. Prefer reimage over ad-hoc repair when identity or residual-state confidence
   is lost.
5. Restore the full baseline, stable by-id assignments, fixture user/key, pinned
   host key, and (GPU) separately mounted/cache-verified public model.
6. Run a manual complete lifecycle cycle under the global lock before resuming
   required CI. Record source SHA, workflow/job, boot IDs, workspace identity,
   and model revision/hash.

Never attach customer disks, restore customer snapshots, or copy customer data
to a fixture.

## Rollback

Rollback first stops fixture-backed dispatch, lets or forces no new holder of
the global lock, and runs/verifies cleanup where pinned SSH remains healthy.
Quarantine both fixtures while reverting source/workflow behavior.

Do **not** restore a provider account token to Actions, re-enable dynamic
provisioning/reaping, weaken host-key checks, share a key across fixtures, point
Forge purge at the model cache, or make ordinary destroy destructive. A rollback
that needs provider-side action is founder-operated. Resume only with an
explicit approved corrective change and a fresh lifecycle qualification record.
