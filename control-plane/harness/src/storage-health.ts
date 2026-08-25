// Runtime RWX health probe (DES-HOR-424-05 / HOR-469).
//
// Kubernetes pod readiness alone does not prove that an established NFS client
// can still perform I/O after share-manager/node failure. The trusted root
// supervisor therefore performs a tiny fsync+rename+unlink transaction under a
// root-only infrastructure directory. Failure removes readiness, drains the
// Work stream, and exits the disposable worker; dispatch owns worker-loss
// fencing and never silently replays a turn/effect.

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

export function checkSandboxStorageHealth(mountRoot: string, workerId: string): void {
  ensureSandboxMountRoot(mountRoot);
  const stats = statfsSync(mountRoot);
  if (stats.bavail <= 0) {
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
      // The expected rename removes the temporary path. A failed operation is
      // already surfaced above; cleanup must not conceal that original error.
    }
    try {
      unlinkSync(committed);
    } catch {
      // Same bounded best-effort cleanup for an interrupted unlink/fsync path.
    }
  }
}
