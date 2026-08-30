// Runtime node-local workspace health and capacity gate (DES-HOR-538-01/02).
//
// The trusted supervisor proves real filesystem I/O and measures the mounted
// filesystem's available blocks. Capacity warning/gating is hysteretic and
// suppresses only fresh dispatch credit: crossing the floor never aborts an
// already active turn. Zero blocks or an actual I/O/fsync failure remains a
// fail-closed worker loss handled by existing turn/effect fencing.

import {
  chmodSync,
  closeSync,
  fsyncSync,
  lstatSync,
  mkdirSync,
  openSync,
  renameSync,
  statfsSync,
  unlinkSync,
  writeFileSync,
} from "node:fs";
import { join } from "node:path";
import { ensureSandboxMountRoot, SandboxError } from "./sandbox.js";

const HEALTH_DIRECTORY = ".iterabase-storage-health";
export const WORKSPACE_WARNING_RATIO = 0.25;
export const WORKSPACE_GATE_RATIO = 0.20;
export const WORKSPACE_REOPEN_RATIO = 0.25;

export interface WorkspaceCapacity {
  freeBytes: number;
  capacityBytes: number;
  freeRatio: number;
  warning: boolean;
  creditGated: boolean;
}

export class WorkspaceCapacityGate {
  private gated = false;

  observe(freeBytes: number, capacityBytes: number): WorkspaceCapacity {
    if (!Number.isFinite(freeBytes) || !Number.isFinite(capacityBytes) || freeBytes < 0 || capacityBytes <= 0 || freeBytes > capacityBytes) {
      throw new SandboxError(`invalid workspace filesystem capacity observation: free=${freeBytes} capacity=${capacityBytes}`);
    }
    const freeRatio = freeBytes / capacityBytes;
    if (freeRatio <= WORKSPACE_GATE_RATIO) this.gated = true;
    else if (this.gated && freeRatio >= WORKSPACE_REOPEN_RATIO) this.gated = false;
    return {
      freeBytes,
      capacityBytes,
      freeRatio,
      warning: freeRatio < WORKSPACE_WARNING_RATIO,
      creditGated: this.gated,
    };
  }
}

export function checkSandboxStorageHealth(mountRoot: string, workerId: string): { freeBytes: number; capacityBytes: number } {
  ensureSandboxMountRoot(mountRoot);
  const stats = statfsSync(mountRoot);
  const freeBytes = Number(stats.bavail) * Number(stats.bsize);
  const capacityBytes = Number(stats.blocks) * Number(stats.bsize);
  if (!Number.isSafeInteger(freeBytes) || !Number.isSafeInteger(capacityBytes) || capacityBytes <= 0) {
    throw new SandboxError(`sandbox storage capacity cannot be represented safely: ${mountRoot}`);
  }
  if (freeBytes <= 0) {
    throw new SandboxError(`sandbox storage has no available filesystem blocks: ${mountRoot}`);
  }

  if (typeof process.getuid !== "function" || typeof process.getgid !== "function") {
    throw new SandboxError(`supervisor uid/gid unavailable during storage health check: ${mountRoot}`);
  }
  const uid = process.getuid();
  const gid = process.getgid();
  const healthRoot = join(mountRoot, HEALTH_DIRECTORY);
  mkdirSync(healthRoot, { recursive: true, mode: 0o700 });
  chmodSync(healthRoot, 0o700);
  const healthStat = lstatSync(healthRoot);
  if (!healthStat.isDirectory() || healthStat.isSymbolicLink()) {
    throw new SandboxError(`sandbox storage health root is unsafe: ${healthRoot}`);
  }
  if (healthStat.uid !== uid || healthStat.gid !== gid || (healthStat.mode & 0o777) !== 0o700) {
    throw new SandboxError(
      `sandbox storage health root must be ${uid}:${gid}/0700 (got ${healthStat.uid}:${healthStat.gid}/${(healthStat.mode & 0o777).toString(8)}): ${healthRoot}`,
    );
  }

  const safeWorker = workerId.replace(/[^a-zA-Z0-9_.-]/g, "_");
  const nonce = `${process.pid}-${Date.now()}`;
  const temporary = join(healthRoot, `.${safeWorker}-${nonce}.tmp`);
  const committed = join(healthRoot, `.${safeWorker}-${nonce}.health`);
  let directoryFD: number | undefined;
  try {
    writeFileSync(temporary, `${workerId}\n${Date.now()}\n`, { mode: 0o600, flag: "wx" });
    const fileFD = openSync(temporary, "r");
    try {
      fsyncSync(fileFD);
    } finally {
      closeSync(fileFD);
    }
    renameSync(temporary, committed);
    directoryFD = openSync(healthRoot, "r");
    fsyncSync(directoryFD);
    unlinkSync(committed);
    fsyncSync(directoryFD);
  } catch (error) {
    throw new SandboxError(`sandbox storage health transaction failed at ${mountRoot}: ${(error as Error).message}`);
  } finally {
    if (directoryFD !== undefined) closeSync(directoryFD);
    try {
      unlinkSync(temporary);
    } catch {
      // The expected rename removes the temporary path. Preserve the original
      // transaction error when cleanup has nothing left to remove.
    }
    try {
      unlinkSync(committed);
    } catch {
      // Same bounded cleanup for an interrupted unlink/fsync path.
    }
  }
  return { freeBytes, capacityBytes };
}
