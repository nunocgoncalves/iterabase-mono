import { collectDefaultMetrics, Counter, Gauge, Histogram, Registry } from "prom-client";

/** Bounded supervisor-side telemetry. No customer, workflow, run, turn, tool,
 * artifact, credential, URL, payload, or error values are labels. */
export class HarnessMetrics {
  readonly registry = new Registry();
  readonly buildInfo = new Gauge({
    name: "control_plane_harness_build_info",
    help: "Immutable harness build information.",
    labelNames: ["version"] as const,
    registers: [this.registry],
  });
  readonly dispatchConnected = new Gauge({
    name: "control_plane_harness_dispatch_connected",
    help: "Whether the supervisor has a welcomed dispatch stream.",
    registers: [this.registry],
  });
  readonly dispatchReconnects = new Counter({
    name: "control_plane_harness_dispatch_reconnects_total",
    help: "Dispatch reconnect cycles by bounded result.",
    labelNames: ["result"] as const,
    registers: [this.registry],
  });
  readonly activeTurns = new Gauge({
    name: "control_plane_harness_active_turns",
    help: "Turns currently owned by this supervisor.",
    registers: [this.registry],
  });
  readonly turns = new Counter({
    name: "control_plane_harness_turns_total",
    help: "Supervisor turns by bounded terminal result.",
    labelNames: ["result"] as const,
    registers: [this.registry],
  });
  readonly turnDuration = new Histogram({
    name: "control_plane_harness_turn_duration_seconds",
    help: "Supervisor turn duration by bounded terminal result.",
    labelNames: ["result"] as const,
    buckets: [0.1, 0.5, 1, 2.5, 5, 10, 30, 60, 120, 300, 600, 1800, 3600],
    registers: [this.registry],
  });
  readonly childProcesses = new Counter({
    name: "control_plane_harness_child_processes_total",
    help: "Disposable child lifecycles by bounded result.",
    labelNames: ["result"] as const,
    registers: [this.registry],
  });
  readonly childRPC = new Counter({
    name: "control_plane_harness_child_rpc_requests_total",
    help: "Child-to-supervisor RPC requests by bounded operation.",
    labelNames: ["operation"] as const,
    registers: [this.registry],
  });
  readonly storageReady = new Gauge({
    name: "control_plane_harness_storage_ready",
    help: "Whether the shared sandbox mount passed the latest fsync health transaction.",
    registers: [this.registry],
  });
  readonly storageChecks = new Counter({
    name: "control_plane_harness_storage_checks_total",
    help: "Shared sandbox storage health transactions by bounded result.",
    labelNames: ["result"] as const,
    registers: [this.registry],
  });
  readonly pendingReplays = new Gauge({
    name: "control_plane_harness_pending_replays",
    help: "Recovered or stream-loss event tails awaiting cumulative ACK.",
    registers: [this.registry],
  });

  constructor(version = process.env.HARNESS_BUILD_VERSION || "dev") {
    collectDefaultMetrics({ register: this.registry, labels: { component: "harness" } });
    this.buildInfo.labels(version).set(1);
    this.dispatchConnected.set(0);
    this.activeTurns.set(0);
    this.storageReady.set(0);
    this.pendingReplays.set(0);
  }
}
