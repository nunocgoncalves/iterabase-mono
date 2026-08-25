import { chmodSync, existsSync, mkdirSync, readdirSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { checkSandboxStorageHealth } from "./storage-health.js";

let base: string;

beforeEach(() => {
  base = join(tmpdir(), `harness-storage-health-${process.pid}-${Date.now()}`);
  mkdirSync(base, { recursive: true, mode: 0o711 });
  chmodSync(base, 0o711);
});

afterEach(() => rmSync(base, { recursive: true, force: true }));

describe("checkSandboxStorageHealth", () => {
  it("fsyncs, renames, and unlinks a worker-scoped transaction", () => {
    checkSandboxStorageHealth(base, "pool-worker-0");
    const healthRoot = join(base, ".iterabase-storage-health");
    expect(existsSync(healthRoot)).toBe(true);
    expect(readdirSync(healthRoot)).toEqual([]);
  });

  it("fails closed when the infrastructure health root is not a directory", () => {
    writeFileSync(join(base, ".iterabase-storage-health"), "unsafe");
    expect(() => checkSandboxStorageHealth(base, "pool-worker-0")).toThrow();
  });

  it("sanitizes a worker identity before using it as a temporary filename", () => {
    checkSandboxStorageHealth(base, "pool/worker:0");
    expect(readdirSync(join(base, ".iterabase-storage-health"))).toEqual([]);
  });
});
