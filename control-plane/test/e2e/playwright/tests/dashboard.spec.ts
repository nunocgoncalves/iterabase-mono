import { readFileSync } from "node:fs";
import AxeBuilder from "@axe-core/playwright";
import { type Page } from "@playwright/test";
import { requiredEnv } from "../env";
import { expect, test, type BrowserObservability } from "./fixtures";

const workKey = requiredEnv("ITERABASE_BROWSER_WORK_KEY");
const coordinator = requiredEnv("ITERABASE_BROWSER_COORDINATOR");
const doneTitle = requiredEnv("ITERABASE_BROWSER_DONE_TITLE");
const approvalTitle = requiredEnv("ITERABASE_BROWSER_APPROVAL_TITLE");
const uploadTitle = requiredEnv("ITERABASE_BROWSER_UPLOAD_TITLE");
const reconnectTitle = requiredEnv("ITERABASE_BROWSER_RECONNECT_TITLE");
const downloadName = requiredEnv("ITERABASE_BROWSER_DOWNLOAD_NAME");
const downloadBody = requiredEnv("ITERABASE_BROWSER_DOWNLOAD_BODY");

async function assertFreshBrowserState(page: Page): Promise<void> {
  await expect
    .poll(() =>
      page.evaluate(() => ({
        cookie: document.cookie,
        local: Object.keys(localStorage),
        session: Object.keys(sessionStorage),
      })),
    )
    .toEqual({ cookie: "", local: [], session: [] });
}

async function connect(page: Page): Promise<void> {
  await page.goto("/");
  await assertFreshBrowserState(page);
  const key = page.getByLabel("Work API key");
  await key.focus();
  await expect(key).toBeFocused();
  await key.fill(workKey);
  await page.getByRole("button", { name: "Connect" }).click();
  await expect(page.getByRole("region", { name: "Work" })).toBeVisible();
  await expect(page.getByText(doneTitle, { exact: true })).toBeVisible();
  await assertFreshBrowserState(page);
}

function allowNetworkOutage(observability: BrowserObservability): void {
  observability.allowHTTPStatus(
    502,
    /^\/v1\//,
    "HOR-483 intentionally interrupts the Go-owned verified API forward to exercise customer error and SSE reconnect states.",
  );
  observability.allowRequestFailure(
    /^\/v1\//,
    /^net::(ERR_ABORTED|ERR_INCOMPLETE_CHUNKED_ENCODING)$/,
    "HOR-483 intentionally interrupts active API/SSE requests before Go restores the same deployed fixture.",
  );
  observability.allowConsoleError(
    /Failed to load resource: (net::ERR_INCOMPLETE_CHUNKED_ENCODING|the server responded with a status of 502)/,
    "Chromium reports the same explicitly allowlisted HOR-483 API/SSE outage in its console channel.",
  );
}

