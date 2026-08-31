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
import { HelloSchema, WorkerMessageSchema, WorkspaceStatusSchema } from "./gen/iterabase/harness/v1/harness_pb.js";
import { loadConfig } from "./config.js";
import { Probes } from "./probes.js";
import { Supervisor } from "./supervisor.js";
import { createChildFactory } from "./child-process.js";
import { HarnessMetrics } from "./metrics.js";
import { checkSandboxStorageHealth, WorkspaceCapacityGate, type WorkspaceCapacity } from "./storage-health.js";
import { TLSKeyError, validateSupervisorTLSKey } from "./tls-key.js";

/** The compiled pi child entry, sibling to this module's output. */
const CHILD_SCRIPT = join(dirname(fileURLToPath(import.meta.url)), "child.js");

export async function runWorker(): Promise<void> {
  const cfg = loadConfig();
  const metrics = new HarnessMetrics();
  // Observe only: never chmod/chown/mirror/replace projected credentials. Only
  // DES-HOR-538-03's contained cert-manager AtomicWriter chain and exact
  // root:root 0440 resolved target pass before network startup; periodic checks
  // safely re-resolve rotation and withdraw readiness on any drift.
  validateSupervisorTLSKey(cfg.tls.key);
  // The gate file lives on the AgentPool's shared PVC so replacement workers
  // retain 20/25 hysteresis instead of reopening credit inside the band.
  const capacityGate = new WorkspaceCapacityGate(cfg.sandboxRoot);
  const observeWorkspace = (): WorkspaceCapacity => {
    const observed = checkSandboxStorageHealth(cfg.sandboxRoot, cfg.worker.workerId);
    const capacity = capacityGate.observe(observed.freeBytes, observed.capacityBytes);
    metrics.storageChecks.labels("pass").inc();
    metrics.storageReady.set(1);
    metrics.workspaceFreeBytes.set(capacity.freeBytes);
    metrics.workspaceCapacityBytes.set(capacity.capacityBytes);
    metrics.workspaceFreeRatio.set(capacity.freeRatio);
    metrics.workspaceCapacityWarning.set(capacity.warning ? 1 : 0);
    metrics.workspaceCreditGated.set(capacity.creditGated ? 1 : 0);
    return capacity;
  };
  const initialCapacity = observeWorkspace();
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

  const toWorkspaceStatus = (capacity: WorkspaceCapacity) => create(WorkspaceStatusSchema, {
    freeBytes: BigInt(capacity.freeBytes),
    capacityBytes: BigInt(capacity.capacityBytes),
    freeRatio: capacity.freeRatio,
    warning: capacity.warning,
    creditGated: capacity.creditGated,
  });
  const sup = new Supervisor({
    cfg,
    hello,
    childFactory: createChildFactory(cfg, CHILD_SCRIPT),
    probes,
    metrics,
    workspaceStatus: toWorkspaceStatus(initialCapacity),
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

  let readinessFailure: Error | undefined;
  const readinessMonitor = setInterval(() => {
    if (readinessFailure || draining) return;
    try {
      validateSupervisorTLSKey(cfg.tls.key);
      const capacity = observeWorkspace();
      sup.updateWorkspaceStatus(toWorkspaceStatus(capacity));
    } catch (error) {
      readinessFailure = error as Error;
      console.error(`supervisor readiness invariant failed: ${readinessFailure.message}`);
      if (!(readinessFailure instanceof TLSKeyError)) metrics.storageChecks.labels("fail").inc();
      metrics.storageReady.set(0);
      probes.setReady(false);
      probes.setHealthy(false);
      clearInterval(readinessMonitor);
      // Drain closes dispatch credit and aborts/fences an active turn. The
      // process then exits non-zero below so Kubernetes replaces this client
      // only after the operator observes healthy storage and identity material.
      void sup.drain().catch((drainError) => {
        console.error(`readiness-failure drain failed: ${(drainError as Error).message}`);
      });
    }
  }, 10_000);

  try {
    await sup.run();
    if (readinessFailure) throw readinessFailure;
  } finally {
    clearInterval(readinessMonitor);
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
