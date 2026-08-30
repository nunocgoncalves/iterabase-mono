import { chmodSync, existsSync, mkdirSync, readdirSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { checkSandboxStorageHealth, WorkspaceCapacityGate } from "./storage-health.js";

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

describe("WorkspaceCapacityGate", () => {
  it("warns below 25%, gates at 20%, and reopens only at 25%", () => {
    const gate = new WorkspaceCapacityGate();
    expect(gate.observe(26, 100)).toMatchObject({ warning: false, creditGated: false });
    expect(gate.observe(24, 100)).toMatchObject({ warning: true, creditGated: false });
    expect(gate.observe(20, 100)).toMatchObject({ warning: true, creditGated: true });
    expect(gate.observe(24, 100)).toMatchObject({ warning: true, creditGated: true });
    expect(gate.observe(25, 100)).toMatchObject({ warning: false, creditGated: false });
  });

  it("rejects uncertain capacity observations", () => {
    const gate = new WorkspaceCapacityGate();
    expect(() => gate.observe(-1, 100)).toThrow();
    expect(() => gate.observe(101, 100)).toThrow();
    expect(() => gate.observe(1, 0)).toThrow();
  });
});
