import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { chmodSync, mkdirSync, mkdtempSync, rmSync, symlinkSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { tmpdir } from "node:os";
import { TLSKeyError, validateSupervisorTLSKey } from "./tls-key.js";

describe("validateSupervisorTLSKey", () => {
  let root: string;
  let key: string;
  const currentUID = typeof process.getuid === "function" ? process.getuid() : 0;
  const currentGID = typeof process.getgid === "function" ? process.getgid() : 0;

  beforeEach(() => {
    root = mkdtempSync(join(tmpdir(), "harness-tls-key-"));
    key = join(root, "tls.key");
    writeFileSync(key, "private-key-material\n", { mode: 0o600 });
    chmodSync(key, 0o600);
  });

  afterEach(() => rmSync(root, { recursive: true, force: true }));

  it("accepts and reads one exact-owner regular 0600 key without mutating it", () => {
    expect(() => validateSupervisorTLSKey(key, currentUID, currentGID)).not.toThrow();
  });

  it.each([0o400, 0o640, 0o600 | 0o001, 0o660, 0o4600])("refuses mode %s", (mode) => {
    chmodSync(key, mode);
    expect(() => validateSupervisorTLSKey(key, currentUID, currentGID)).toThrow(TLSKeyError);
  });

  it("refuses a symlink even when its target is an exact 0600 key", () => {
    const link = join(root, "linked.key");
    symlinkSync(key, link);
    expect(() => validateSupervisorTLSKey(link, currentUID, currentGID)).toThrow(/symlink/);
  });

  it("refuses a non-regular path", () => {
    const directory = join(root, "directory.key");
    mkdirSync(directory, { mode: 0o700 });
    expect(() => validateSupervisorTLSKey(directory, currentUID, currentGID)).toThrow(/not a regular file/);
  });

  it("refuses an owner other than the configured root UID:GID", () => {
    expect(() => validateSupervisorTLSKey(key, currentUID + 1, currentGID)).toThrow(/owner/);
  });

  it("refuses an empty key", () => {
    writeFileSync(key, "", { mode: 0o600 });
    expect(() => validateSupervisorTLSKey(key, currentUID, currentGID)).toThrow(/empty or unreadable/);
  });
});
