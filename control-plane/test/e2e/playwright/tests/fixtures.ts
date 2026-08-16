import { appendFileSync } from "node:fs";
import { join } from "node:path";
import {
  expect,
  test as base,
  type ConsoleMessage,
  type Request,
  type Response,
} from "@playwright/test";
import { requiredEnv } from "../env";

type Allowance = {
  matches: (path: string, value: number | string) => boolean;
  rationale: string;
};

export type BrowserObservability = {
  allowHTTPStatus(status: number, path: RegExp, rationale: string): void;
  allowRequestFailure(path: RegExp, rationale: string): void;
  allowConsoleError(message: RegExp, rationale: string): void;
};

type Fixtures = { observability: BrowserObservability };

function requestPath(rawURL: string): string {
  const parsed = new URL(rawURL);
  return parsed.pathname;
}

function writeEvidence(record: Record<string, unknown>): void {
  appendFileSync(
    join(requiredEnv("ITERABASE_BROWSER_ARTIFACT_ROOT"), "network.jsonl"),
    `${JSON.stringify({ at: new Date().toISOString(), ...record })}\n`,
    { mode: 0o600 },
  );
}

export const test = base.extend<Fixtures>({
  observability: async ({ page }, use) => {
    const statusAllowances: Allowance[] = [];
    const failureAllowances: Allowance[] = [];
    const consoleAllowances: Allowance[] = [];
    const unexpected: string[] = [];

    const observability: BrowserObservability = {
      allowHTTPStatus(status, path, rationale) {
        statusAllowances.push({
          matches: (candidatePath, value) =>
            value === status && path.test(candidatePath),
          rationale,
        });
      },
      allowRequestFailure(path, rationale) {
        failureAllowances.push({
          matches: (candidatePath) => path.test(candidatePath),
          rationale,
        });
      },
      allowConsoleError(message, rationale) {
        consoleAllowances.push({
          matches: (_path, value) =>
            typeof value === "string" && message.test(value),
          rationale,
        });
      },
    };

    const onConsole = (message: ConsoleMessage) => {
      const allowance = consoleAllowances.find((entry) =>
        entry.matches("", message.text()),
      );
      writeEvidence({
        kind: "console",
        level: message.type(),
        text: message.text(),
        allowlisted: allowance?.rationale,
      });
      if (message.type() === "error" && !allowance) {
        unexpected.push(`console error: ${message.text()}`);
      }
    };
    const onPageError = (error: Error) => {
      writeEvidence({ kind: "page-error", message: error.message });
      unexpected.push(`page error: ${error.message}`);
    };
    const onResponse = (response: Response) => {
      const path = requestPath(response.url());
      const status = response.status();
      const allowance = statusAllowances.find((entry) =>
        entry.matches(path, status),
      );
      writeEvidence({
        kind: "response",
        method: response.request().method(),
        path,
        resourceType: response.request().resourceType(),
        status,
        allowlisted: allowance?.rationale,
      });
      if (status >= 400 && !allowance) {
        unexpected.push(`HTTP ${status}: ${path}`);
      }
    };
    const onRequestFailed = (request: Request) => {
      const path = requestPath(request.url());
      const failure = request.failure()?.errorText || "unknown failure";
      const allowance = failureAllowances.find((entry) =>
        entry.matches(path, failure),
      );
      writeEvidence({
        kind: "request-failed",
        method: request.method(),
        path,
        failure,
        allowlisted: allowance?.rationale,
      });
      if (!allowance) unexpected.push(`request failed: ${path}: ${failure}`);
    };

    page.on("console", onConsole);
    page.on("pageerror", onPageError);
    page.on("response", onResponse);
    page.on("requestfailed", onRequestFailed);
    await use(observability);
    page.off("console", onConsole);
    page.off("pageerror", onPageError);
    page.off("response", onResponse);
    page.off("requestfailed", onRequestFailed);
    expect(
      unexpected,
      "unexpected browser console/page/network failures",
    ).toEqual([]);
  },
});

export { expect };
