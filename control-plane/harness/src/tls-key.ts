// Fail-closed supervisor identity-key invariant (DES-HOR-538-03).
//
// cert-manager CSI uses Kubernetes AtomicWriter symlinks so that a pod sees
// renewed credentials without restart. The trusted supervisor accepts only the
// exact contained AtomicWriter chain and validates the current resolved inode.
// This validator observes only; it never chmods, chowns, mirrors, replaces, or
// otherwise repairs projected credential material.

import {
  closeSync,
  constants,
  fstatSync,
  lstatSync,
  openSync,
  readFileSync,
  readlinkSync,
  readSync,
  type Stats,
} from "node:fs";
import { basename, dirname, isAbsolute, join, relative, resolve, sep } from "node:path";

const VISIBLE_KEY_LINK = "..data/tls.key";
const RESOLVED_KEY_MODE = 0o440;
const ATOMIC_WRITER_TIMESTAMP = /^\.\.\d{4}_\d{2}_\d{2}_\d{2}_\d{2}_\d{2}\.[0-9a-z]+$/;
const MAX_ROTATION_ATTEMPTS = 3;

export class TLSKeyError extends Error {
  constructor(message: string, options?: ErrorOptions) {
    super(message, options);
    this.name = "TLSKeyError";
  }
}

class TLSKeyRotationRaceError extends Error {}

/**
 * Validate and prove readability of the supervisor's pod-scoped private key.
 *
 * Production requires root:root and Linux's live mount table. Owner,
 * ancestor-boundary, and mount-info arguments are injectable only so
 * unprivileged unit tests can exercise the real path logic.
 */
export function validateSupervisorTLSKey(
  path: string,
  expectedOwnerUID = 0,
  expectedOwnerGID = 0,
  ancestorBoundary = "/",
  mountInfoPath = "/proc/self/mountinfo",
): void {
  let lastRotationError: Error | undefined;
  for (let attempt = 0; attempt < MAX_ROTATION_ATTEMPTS; attempt += 1) {
    try {
      validateCurrentAtomicWriterKey(path, expectedOwnerUID, expectedOwnerGID, ancestorBoundary, mountInfoPath);
      return;
    } catch (error) {
      if (!(error instanceof TLSKeyRotationRaceError)) throw error;
      lastRotationError = error;
    }
  }
  throw new TLSKeyError(
    `supervisor TLS private key changed during ${MAX_ROTATION_ATTEMPTS} bounded validation attempts: ${path}`,
    { cause: lastRotationError },
  );
}

