// Plain-HTTP kubelet probes (HOR-381). /healthz reflects supervisor event-loop
// health (200 unless fatal); /readyz is 200 only after Welcome while registered
// and not draining/fatal — busy capacity (a turn in flight) is represented by
// the absence of a Ready credit, NOT by failing Kubernetes readiness. Loopback
// plus bounded supervisor /metrics on the same in-cluster listener; no RPC.

import { createServer, type Server } from "node:http";
import type { Registry } from "prom-client";

export class Probes {
  private healthy = true;
  private ready = false;
  private server: Server | null = null;

  constructor(private readonly metrics?: Registry) {}

  setHealthy(v: boolean): void {
    this.healthy = v;
  }
  setReady(v: boolean): void {
    this.ready = v;
  }

  /** Start the probe server. Resolves when listening (port=0 => OS-assigned). */
  start(port: number): Promise<Server> {
    const server = createServer(async (req, res) => {
      const path = req.url?.split("?", 1)[0];
      if (path === "/healthz") {
        res.writeHead(this.healthy ? 200 : 503).end(this.healthy ? "ok" : "unhealthy");
      } else if (path === "/readyz") {
        res.writeHead(this.ready ? 200 : 503).end(this.ready ? "ok" : "not ready");
      } else if (path === "/metrics" && this.metrics) {
        try {
          res.writeHead(200, { "Content-Type": this.metrics.contentType });
          res.end(await this.metrics.metrics());
        } catch {
          res.writeHead(500).end();
        }
      } else {
        res.writeHead(404).end();
      }
    });
    this.server = server;
    return new Promise((resolve, reject) => {
      server.once("error", reject);
      server.listen(port, () => {
        server.removeListener("error", reject);
        resolve(server);
      });
    });
  }

  async stop(): Promise<void> {
    const s = this.server;
    this.server = null;
    if (!s) return;
    await new Promise<void>((resolve) => s.close(() => resolve()));
  }
}
