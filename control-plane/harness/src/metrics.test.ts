import { describe, expect, it } from "vitest";
import { HarnessMetrics } from "./metrics.js";
import { Probes } from "./probes.js";


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
});