function validateCurrentAtomicWriterKey(
  path: string,
  expectedOwnerUID: number,
  expectedOwnerGID: number,
  ancestorBoundary: string,
  mountInfoPath: string,
): void {
  if (!isAbsolute(path) || basename(path) !== "tls.key") {
    throw new TLSKeyError(`supervisor TLS private key must be the absolute expected tls.key path: ${path}`);
  }
  const mountRoot = dirname(path);
  assertProtectedAncestors(mountRoot, ancestorBoundary, expectedOwnerUID, mountInfoPath);

  const visibleLinkStat = safeLstat(path, "visible key link");
  assertOwnedSymlink(visibleLinkStat, path, expectedOwnerUID, expectedOwnerGID);
  const visibleTarget = safeReadlink(path, "visible key link");
  if (visibleTarget !== VISIBLE_KEY_LINK) {
    throw new TLSKeyError(`supervisor TLS private key link ${JSON.stringify(visibleTarget)} != ${JSON.stringify(VISIBLE_KEY_LINK)}: ${path}`);
  }

  const dataLinkPath = join(mountRoot, "..data");
  const dataLinkStat = safeLstat(dataLinkPath, "AtomicWriter data link");
  assertOwnedSymlink(dataLinkStat, dataLinkPath, expectedOwnerUID, expectedOwnerGID);
  const timestampName = safeReadlink(dataLinkPath, "AtomicWriter data link");
  if (isAbsolute(timestampName) || !ATOMIC_WRITER_TIMESTAMP.test(timestampName)) {
    throw new TLSKeyError(`supervisor TLS private key has invalid AtomicWriter data target ${JSON.stringify(timestampName)}: ${dataLinkPath}`);
  }

  const timestampDir = resolve(mountRoot, timestampName);
  assertStrictlyBeneath(mountRoot, timestampDir, "AtomicWriter timestamp directory");
  if (dirname(timestampDir) !== mountRoot) {
    throw new TLSKeyError(`supervisor TLS private key AtomicWriter data target is not one mount-root child: ${timestampDir}`);
  }
  const timestampStat = lstatDuringRotation(timestampDir, "AtomicWriter timestamp directory");
  assertProtectedDirectory(timestampStat, timestampDir, expectedOwnerUID, mountInfoPath);

  const targetPath = join(timestampDir, "tls.key");
  assertStrictlyBeneath(mountRoot, targetPath, "resolved private key");
  const targetBefore = lstatDuringRotation(targetPath, "resolved private key");
  assertResolvedKeyStat(targetBefore, targetPath, expectedOwnerUID, expectedOwnerGID);

  let fd: number | undefined;
  try {
    try {
      fd = openSync(targetPath, constants.O_RDONLY | noFollowFlag());
    } catch (error) {
      if (isNotFound(error)) throw new TLSKeyRotationRaceError(`resolved key rotated before open: ${targetPath}`);
      throw error;
    }
    const opened = fstatSync(fd);
    assertResolvedKeyStat(opened, targetPath, expectedOwnerUID, expectedOwnerGID);
    if (!sameInode(opened, targetBefore)) {
      throw new TLSKeyError(`supervisor TLS private key changed while opening: ${targetPath}`);
    }
    const probe = Buffer.allocUnsafe(1);
    if (readSync(fd, probe, 0, 1, 0) !== 1) {
      throw new TLSKeyError(`supervisor TLS private key is empty or unreadable: ${targetPath}`);
    }

    assertProtectedAncestors(mountRoot, ancestorBoundary, expectedOwnerUID, mountInfoPath);
    const visibleAfter = safeLstat(path, "visible key link");
    const dataAfter = safeLstat(dataLinkPath, "AtomicWriter data link");
    assertOwnedSymlink(visibleAfter, path, expectedOwnerUID, expectedOwnerGID);
    assertOwnedSymlink(dataAfter, dataLinkPath, expectedOwnerUID, expectedOwnerGID);
    const visibleTargetAfter = safeReadlink(path, "visible key link");
    const timestampAfter = safeReadlink(dataLinkPath, "AtomicWriter data link");
    if (
      !sameInode(visibleAfter, visibleLinkStat)
      || !sameInode(dataAfter, dataLinkStat)
      || visibleTargetAfter !== visibleTarget
      || timestampAfter !== timestampName
    ) {
      throw new TLSKeyRotationRaceError(`AtomicWriter links rotated during validation: ${path}`);
    }

    const timestampDirectoryAfter = lstatDuringRotation(timestampDir, "AtomicWriter timestamp directory");
    assertProtectedDirectory(timestampDirectoryAfter, timestampDir, expectedOwnerUID, mountInfoPath);
    if (!sameInode(timestampDirectoryAfter, timestampStat)) {
      throw new TLSKeyError(`supervisor TLS AtomicWriter timestamp directory changed during validation: ${timestampDir}`);
    }

    let targetAfter: Stats;
    try {
      targetAfter = lstatSync(targetPath);
    } catch (error) {
      if (isNotFound(error)) throw new TLSKeyRotationRaceError(`resolved key rotated after open: ${targetPath}`);
      throw error;
    }
    assertResolvedKeyStat(targetAfter, targetPath, expectedOwnerUID, expectedOwnerGID);
    if (!sameInode(targetAfter, opened)) {
      throw new TLSKeyError(`supervisor TLS private key changed after opening: ${targetPath}`);
    }
  } catch (error) {
    if (error instanceof TLSKeyError || error instanceof TLSKeyRotationRaceError) throw error;
    throw new TLSKeyError(`supervisor TLS private key open/read failed: ${targetPath}: ${(error as Error).message}`, { cause: error });
  } finally {
    if (fd !== undefined) closeSync(fd);
  }
}

function assertProtectedAncestors(
  mountRoot: string,
  boundary: string,
  expectedOwnerUID: number,
  mountInfoPath: string,
): void {
  const normalizedBoundary = resolve(boundary);
  const normalizedMount = resolve(mountRoot);
  if (!isAbsolute(boundary) || !isBeneathOrEqual(normalizedBoundary, normalizedMount)) {
    throw new TLSKeyError(`supervisor TLS ancestor boundary ${boundary} does not contain CSI mount ${mountRoot}`);
  }

  let current = normalizedMount;
  for (;;) {
    const stat = safeLstat(current, "CSI mount ancestor");
    assertProtectedDirectory(stat, current, expectedOwnerUID, mountInfoPath);
    if (current === normalizedBoundary) return;
    const parent = dirname(current);
    if (parent === current) {
      throw new TLSKeyError(`supervisor TLS ancestor boundary was not reached from CSI mount: ${boundary}`);
    }
    current = parent;
  }
}

function assertProtectedDirectory(stat: Stats, path: string, expectedOwnerUID: number, mountInfoPath: string): void {
  if (stat.isSymbolicLink() || !stat.isDirectory()) {
    throw new TLSKeyError(`supervisor TLS path is not a non-symlink directory: ${path}`);
  }
  if (stat.uid !== expectedOwnerUID) {
    throw new TLSKeyError(`supervisor TLS directory owner ${stat.uid} != ${expectedOwnerUID}: ${path}`);
  }
  const mode = stat.mode & 0o7777;
  if ((mode & 0o022) !== 0 && !isOnReadOnlyMount(path, mountInfoPath)) {
    throw new TLSKeyError(`supervisor TLS directory is child-writable at mode ${formatMode(mode)}: ${path}`);
  }
}

