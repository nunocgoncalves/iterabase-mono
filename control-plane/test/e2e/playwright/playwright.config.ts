import { join } from "node:path";
import { defineConfig, devices } from "@playwright/test";
import { requiredEnv } from "./env";

const artifactRoot = requiredEnv("ITERABASE_BROWSER_ARTIFACT_ROOT");

export default defineConfig({
  testDir: "./tests",
  fullyParallel: false,
  workers: 1,
  retries: 0,
  timeout: 60_000,
  expect: { timeout: 12_000 },
  outputDir: join(artifactRoot, "raw"),
  globalSetup: "./global-setup.ts",
  globalTeardown: "./global-teardown.ts",
  reporter: [
    ["line"],
    ["json", { outputFile: join(artifactRoot, "report.json") }],
  ],
  use: {
    baseURL: requiredEnv("ITERABASE_BROWSER_ENDPOINT"),
    ignoreHTTPSErrors: false,
    trace: "retain-on-failure",
    screenshot: "only-on-failure",
    video: "off",
    locale: "en-GB",
    actionTimeout: 12_000,
    navigationTimeout: 30_000,
  },
  projects: [
    {
      name: "chromium",
      use: { ...devices["Desktop Chrome"] },
    },
  ],
});
