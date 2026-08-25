// The harness worker entry point (HOR-381). Boots infra-only config, starts the
// kubelet probes, runs the supervisor (connect -> Work bidi stream -> turn
// loop, spawning the per-turn pi child via the setpriv launcher), and handles
// SIGTERM/SIGINT (drain: no new credits, abort the active turn, exit).
//
// The child entry (dist/child.js) is the pi AgentSession; this process is the
// trusted supervisor and never imports pi.

import { fileURLToPath } from "node:url";
import { dirname, join } from "node:path";
import { create } from "@bufbuild/protobuf";
import { HelloSchema, WorkerMessageSchema } from "./gen/iterabase/harness/v1/harness_pb.js";
import { loadConfig } from "./config.js";
import { Probes } from "./probes.js";
import { Supervisor } from "./supervisor.js";
import { createChildFactory } from "./child-process.js";
import { HarnessMetrics } from "./metrics.js";
import { checkSandboxStorageHealth } from "./storage-health.js";

/** The compiled pi child entry, sibling to this module's output. */
const CHILD_SCRIPT = join(dirname(fileURLToPath(import.meta.url)), "child.js");

export async function runWorker(): Promise<void> {
  const cfg = loadConfig();
  const metrics = new HarnessMetrics();
  // Ensure the shared RWX sandbox mount root is 0711 root-owned and prove a
  // complete filesystem transaction before opening dispatch credit.
  checkSandboxStorageHealth(cfg.sandboxRoot, cfg.worker.workerId);
  metrics.storageChecks.labels("pass").inc();
  metrics.storageReady.set(1);
  const probes = new Probes(metrics.registry);
  await probes.start(cfg.probe.port);

  const hello = create(WorkerMessageSchema, {
    kind: {
      case: "hello",
      value: create(HelloSchema, {
        workerId: cfg.worker.workerId,
        poolId: cfg.worker.poolId,
        buildVersion: process.env.HARNESS_BUILD_VERSION ?? "",
        protocolVersion: "1",
      }),
    },
  });

  const sup = new Supervisor({
    cfg,
    hello,
    childFactory: createChildFactory(cfg, CHILD_SCRIPT),
    probes,
    metrics,
  });

  let draining = false;
  const drain = async (sig: string): Promise<void> => {
    if (draining) return;
    draining = true;
    console.log(`harness received ${sig}; draining`);
    await sup.drain();
  };
  process.on("SIGTERM", () => void drain("SIGTERM"));
  process.on("SIGINT", () => void drain("SIGINT"));

  let storageFailure: Error | undefined;
  const storageMonitor = setInterval(() => {
    if (storageFailure || draining) return;
    try {
      checkSandboxStorageHealth(cfg.sandboxRoot, cfg.worker.workerId);
      metrics.storageChecks.labels("pass").inc();
      metrics.storageReady.set(1);
    } catch (error) {
      storageFailure = error as Error;
      console.error(`sandbox storage became unavailable: ${storageFailure.message}`);
      metrics.storageChecks.labels("fail").inc();
      metrics.storageReady.set(0);
      probes.setReady(false);
      probes.setHealthy(false);
      clearInterval(storageMonitor);
      // Drain closes dispatch credit and aborts/fences an active turn. The
      // process then exits non-zero below so Kubernetes replaces this client
      // only after the operator observes healthy backend storage.
      void sup.drain().catch((drainError) => {
        console.error(`storage-failure drain failed: ${(drainError as Error).message}`);
      });
    }
  }, 10_000);

  try {
    await sup.run();
    if (storageFailure) throw storageFailure;
  } finally {
    clearInterval(storageMonitor);
    await probes.stop();
  }
}

// Run only when this module is the entry point (not when imported by tests).
if (process.argv[1] && fileURLToPath(import.meta.url) === process.argv[1]) {
  runWorker().catch((err) => {
    console.error(err);
    process.exit(1);
  });
}
