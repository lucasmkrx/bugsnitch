// SPDX-License-Identifier: MPL-2.0
// Used by TestLiveCheckoutIncidentRepair against its ephemeral loopback site.
import assert from "node:assert/strict";
import { chromium } from "playwright-core";

const [origin, behavior, release] = process.argv.slice(2);
assert.equal(new URL(origin).hostname, "127.0.0.1");
assert.ok(["broken", "fixed"].includes(behavior));
const browser = await chromium.launch(
  process.env.BUGSNITCH_TEST_CHROME
    ? { executablePath: process.env.BUGSNITCH_TEST_CHROME, headless: true }
    : { channel: "chrome", headless: true },
);
try {
  const page = await browser.newPage();
  const events = [];
  await page.route("**/*", async (route) => {
    const request = route.request();
    const url = new URL(request.url());
    if (url.origin !== origin) return route.abort();
    if (url.pathname !== "/api/21/envelope/") return route.continue();
    assert.equal(request.method(), "POST");
    const parts = request.postDataBuffer().toString().trim().split("\n");
    assert.equal(JSON.parse(parts[1]).type, "event");
    events.push(JSON.parse(parts[2]));
    return route.fulfill({ status: 200, contentType: "application/json", body: "{}" });
  });
  await page.goto(origin);
  await page.getByText("Sentry reporting enabled for this test project.").waitFor();
  await page.getByRole("button", { name: "Calculate shipping" }).click();
  const expected = behavior === "fixed" ? "Free shipping applied." : "A $5.00 shipping fee was added.";
  await page.getByText(expected, { exact: true }).waitFor();
  if (behavior === "broken") {
    await page.getByText("The SDK finished sending queued reports.", { exact: false }).waitFor();
    assert.equal(events.length, 1);
    assert.equal(events[0].release, release);
    assert.equal(events[0].environment, "bugsnitch-fixture");
    assert.equal(events[0].exception.values[0].type, "ShippingThresholdError");
    assert.deepEqual(events[0].fingerprint, ["checkout", "free-shipping-threshold-v1"]);
  } else {
    assert.equal(events.length, 0);
  }
  process.stdout.write(JSON.stringify(events));
} finally {
  await browser.close();
}
