import { afterEach, beforeEach, describe, expect, it } from "vitest";
import {
  chmodSync,
  mkdirSync,
  mkdtempSync,
  renameSync,
  rmSync,
  symlinkSync,
  unlinkSync,
  writeFileSync,
} from "node:fs";
import { join } from "node:path";
import { tmpdir } from "node:os";
import { TLSKeyError, validateSupervisorTLSKey } from "./tls-key.js";

describe("validateSupervisorTLSKey", () => {
  let root: string;
  let mount: string;
  let key: string;
  let dataLink: string;
  let timestampName: string;
  let target: string;
  const currentUID = typeof process.getuid === "function" ? process.getuid() : 0;
  const currentGID = typeof process.getgid === "function" ? process.getgid() : 0;

  const createTimestamp = (name: string, contents = "private-key-material\n"): string => {
    const directory = join(mount, name);
    mkdirSync(directory, { mode: 0o755 });
    chmodSync(directory, 0o755);
    const projectedKey = join(directory, "tls.key");
    writeFileSync(projectedKey, contents, { mode: 0o440 });
    chmodSync(projectedKey, 0o440);
    return projectedKey;
  };

  const validate = (): void => validateSupervisorTLSKey(key, currentUID, currentGID, root);

  beforeEach(() => {
    root = mkdtempSync(join(tmpdir(), "harness-tls-key-"));
    chmodSync(root, 0o700);
    mount = join(root, "tls");
    mkdirSync(mount, { mode: 0o700 });
    chmodSync(mount, 0o700);
    key = join(mount, "tls.key");
    dataLink = join(mount, "..data");
    timestampName = "..2026_08_31_12_00_00.000000001";
    target = createTimestamp(timestampName);
    symlinkSync(timestampName, dataLink);
    symlinkSync("..data/tls.key", key);
  });

  afterEach(() => rmSync(root, { recursive: true, force: true }));

  it("accepts and reads the exact contained AtomicWriter chain and 0440 target", () => {
    expect(validate).not.toThrow();
  });

  it("revalidates the current target after an atomic rotation without pinning the old inode", () => {
    const nextName = "..2026_08_31_13_00_00.000000002";
    createTimestamp(nextName, "rotated-private-key-material\n");
    const temporaryLink = join(mount, "..data_tmp");
    symlinkSync(nextName, temporaryLink);
    renameSync(temporaryLink, dataLink);
    rmSync(join(mount, timestampName), { recursive: true, force: true });
    expect(validate).not.toThrow();
  });

  it.each([0o400, 0o600, 0o640, 0o441, 0o4600])("refuses resolved target mode %s", (mode) => {
    chmodSync(target, mode);
    expect(validate).toThrow(/mode .* != 0440/);
  });

  it("refuses a direct regular visible key instead of the exact AtomicWriter link", () => {
    unlinkSync(key);
    writeFileSync(key, "private-key-material\n", { mode: 0o440 });
    expect(validate).toThrow(/required symlink/);
  });

  it.each(["tls.key", "../tls.key", "/tmp/tls.key", "..data/other.key"])(
    "refuses visible link target %s",
    (linkTarget) => {
      unlinkSync(key);
      symlinkSync(linkTarget, key);
      expect(validate).toThrow(/private key link/);
    },
  );

  it.each(["../outside", "/outside", "ordinary-directory", "..2026_08_31_12_00_00.UPPER"])(
    "refuses AtomicWriter data-link target %s",
    (linkTarget) => {
      unlinkSync(dataLink);
      symlinkSync(linkTarget, dataLink);
      expect(validate).toThrow(/invalid AtomicWriter data target/);
    },
  );

  it("refuses an extra symlink layer in place of the timestamp directory", () => {
    const extraName = "..2026_08_31_14_00_00.000000003";
    symlinkSync(timestampName, join(mount, extraName));
    unlinkSync(dataLink);
    symlinkSync(extraName, dataLink);
    expect(validate).toThrow(/not a non-symlink directory/);
  });

  it("refuses a symlink resolved target", () => {
    unlinkSync(target);
    symlinkSync("../..data/tls.key", target);
    expect(validate).toThrow(/not a non-symlink regular file/);
  });

  it("refuses a non-regular resolved target", () => {
    unlinkSync(target);
    mkdirSync(target, { mode: 0o440 });
    expect(validate).toThrow(/not a non-symlink regular file/);
  });

  it("refuses a target or chain owner other than the configured root identity", () => {
    expect(() => validateSupervisorTLSKey(key, currentUID + 1, currentGID, root)).toThrow(/owner/);
  });

  it("refuses a child-writable CSI mount", () => {
    chmodSync(mount, 0o720);
    expect(validate).toThrow(/child-writable/);
  });

  it("refuses a child-writable relevant ancestor on a writable mount", () => {
    const mountInfo = join(root, "mountinfo-rw");
    writeFileSync(mountInfo, `42 1 0:42 / ${root} rw,relatime - tmpfs tmpfs rw\n`);
    chmodSync(root, 0o702);
    expect(() => validateSupervisorTLSKey(key, currentUID, currentGID, root, mountInfo)).toThrow(/child-writable/);
  });

  it("accepts write mode bits that are not effectively child-writable on a read-only mount", () => {
    const mountInfo = join(root, "mountinfo-ro");
    writeFileSync(mountInfo, `42 1 0:42 / ${root} ro,relatime - tmpfs tmpfs ro\n`);
    chmodSync(root, 0o702);
    expect(() => validateSupervisorTLSKey(key, currentUID, currentGID, root, mountInfo)).not.toThrow();
  });

  it("refuses an ancestor boundary that does not contain the CSI mount", () => {
    expect(() => validateSupervisorTLSKey(key, currentUID, currentGID, join(root, "other"))).toThrow(
      /does not contain CSI mount/,
    );
  });

  it("refuses an empty resolved key", () => {
    chmodSync(target, 0o640);
    writeFileSync(target, "", { mode: 0o440 });
    chmodSync(target, 0o440);
    expect(validate).toThrow(/empty or unreadable/);
  });

  it("refuses any configured key basename other than tls.key", () => {
    expect(() => validateSupervisorTLSKey(join(mount, "client.key"), currentUID, currentGID, root)).toThrow(/expected tls.key path/);
  });

  it("returns typed fail-closed errors", () => {
    unlinkSync(dataLink);
    expect(validate).toThrow(TLSKeyError);
  });
});