test("uses in-memory authentication, real loading/error responses, and both locales", async ({
  page,
  observability,
}) => {
  observability.allowHTTPStatus(
    401,
    /^\/v1\/work-(dashboard|items)$/,
    "The invalid-key customer error state deliberately exercises the deployed authentication rejection.",
  );
  observability.allowConsoleError(
    /Failed to load resource: the server responded with a status of 401/,
    "Chromium reports the same explicitly allowlisted deployed invalid-key rejection in its console channel.",
  );
  await page.goto("/");
  await assertFreshBrowserState(page);

  await page.getByLabel("Work API key").fill("invalid-browser-key");
  await page.getByRole("button", { name: "Connect" }).click();
  await expect(page.getByRole("alert")).toHaveText(
    "The key could not open the Dashboard.",
  );

  let delayed = false;
  await page.route("**/v1/work-dashboard?*", async (route) => {
    if (!delayed) {
      delayed = true;
      await new Promise((resolve) => setTimeout(resolve, 500));
    }
    await route.continue();
  });
  await page.getByLabel("Work API key").fill(workKey);
  await page.getByRole("button", { name: "Connect" }).click();
  await expect(page.getByRole("button", { name: "Connecting…" })).toBeVisible();
  await expect(page.getByText(doneTitle, { exact: true })).toBeVisible();
  await assertFreshBrowserState(page);

  await page.getByRole("button", { name: "PT", exact: true }).click();
  await expect(page.getByRole("region", { name: "Trabalho" })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Concluído" })).toBeVisible();
  await page.getByRole("button", { name: "Desligar" }).click();
  await expect(
    page.getByRole("heading", { name: "Ligar ao Painel" }),
  ).toBeVisible();
  await expect(page.getByLabel("Chave da API de trabalho")).toHaveValue("");
  await assertFreshBrowserState(page);
});

test("covers portfolio search, customer-safe detail, download, keyboard, and accessibility", async ({
  page,
}) => {
  await connect(page);
  await expect(page.getByRole("heading", { name: "Blocked" })).toBeVisible();
  await expect(page.getByText(approvalTitle, { exact: true })).toBeVisible();
  await expect(page.getByText(uploadTitle, { exact: true })).toBeVisible();

  const search = page.getByPlaceholder("Search customer, document, source…");
  await search.fill("no matching synthetic case");
  await expect(page.getByText("0 items shown")).toBeVisible();
  await expect(page.getByText("No items")).toHaveCount(4);
  await search.fill(doneTitle);
  await expect(page.getByText(doneTitle, { exact: true })).toBeVisible();
  await expect(page.getByText(approvalTitle, { exact: true })).toHaveCount(0);

  await page.getByText(doneTitle, { exact: true }).click();
  const detail = page.getByRole("dialog", { name: doneTitle });
  await expect(detail).toBeVisible();
  await expect(
    detail.getByRole("heading", { name: "Review completed" }),
  ).toBeVisible();
  await expect(detail.getByRole("heading", { name: "Timeline" })).toBeVisible();
  await expect(detail).not.toContainText("PRIVATE-SOURCE-MUST-NOT-LEAK-478");
  await expect(detail).not.toContainText(/prompt|worker-id|raw tool trace/i);

  const downloadPromise = page.waitForEvent("download");
  await detail.getByRole("button", { name: new RegExp(downloadName) }).click();
  const download = await downloadPromise;
  expect(download.suggestedFilename()).toBe(downloadName);
  const downloadedPath = await download.path();
  expect(downloadedPath).not.toBeNull();
  expect(readFileSync(downloadedPath!, "utf8")).toBe(downloadBody);

  await page.keyboard.press("Escape");
  await expect(detail).toHaveCount(0);
  await page.getByRole("button", { name: "PT", exact: true }).click();
  await page.getByText(doneTitle, { exact: true }).click();
  const portugueseDetail = page.getByRole("dialog", { name: doneTitle });
  await expect(
    portugueseDetail.getByRole("heading", { name: "Revisão concluída" }),
  ).toBeVisible();
  await expect(
    portugueseDetail.getByRole("heading", { name: "Cronologia" }),
  ).toBeVisible();
  await expect(
    portugueseDetail.getByRole("button", {
      name: new RegExp(`${downloadName}.*Transferir`),
    }),
  ).toBeVisible();
  await page.keyboard.press("Escape");
  await page.getByRole("button", { name: "EN", exact: true }).click();
  await search.fill("");
  await expect(page.getByText(approvalTitle, { exact: true })).toBeVisible();

  test.info().annotations.push({
    type: "accessibility-baseline",
    description:
      "WCAG 2.0/2.1 A+AA automated structural rules; color contrast stays with the existing component baseline because deterministic visual contrast is not represented by this browser fixture.",
  });
  const accessibility = await new AxeBuilder({ page })
    .withTags(["wcag2a", "wcag2aa", "wcag21a", "wcag21aa"])
    .disableRules(["color-contrast"])
    .analyze();
  expect(accessibility.violations).toEqual([]);
});

test("submits blockers, uploads/downloads artifacts, and records feedback through real APIs", async ({
  page,
}) => {
  await connect(page);

  await page.getByText(doneTitle, { exact: true }).click();
  await expect(page.getByRole("dialog", { name: doneTitle })).toBeVisible();
  await page.getByRole("button", { name: /Result needs improvement/ }).click();
  await page.getByRole("button", { name: "Poor output" }).click();
  await page
    .getByPlaceholder(/Explanation/)
    .fill("Browser-reviewed synthetic correction.");
  await page.getByRole("button", { name: "Save feedback" }).click();
  await expect(page.getByRole("status")).toContainText(
    "Feedback captured without starting new work.",
  );
  await page.keyboard.press("Escape");

  await page.getByRole("button", { name: "PT", exact: true }).click();
  await page.getByText(approvalTitle, { exact: true }).click();
  const approval = page.getByRole("dialog", { name: approvalTitle });
  await expect(
    approval.getByRole("heading", { name: "Revisão necessária" }),
  ).toBeVisible();
  await approval.getByLabel("Aprovado").check();
  await approval.getByRole("button", { name: "Enviar resposta" }).click();
  await expect(approval.getByRole("status")).toContainText("Resposta enviada");
  await expect(approval.locator(".status-badge")).toHaveText("Concluído");
  await page.keyboard.press("Escape");
  await page.getByRole("button", { name: "EN", exact: true }).click();

  await page.getByText(uploadTitle, { exact: true }).click();
  const upload = page.getByRole("dialog", { name: uploadTitle });
  await expect(
    upload.getByRole("heading", { name: "Evidence file required" }),
  ).toBeVisible();
  await upload.getByLabel("Choose the required artifact").setInputFiles({
    name: "uploaded-browser-evidence.txt",
    mimeType: "text/plain",
    buffer: Buffer.from("Synthetic Playwright upload for HOR-483.\n"),
  });
  await upload.getByLabel("Note").fill("Reviewed in the real browser journey.");
  await upload.getByRole("button", { name: "Send response" }).click();
  await expect(upload.getByRole("status")).toContainText("Response sent");
  await expect(upload.locator(".status-badge")).toHaveText("Done");
});

test("renders the customer error state and reconnects SSE after a Go-owned network break", async ({
  page,
  request,
  observability,
}) => {
  await connect(page);
  allowNetworkOutage(observability);

  const restart = await request.post(`${coordinator}/restart`);
  expect(restart.status()).toBe(202);
  await expect
    .poll(
      async () =>
        (await (await request.get(`${coordinator}/status`)).json()).status,
    )
    .toBe("outage");

  await page
    .getByPlaceholder("Search customer, document, source…")
    .fill(reconnectTitle);
  await expect(page.getByRole("alert")).toHaveText(
    "Dashboard data could not be refreshed.",
  );

  const recover = await request.post(`${coordinator}/recover`);
  expect(recover.status()).toBe(202);
  await expect
    .poll(
      async () =>
        (await (await request.get(`${coordinator}/status`)).json()).status,
      {
        timeout: 30_000,
      },
    )
    .toBe("ready");
  await expect(page.getByText(reconnectTitle, { exact: true })).toBeVisible({
    timeout: 30_000,
  });
  await expect(page.getByRole("alert")).toHaveCount(0);
});

test("keeps the critical connect, board, and detail path usable at a mobile viewport", async ({
  page,
}) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await connect(page);
  await expect(page.getByRole("region", { name: "Work" })).toBeVisible();
  await expect
    .poll(() =>
      page.evaluate(
        () =>
          document.documentElement.scrollWidth <=
          document.documentElement.clientWidth + 1,
      ),
    )
    .toBe(true);

  await page.getByText(doneTitle, { exact: true }).click();
  const detail = page.getByRole("dialog", { name: doneTitle });
  await expect(detail).toBeVisible();
  await expect(detail.getByRole("heading", { name: "Timeline" })).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(detail).toHaveCount(0);
});
