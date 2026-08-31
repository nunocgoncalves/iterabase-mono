// Fail-closed supervisor identity-key invariant (DES-HOR-538-01).
//
// The cert-manager CSI volume is pod-scoped. The trusted supervisor is the only
// process allowed to read its private key. Startup and periodic readiness checks
// require the path to remain the same non-symlink regular inode, owned by root,
// with exact mode 0600. This validator observes only; it never chmods, chowns,
// replaces, or otherwise repairs projected credential material.

import {
  closeSync,
  constants,
  fstatSync,
  lstatSync,
  openSync,
  readSync,
  type Stats,
} from "node:fs";

export class TLSKeyError extends Error {
  constructor(message: string, options?: ErrorOptions) {
    super(message, options);
    this.name = "TLSKeyError";
  }
}

/**
 * Validate and prove readability of the supervisor's pod-scoped private key.
 * The default owner is root:root (UID:GID 0:0); expected owner values are
 * injectable only so non-root unit tests can exercise the inode/read path.
 */
export function validateSupervisorTLSKey(path: string, expectedOwnerUID = 0, expectedOwnerGID = 0): void {
  let before: Stats;
  try {
    before = lstatSync(path);
  } catch (error) {
    throw new TLSKeyError(`supervisor TLS private key stat failed: ${path}: ${(error as Error).message}`, { cause: error });
  }
  assertSafeKeyStat(before, path, expectedOwnerUID, expectedOwnerGID);

  let fd: number | undefined;
  try {
    fd = openSync(path, constants.O_RDONLY | noFollowFlag());
    const opened = fstatSync(fd);
    assertSafeKeyStat(opened, path, expectedOwnerUID, expectedOwnerGID);
    if (opened.dev !== before.dev || opened.ino !== before.ino) {
      throw new TLSKeyError(`supervisor TLS private key changed while opening: ${path}`);
    }
    const probe = Buffer.allocUnsafe(1);
    if (readSync(fd, probe, 0, 1, 0) !== 1) {
      throw new TLSKeyError(`supervisor TLS private key is empty or unreadable: ${path}`);
    }
  } catch (error) {
    if (error instanceof TLSKeyError) throw error;
    throw new TLSKeyError(`supervisor TLS private key open/read failed: ${path}: ${(error as Error).message}`, { cause: error });
  } finally {
    if (fd !== undefined) closeSync(fd);
  }
}

function assertSafeKeyStat(stat: Stats, path: string, expectedOwnerUID: number, expectedOwnerGID: number): void {
  if (stat.isSymbolicLink()) throw new TLSKeyError(`supervisor TLS private key is a symlink: ${path}`);
  if (!stat.isFile()) throw new TLSKeyError(`supervisor TLS private key is not a regular file: ${path}`);
  if (stat.uid !== expectedOwnerUID || stat.gid !== expectedOwnerGID) {
    throw new TLSKeyError(`supervisor TLS private key owner ${stat.uid}:${stat.gid} != ${expectedOwnerUID}:${expectedOwnerGID}: ${path}`);
  }
  const mode = stat.mode & 0o7777;
  if (mode !== 0o600) {
    throw new TLSKeyError(`supervisor TLS private key mode ${mode.toString(8).padStart(4, "0")} != 0600: ${path}`);
  }
}

function noFollowFlag(): number {
  const value = (constants as typeof constants & { O_NOFOLLOW?: number }).O_NOFOLLOW;
  if (typeof value !== "number") throw new TLSKeyError("O_NOFOLLOW is unavailable; refusing supervisor TLS private key validation");
  return value;
}
