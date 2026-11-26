// SPDX-License-Identifier: MPL-2.0
// Trusted harness for the disposable checkout example. Run through local-browser-qa.
import { readFile } from "node:fs/promises";
import { join } from "node:path";
import { pathToFileURL } from "node:url";

const [playwright, executablePath] = process.argv.slice(2);
const { chromium } = await import(pathToFileURL(playwright).href);
const chunks = [];
for await (const chunk of process.stdin) chunks.push(chunk);
const { checkout, revision } = JSON.parse(
  Buffer.concat(chunks).toString("utf8"),
);

const base = "https://checkout.test";
const assets = new Set([
  "index.html",
  "app.mjs",
  "config.mjs",
  "shipping.mjs",
  "telemetry.mjs",
]);
const browser = await chromium.launch(
  executablePath
    ? { executablePath, headless: true }
    : { channel: "chrome", headless: true },
);
try {
  const page = await browser.newPage({
    viewport: { width: 760, height: 460 },
    serviceWorkers: "block",
  });
  // Serve the exported files inside this browser context. Block other requests.
  await page.route("**/*", async (route) => {
    const url = new URL(route.request().url());
    const file = url.pathname === "/" ? "index.html" : url.pathname.slice(1);
    if (url.origin !== base || !assets.has(file)) return route.abort();
    return route.fulfill({
      contentType: file.endsWith(".html") ? "text/html" : "text/javascript",
      body: await readFile(join(checkout, file)),
    });
  });
  await page.goto(base, { timeout: 10000 });
  await page.getByText("Sentry reporting is off.").waitFor({ timeout: 10000 });
  await page
    .getByRole("button", { name: "Calculate shipping" })
    .click({ timeout: 10000 });
  const observed = await page.locator("output").textContent();
  if (
    observed !== "Free shipping applied." &&
    observed !== "A $5.00 shipping fee was added."
  ) {
    throw new Error("The checkout did not return a shipping result");
  }
  const screenshot = await page.locator("main").screenshot({ timeout: 10000 });
  process.stdout.write(
    JSON.stringify({
      revision,
      result: observed === "Free shipping applied." ? "pass" : "fail",
      observed,
      screenshot: screenshot.toString("base64"),
    }) + "\n",
  );
} finally {
  await browser.close();
}
