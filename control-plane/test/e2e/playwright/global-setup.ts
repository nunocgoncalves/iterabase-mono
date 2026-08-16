import { mkdirSync, rmSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { requiredEnv } from "./env";

export default function globalSetup(): void {
  const root = requiredEnv("ITERABASE_BROWSER_ARTIFACT_ROOT");
  rmSync(join(root, "raw"), { recursive: true, force: true });
  rmSync(join(root, "safe-opaque"), { recursive: true, force: true });
  rmSync(join(root, "sanitized.marker"), { force: true });
  mkdirSync(join(root, "raw"), { recursive: true, mode: 0o700 });
  writeFileSync(join(root, "network.jsonl"), "", { mode: 0o600 });
}
