import { mkdtempSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { describe, expect, it } from "vitest";
import { EventOutbox } from "./event-outbox.js";
import { HarnessMetrics } from "./metrics.js";
import { Probes } from "./probes.js";
import { Supervisor } from "./supervisor.js";
import type { HarnessConfig } from "./config.js";

describe("HarnessMetrics", () => {
  it("exports bounded supervisor metrics without runtime identifiers", async () => {
    const metrics = new HarnessMetrics("1.2.3");
    metrics.dispatchConnected.set(1);
    metrics.turns.labels("completed").inc();
    metrics.childRPC.labels("modelRequest").inc();
    const rendered = await metrics.registry.metrics();
    expect(rendered).toContain("control_plane_harness_dispatch_connected 1");
    expect(rendered).toContain('control_plane_harness_turns_total{result="completed"} 1');
    expect(rendered).toContain('control_plane_harness_child_rpc_requests_total{operation="modelRequest"} 1');
    expect(rendered).not.toMatch(/turn[_-]?id|run[_-]?id|workflow|credential/i);
  });

  it("serves metrics on the existing probe listener", async () => {
    const metrics = new HarnessMetrics("test");
    const probes = new Probes(metrics.registry);
    const server = await probes.start(0);
    const address = server.address();
    if (!address || typeof address === "string") throw new Error("expected TCP address");
    const response = await fetch(`http://127.0.0.1:${address.port}/metrics`);
    expect(response.status).toBe(200);
    expect(await response.text()).toContain("control_plane_harness_build_info");
    await probes.stop();
  });

  it("reports a replay backlog immediately when stream loss retains the active WAL", async () => {
    const walDir = mkdtempSync(join(tmpdir(), "harness-metrics-wal-"));
    try {
      const metrics = new HarnessMetrics("test");
      const supervisor = new Supervisor({
        cfg: { walDir, tokenDelta: { sendBufferBytes: 1024 } } as HarnessConfig,
        hello: {} as never,
        childFactory: (() => {
          throw new Error("child factory is not used");
        }) as never,
        probes: new Probes(metrics.registry),
        gatewayClient: {} as never,
        modelStream: (() => {
          throw new Error("model stream is not used");
        }) as never,
        metrics,
      });
      const internals = supervisor as unknown as {
        outbox: EventOutbox | null;
        turn: {
          turnId: string;
          aborted: boolean;
          completionReported: boolean;
          acked: Promise<void>;
          resolveAck: () => void;
          discoveryAc: AbortController | null;
        } | null;
        failClosed: (err: unknown) => Promise<void>;
      };
      internals.outbox = new EventOutbox(walDir, "turn-stream-loss", 100);
      internals.turn = {
        turnId: "turn-stream-loss",
        aborted: false,
        completionReported: false,
        acked: Promise.resolve(),
        resolveAck: () => {},
        discoveryAc: null,
      };

      await internals.failClosed("stream loss");

      expect(await metrics.registry.metrics()).toContain("control_plane_harness_pending_replays 1");
    } finally {
      rmSync(walDir, { recursive: true, force: true });
    }
  });
});
