// Runtime node-local workspace health and capacity gate (DES-HOR-538-01/02).
//
// The trusted supervisor proves real filesystem I/O and measures the mounted
// filesystem's available blocks. Capacity warning/gating is hysteretic and
// suppresses only fresh dispatch credit: crossing the floor never aborts an
// already active turn. Zero blocks or an actual I/O/fsync failure remains a
// fail-closed worker loss handled by existing turn/effect fencing.

import { randomUUID } from "node:crypto";
import {
  chmodSync,
  closeSync,
  existsSync,
  fsyncSync,
  lstatSync,
  mkdirSync,
  openSync,
  readFileSync,
  renameSync,
  statfsSync,
  unlinkSync,
  writeFileSync,
} from "node:fs";
import { join } from "node:path";
import { ensureSandboxMountRoot, SandboxError } from "./sandbox.js";

const HEALTH_DIRECTORY = ".iterabase-storage-health";
const CAPACITY_GATE_STATE = "capacity-gate";
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
  private loaded = false;

  constructor(private readonly mountRoot?: string) {}

  observe(freeBytes: number, capacityBytes: number): WorkspaceCapacity {
    if (!Number.isFinite(freeBytes) || !Number.isFinite(capacityBytes) || freeBytes < 0 || capacityBytes <= 0 || freeBytes > capacityBytes) {
      throw new SandboxError(`invalid workspace filesystem capacity observation: free=${freeBytes} capacity=${capacityBytes}`);
    }
    const freeRatio = freeBytes / capacityBytes;
    if (!this.loaded) this.load(freeRatio);
    const wasGated = this.gated;
    if (freeRatio <= WORKSPACE_GATE_RATIO) this.gated = true;
    else if (this.gated && freeRatio >= WORKSPACE_REOPEN_RATIO) this.gated = false;
    if (this.mountRoot && (wasGated !== this.gated || !existsSync(this.statePath()))) this.persist();
    return {
      freeBytes,
      capacityBytes,
      freeRatio,
      warning: freeRatio < WORKSPACE_WARNING_RATIO,
      creditGated: this.gated,
    };
  }

  private statePath(): string {
    if (!this.mountRoot) throw new SandboxError("workspace capacity gate has no persistent mount root");
    return join(this.mountRoot, HEALTH_DIRECTORY, CAPACITY_GATE_STATE);
  }

  private load(freeRatio: number): void {
    this.loaded = true;
    if (!this.mountRoot) return;
    const healthRoot = ensureWorkspaceHealthRoot(this.mountRoot);
    const path = join(healthRoot, CAPACITY_GATE_STATE);
    if (!existsSync(path)) {
      // No durable history is ambiguous in the hysteresis band. Start closed
      // there and require an observation at/above 25% before fresh credit.
      this.gated = freeRatio < WORKSPACE_REOPEN_RATIO;
      this.persist();
      return;
    }
    const stat = lstatSync(path);
    if (!stat.isFile() || stat.isSymbolicLink() || stat.uid !== currentUID() || stat.gid !== currentGID() || (stat.mode & 0o777) !== 0o600) {
      throw new SandboxError(`workspace capacity gate state is unsafe: ${path}`);
    }
    const value = readFileSync(path, "utf8");
    if (value !== "open\n" && value !== "gated\n") {
      throw new SandboxError(`workspace capacity gate state is invalid: ${path}`);
    }
    this.gated = value === "gated\n";
  }

  private persist(): void {
    if (!this.mountRoot) return;
    const healthRoot = ensureWorkspaceHealthRoot(this.mountRoot);
    const path = join(healthRoot, CAPACITY_GATE_STATE);
    const temporary = join(healthRoot, `.${CAPACITY_GATE_STATE}-${randomUUID()}.tmp`);
    let fileFD: number | undefined;
    let directoryFD: number | undefined;
    try {
      writeFileSync(temporary, this.gated ? "gated\n" : "open\n", { mode: 0o600, flag: "wx" });
      chmodSync(temporary, 0o600);
      fileFD = openSync(temporary, "r");
      fsyncSync(fileFD);
      closeSync(fileFD);
      fileFD = undefined;
      renameSync(temporary, path);
      directoryFD = openSync(healthRoot, "r");
      fsyncSync(directoryFD);
    } catch (error) {
      throw new SandboxError(`persist workspace capacity gate state at ${path}: ${(error as Error).message}`);
    } finally {
      if (fileFD !== undefined) closeSync(fileFD);
      if (directoryFD !== undefined) closeSync(directoryFD);
      try {
        unlinkSync(temporary);
      } catch {
        // Atomic rename removes the temporary path on success.
      }
    }
  }
}

function currentUID(): number {
  if (typeof process.getuid !== "function") throw new SandboxError("supervisor uid unavailable during storage health check");
  return process.getuid();
}

function currentGID(): number {
  if (typeof process.getgid !== "function") throw new SandboxError("supervisor gid unavailable during storage health check");
  return process.getgid();
}

function ensureWorkspaceHealthRoot(mountRoot: string): string {
  ensureSandboxMountRoot(mountRoot);
  const uid = currentUID();
  const gid = currentGID();
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
  return healthRoot;
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

  const healthRoot = ensureWorkspaceHealthRoot(mountRoot);

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