function isOnReadOnlyMount(path: string, mountInfoPath: string): boolean {
  let source: string;
  try {
    source = readFileSync(mountInfoPath, "utf8");
  } catch (error) {
    throw new TLSKeyError(
      `supervisor TLS cannot prove child-writable directory is on a read-only mount: ${path}: ${(error as Error).message}`,
      { cause: error },
    );
  }

  const candidate = resolve(path);
  let bestMount = "";
  let bestReadOnly = false;
  for (const line of source.split("\n")) {
    if (line === "") continue;
    const separator = line.indexOf(" - ");
    if (separator < 0) continue;
    const fields = line.slice(0, separator).split(" ");
    if (fields.length < 6) continue;
    const mountPoint = decodeMountInfoPath(fields[4]);
    if (!isBeneathOrEqual(mountPoint, candidate) || mountPoint.length < bestMount.length) continue;
    bestMount = mountPoint;
    bestReadOnly = fields[5].split(",").includes("ro");
  }
  if (bestMount === "") {
    throw new TLSKeyError(`supervisor TLS cannot identify mount options for child-writable directory: ${path}`);
  }
  return bestReadOnly;
}

function decodeMountInfoPath(path: string): string {
  return path.replace(/\\(040|011|012|134)/g, (_match, code: string) => {
    switch (code) {
      case "040":
        return " ";
      case "011":
        return "\t";
      case "012":
        return "\n";
      default:
        return "\\";
    }
  });
}

function assertOwnedSymlink(stat: Stats, path: string, expectedOwnerUID: number, expectedOwnerGID: number): void {
  if (!stat.isSymbolicLink()) {
    throw new TLSKeyError(`supervisor TLS AtomicWriter path is not the required symlink: ${path}`);
  }
  if (stat.uid !== expectedOwnerUID || stat.gid !== expectedOwnerGID) {
    throw new TLSKeyError(`supervisor TLS symlink owner ${stat.uid}:${stat.gid} != ${expectedOwnerUID}:${expectedOwnerGID}: ${path}`);
  }
}

function assertResolvedKeyStat(stat: Stats, path: string, expectedOwnerUID: number, expectedOwnerGID: number): void {
  if (stat.isSymbolicLink() || !stat.isFile()) {
    throw new TLSKeyError(`supervisor TLS resolved private key is not a non-symlink regular file: ${path}`);
  }
  if (stat.uid !== expectedOwnerUID || stat.gid !== expectedOwnerGID) {
    throw new TLSKeyError(`supervisor TLS resolved private key owner ${stat.uid}:${stat.gid} != ${expectedOwnerUID}:${expectedOwnerGID}: ${path}`);
  }
  const mode = stat.mode & 0o7777;
  if (mode !== RESOLVED_KEY_MODE) {
    throw new TLSKeyError(`supervisor TLS resolved private key mode ${formatMode(mode)} != 0440: ${path}`);
  }
}

function assertStrictlyBeneath(root: string, candidate: string, label: string): void {
  const rel = relative(root, candidate);
  if (rel === "" || rel === ".." || rel.startsWith(`..${sep}`) || isAbsolute(rel)) {
    throw new TLSKeyError(`supervisor TLS ${label} escapes expected CSI mount ${root}: ${candidate}`);
  }
}

function isBeneathOrEqual(root: string, candidate: string): boolean {
  const rel = relative(root, candidate);
  return rel === "" || (rel !== ".." && !rel.startsWith(`..${sep}`) && !isAbsolute(rel));
}

function safeLstat(path: string, label: string): Stats {
  try {
    return lstatSync(path);
  } catch (error) {
    throw new TLSKeyError(`supervisor TLS ${label} stat failed: ${path}: ${(error as Error).message}`, { cause: error });
  }
}

function lstatDuringRotation(path: string, label: string): Stats {
  try {
    return lstatSync(path);
  } catch (error) {
    if (isNotFound(error)) throw new TLSKeyRotationRaceError(`${label} rotated before validation: ${path}`);
    throw new TLSKeyError(`supervisor TLS ${label} stat failed: ${path}: ${(error as Error).message}`, { cause: error });
  }
}

function safeReadlink(path: string, label: string): string {
  try {
    return readlinkSync(path);
  } catch (error) {
    throw new TLSKeyError(`supervisor TLS ${label} readlink failed: ${path}: ${(error as Error).message}`, { cause: error });
  }
}

function sameInode(left: Stats, right: Stats): boolean {
  return left.dev === right.dev && left.ino === right.ino;
}

function isNotFound(error: unknown): boolean {
  return (error as NodeJS.ErrnoException).code === "ENOENT";
}

function formatMode(mode: number): string {
  return mode.toString(8).padStart(4, "0");
}

function noFollowFlag(): number {
  const value = (constants as typeof constants & { O_NOFOLLOW?: number }).O_NOFOLLOW;
  if (typeof value !== "number") throw new TLSKeyError("O_NOFOLLOW is unavailable; refusing supervisor TLS private key validation");
  return value;
}
