// SPDX-License-Identifier: MPL-2.0
// Called by scripts/smoke.py with credentials scoped to its disposable service.
import assert from "node:assert/strict";
import { chromium } from "playwright-core";

const origin = process.env.BUGSNITCH_ORIGIN;
const token = process.env.BUGSNITCH_OWNER_TOKEN;
assert.ok(
  origin?.startsWith("http://127.0.0.1:") && token,
  "Run through scripts/smoke.py",
);
const browser = await chromium.launch({ channel: "chrome", headless: true });
try {
  for (const viewport of [
    { width: 1440, height: 900 },
    { width: 360, height: 800 },
  ]) {
    const page = await browser.newPage({ viewport });
    const errors = [];
    page.on("pageerror", (error) => errors.push(error.message));
    await page.goto(origin + "/app/");
    await page.getByLabel("Owner token").fill(token);
    await page
      .locator('form button[type="submit"],form button.primary')
      .first()
      .click();
    await page
      .getByRole("heading", { name: "Overview", exact: true })
      .waitFor();
    async function visit(name) {
      if (viewport.width === 360)
        await page.getByRole("button", { name: "Open navigation" }).click();
      const nav =
        viewport.width === 360
          ? page.getByRole("dialog")
          : page.getByRole("navigation");
      await nav.getByRole("link", { name, exact: true }).click();
      await page.getByRole("heading", { name, exact: true }).first().waitFor();
    }
    await visit("Incidents");
    await page.getByText("Preview is blank", { exact: true }).first().waitFor();
    await visit("Devices");
    await page.getByText("one", { exact: true }).first().waitFor();
    await page.getByText("two", { exact: true }).first().waitFor();
    assert.ok(
      await page.evaluate(
        () => document.documentElement.scrollWidth <= innerWidth,
      ),
    );
    assert.deepEqual(errors, []);
    await page.close();
  }
} finally {
  await browser.close();
}
