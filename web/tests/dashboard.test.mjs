// SPDX-License-Identifier: MPL-2.0
// Browser protocol fixtures. All records in this suite are synthetic.
import test from "node:test";
import assert from "node:assert/strict";
import { createServer } from "node:http";
import { readFile, mkdir } from "node:fs/promises";
import { resolve, extname } from "node:path";
import { chromium } from "playwright-core";

const staticRoot = resolve("dist");
const types = {
  ".html": "text/html",
  ".js": "text/javascript",
  ".css": "text/css",
  ".svg": "image/svg+xml",
};
async function staticHost() {
  const server = createServer(async (req, res) => {
    const path = new URL(req.url, "http://local").pathname;
    const file = resolve(
      staticRoot,
      "." + (path === "/" ? "/index.html" : path),
    );
    if (!file.startsWith(staticRoot + "/")) {
      res.writeHead(404).end();
      return;
    }
    try {
      const body = await readFile(file);
      res
        .writeHead(200, {
          "content-type": types[extname(file)] || "application/octet-stream",
        })
        .end(body);
    } catch {
      res.writeHead(404).end();
    }
  });
  await new Promise((ok, fail) =>
    server.once("error", fail).listen(0, "127.0.0.1", ok),
  );
  return { server, url: "http://127.0.0.1:" + server.address().port };
}
const project = {
  id: "p1",
  name: "Storefront",
  repository: "example/storefront",
  paused: true,
  version: 4,
  preset: "Supervised",
  policyVersion: 1,
  retentionDays: 30,
  requirePlan: false,
  allowPublication: true,
  allowPromotion: true,
  allowDraftPR: true,
  autoTriage: false,
};
const decision = {
  id: "d1",
  projectId: "p1",
  subjectVersion: 4,
  version: 1,
  status: "pending",
  question: "Allow this project to accept new work again?",
  options: ["Resume dispatch", "Keep paused"],
  expiresAt: "2026-12-01T18:00:00Z",
  answer: null,
  requestId: null,
};
function fixture() {
  return {
    workspace: {
      id: "w1",
      name: "Test workspace",
      stopped: false,
      dispatchVersion: 1,
    },
    projects: [{ ...project }],
    devices: [],
    incidents: [],
    publications: [],
    limits: { version: 1, tickets: 2, turns: 2 },
    decisions: [{ ...decision }],
    attempts: [],
    tickets: [],
    plans: [],
    pullRequests: [],
    messages: [],
    events: [
      {
        seq: 1,
        projectId: "p1",
        kind: "project.created",
        subjectId: "p1",
        createdAt: "2026-11-21T14:03:00Z",
      },
    ],
    capabilities: {
      hostedInference: false,
      reason: "Hosted inference requires deployment authorization.",
    },
    updatedAt: "2026-11-21T15:06:00Z",
  };
}
test("desktop and mobile approval, stale state, replay and owner navigation", async (t) => {
  const { server, url } = await staticHost();
  t.after(() => server.close());
  const browser = await chromium.launch(
    process.env.BUGSNITCH_TEST_CHROME
      ? { executablePath: process.env.BUGSNITCH_TEST_CHROME, headless: true }
      : { channel: "chrome", headless: true },
  );
  t.after(() => browser.close());
  for (const viewport of [
    { width: 1440, height: 900 },
    { width: 360, height: 800 },
  ]) {
    await t.test(viewport.width + "px viewport", async () => {
      const c = await browser.newContext({ viewport });
      try {
        let state = fixture(),
          mode = "normal",
          answers = [];
        let artifactReads = 0;
        const page = await c.newPage(),
          errors = [];
        page.on("pageerror", (e) => errors.push(e.message));
        await page.route("**/api/v1/**", async (route) => {
          const req = route.request(),
            path = new URL(req.url()).pathname;
          if (path.endsWith("/state")) return route.fulfill({ json: state });
          if (path.endsWith("/answer")) {
            const body = req.postDataJSON();
            answers.push(body);
            if (mode === "lose-request") {
              mode = "normal";
              return route.abort("connectionreset");
            }
            Object.assign(state.decisions[0], {
              answer: body.answer,
              requestId: body.requestId,
              status: "answered",
              version: 2,
            });
            if (mode === "lose-reply") {
              mode = "normal";
              return route.abort("connectionreset");
            }
            return route.fulfill({ json: { id: "d1", answer: body.answer } });
          }
          if (path.endsWith("/policy")) {
            const b = req.postDataJSON();
            assert.deepEqual(b, {
              version: 4,
              preset: "Autonomous within limits",
            });
            Object.assign(state.projects[0], {
              preset: b.preset,
              autoTriage: true,
              requirePlan: true,
              version: 5,
            });
            return route.fulfill({ json: { preset: b.preset } });
          }
          if (path.endsWith("/attempts/a1/diff")) {
            artifactReads++;
            const metadata = state.attempts[0].sharedDiff;
            return route.fulfill({
              json: {
                ...metadata,
                digest: artifactReads === 1 ? "old" : metadata.digest,
                text: "+<img src=x onerror=alert(1)>" + "a".repeat(350),
              },
            });
          }
          if (path.endsWith("/retention")) {
            assert.deepEqual(req.postDataJSON(), { version: 5, days: 7 });
            state.projects[0].version++;
            state.projects[0].retentionDays = 7;
            return route.fulfill({ json: { days: 7 } });
          }
          if (path.endsWith("/workspace/dispatch")) {
            const body = req.postDataJSON();
            assert.equal(body.version, state.workspace.dispatchVersion);
            assert.ok(body.requestId);
            Object.assign(state.workspace, {
              stopped: body.stopped,
              dispatchVersion: body.version + 1,
            });
            return route.fulfill({ status: 202, json: { accepted: true } });
          }
          if (path.endsWith("/publications")) {
            const b = req.postDataJSON();
            assert.equal(b.plan.tickets[0].number, 1);
            state.publications = [
              {
                id: "pub1",
                projectId: "p1",
                version: 1,
                status: "proposed",
                document: b.plan,
                issues: [],
              },
            ];
            return route.fulfill({
              status: 201,
              json: { id: "pub1", status: "proposed" },
            });
          }
          if (path.endsWith("/publications/pub1/approve")) {
            assert.deepEqual(req.postDataJSON(), { version: 1 });
            state.publications[0].status = "accepted";
            return route.fulfill({ status: 202, json: { status: "accepted" } });
          }
          if (path.endsWith("/pairings"))
            return route.fulfill({
              status: 201,
              json: {
                code: "0123456789abcdef",
                expiresAt: "2026-11-21T15:16:00Z",
              },
            });
          return route.fulfill({
            status: 404,
            json: { error: "Unconfigured test route" },
          });
        });
        await page.goto(url);
        await page
          .getByRole("heading", { name: "Overview", exact: true })
          .waitFor();
        if (process.env.BUGSNITCH_SCREENSHOTS) {
          await mkdir(process.env.BUGSNITCH_SCREENSHOTS, { recursive: true });
          await page.screenshot({
            path: resolve(
              process.env.BUGSNITCH_SCREENSHOTS,
              "fixture-overview-" + viewport.width + ".png",
            ),
          });
        }
        await page.keyboard.press("Tab");
        assert.equal(
          await page.locator(":focus").innerText(),
          "Skip to content",
        );
        await page.keyboard.press("Enter");
        assert.equal(
          await page.locator(":focus").getAttribute("id"),
          "main-content",
        );
        const noOverflow = async () =>
          assert.ok(
            await page.evaluate(
              () => document.documentElement.scrollWidth <= innerWidth,
            ),
            "horizontal page overflow",
          );
        await noOverflow();
        if (viewport.width === 360) {
          await page.getByRole("button", { name: "Open navigation" }).click();
          await page
            .getByRole("dialog")
            .getByRole("link", { name: /Decisions/ })
            .click();
        } else
          await page
            .getByRole("navigation")
            .getByRole("link", { name: /Decisions/ })
            .click();
        await page
          .getByRole("button", { name: "Keep paused", exact: true })
          .waitFor();
        // The open tab still has the old record. A fresh read on click must prevent this POST.
        state.decisions[0].status = "stale";
        await page
          .getByRole("button", { name: "Keep paused", exact: true })
          .click();
        await page
          .getByRole("alert")
          .filter({ hasText: "This decision changed" })
          .waitFor();
        assert.equal(answers.length, 0);
        state = fixture();
        await page.reload();
        await page
          .getByRole("button", { name: "Keep paused", exact: true })
          .waitFor();
        await c.setOffline(true);
        await page
          .getByText(/Offline. Displaying the last received workspace state/)
          .waitFor();
        assert.equal(
          await page
            .getByRole("button", { name: "Keep paused", exact: true })
            .isDisabled(),
          true,
        );
        await c.setOffline(false);
        await page.waitForFunction(() =>
          [...document.querySelectorAll("button")].some(
            (b) => b.textContent === "Keep paused" && !b.disabled,
          ),
        );
        // Delivery failed before the service committed. An explicit retry must reuse the same ID.
        mode = "lose-request";
        await page
          .getByRole("button", { name: "Keep paused", exact: true })
          .click();
        await page
          .getByRole("alert")
          .filter({ hasText: "service could not be reached" })
          .waitFor();
        assert.equal(answers.length, 1);
        await page
          .getByRole("button", { name: "Keep paused", exact: true })
          .click();
        await page.getByText("Recorded answer · Keep paused").waitFor();
        assert.equal(answers.length, 2);
        assert.equal(answers[0].requestId, answers[1].requestId);
        // A completed answer is read back after reload without replaying a mutation.
        await page.reload();
        await page.getByText("Recorded answer · Keep paused").waitFor();
        assert.equal(answers.length, 2);
        state = fixture();
        await page.reload();
        await page
          .getByRole("button", { name: "Resume dispatch", exact: true })
          .waitFor();
        mode = "lose-reply";
        await page
          .getByRole("button", { name: "Resume dispatch", exact: true })
          .click();
        await page.getByText("Recorded answer · Resume dispatch").waitFor();
        assert.equal(answers.length, 3);
        await page.reload();
        await page.getByText("Recorded answer · Resume dispatch").waitFor();
        assert.equal(answers.length, 3);
        for (const section of [
          "plans",
          "work",
          "prs",
          "incidents",
          "devices",
          "chat",
          "settings",
        ]) {
          await page.goto(url + "/#/" + section + "?project=p1");
          await page
            .getByRole("main")
            .getByRole("heading", { level: 1 })
            .waitFor();
          await noOverflow();
        }
        await page.goto(url + "/#/devices?project=p1");
        await page
          .getByRole("button", { name: "Pair device", exact: true })
          .click();
        await page.getByLabel("Device name").fill("Test Mac");
        await page.getByRole("button", { name: "Generate code" }).click();
        await page.getByText("0123456789abcdef", { exact: true }).waitFor();
        await page.getByRole("button", { name: "Done", exact: true }).click();
        await noOverflow();
        await page.goto(url + "/#/settings?project=p1");
        await page.getByRole("button", { name: "Edit project policy" }).click();
        await page
          .getByLabel("Autonomy preset")
          .selectOption("Autonomous within limits");
        await page.getByRole("button", { name: "Review changes" }).click();
        await page
          .getByRole("dialog", { name: "Confirm policy change" })
          .waitFor();
        assert.equal(state.projects[0].autoTriage, false);
        await page.getByRole("button", { name: "Apply policy" }).click();
        await page.getByText("Project policy saved.").waitFor();
        await page.goto(url + "/#/plans?project=p1");
        const plan = {
          id: "plan-1",
          projectId: "p1",
          version: 1,
          title: "Storefront launch",
          status: "approved",
          document: {
            title: "Storefront launch",
            milestone: "Release",
            target: "2026-11-28",
            forecast: "2026-11-27",
            assumptions: "Normal traffic",
            tickets: [1, 2, 3].map((n) => ({
              number: n,
              title: "Ticket " + n,
              criteria: ["Behavior verified"],
              verification: "Integration test",
              priority: 1,
              estimateHours: 4,
              dependencies: [],
              resources: [],
            })),
          },
          executionOrder: [1, 2, 3],
          progressForecast: {
            status: "unknown",
            completedTickets: 1,
            totalTickets: 3,
            completedEstimateHours: 4,
            totalEstimateHours: 12,
            asOf: "2026-11-21",
            reason: "Completion evidence spans only one day.",
          },
        };
        state.plans = [plan];
        await page.reload();
        const planPanel = page
          .locator("section")
          .filter({
            has: page.getByRole("heading", { name: plan.title, exact: true }),
          });
        await planPanel
          .getByText("Completion evidence spans only one day.")
          .waitFor();
        assert.equal(
          await planPanel
            .getByText("Projected finish", { exact: true })
            .count(),
          0,
        );
        Object.assign(plan.progressForecast, {
          status: "projected",
          completedTickets: 2,
          completedEstimateHours: 8,
          date: "2026-11-30",
          targetDeltaDays: 2,
        });
        delete plan.progressForecast.reason;
        await page.reload();
        await planPanel
          .getByText("2026-11-30 · 2 days after target", { exact: true })
          .waitFor();
        await planPanel.getByText("2026-11-28", { exact: true }).waitFor();
        await planPanel.getByText("2026-11-27", { exact: true }).waitFor();
        await planPanel
          .getByText(/2 of 3 tickets with integration evidence/)
          .waitFor();
        await noOverflow();
        Object.assign(plan.progressForecast, {
          status: "complete",
          completedTickets: 3,
          completedEstimateHours: 12,
          date: "2026-11-28",
          asOf: "2026-11-28",
          targetDeltaDays: 0,
        });
        await page.reload();
        await planPanel
          .getByText("Recorded completion", { exact: true })
          .waitFor();
        await planPanel
          .getByText("2026-11-28 · On target date", { exact: true })
          .waitFor();
        assert.equal(
          await planPanel
            .getByText("Projected finish", { exact: true })
            .count(),
          0,
        );
        await page.getByRole("button", { name: "Draft plan" }).click();
        await page.getByLabel("Ticket source").selectOption("new");
        await page
          .getByLabel("Deliverable", { exact: true })
          .fill("Accurate totals");
        await page.getByLabel("Milestone", { exact: true }).fill("Rounding");
        await page.getByLabel("Provisional ticket number").fill("1");
        await page.getByLabel("Ticket title").fill("Round to cents");
        await page
          .getByLabel(/Acceptance criteria/)
          .fill("Fractional cents are rounded.");
        await page.getByLabel("Verification method").fill("Unit tests");
        await page
          .getByRole("button", { name: "Save publication proposal" })
          .click();
        await page.getByRole("button", { name: "Review publication" }).click();
        await page
          .getByRole("dialog", { name: "Create GitHub issues?" })
          .waitFor();
        assert.equal(state.publications[0].status, "proposed");
        await page
          .getByRole("button", { name: "Authorize issue creation" })
          .click();
        await page.getByText(/Publication accepted/).waitFor();
        assert.equal(state.publications[0].status, "accepted");
        await noOverflow();
        await page.goto(url + "/#/settings?project=p1");
        await page
          .getByRole("button", { name: "Review emergency stop" })
          .click();
        assert.equal(state.workspace.stopped, false);
        await page
          .getByRole("button", { name: "Stop workspace", exact: true })
          .click();
        await page
          .getByRole("button", { name: "Review workspace unlock" })
          .waitFor();
        assert.equal(state.workspace.stopped, true);
        await page
          .getByRole("button", { name: "Review workspace unlock" })
          .click();
        await page
          .getByRole("button", { name: "Unlock workspace", exact: true })
          .click();
        await page
          .getByRole("button", { name: "Review emergency stop" })
          .waitFor();
        assert.equal(state.projects[0].paused, true);
        await page.getByRole("button", { name: "Use dark theme" }).click();
        await page.reload();
        await page.getByRole("button", { name: "Use light theme" }).waitFor();
        await noOverflow();
        await page.getByLabel("Content retention in days").fill("7");
        await page.getByRole("button", { name: "Save retention" }).click();
        assert.equal(state.projects[0].retentionDays, 30);
        await page
          .getByRole("button", { name: "Apply shorter retention" })
          .click();
        await page.getByText("Retention period saved.").waitFor();
        const sha = "b".repeat(40),
          base = "a".repeat(40);
        state.attempts = [
          {
            id: "a1",
            projectId: "p1",
            machine: "Test Mac",
            repository: project.repository,
            issueNumber: 1,
            phase: "review",
            state: "completed",
            updatedAt: state.updatedAt,
            baseSha: base,
            candidateSha: sha,
            connection: "connected",
            version: 3,
            sharedDiff: {
              digest: "c".repeat(64),
              candidateSha: sha,
              baseSha: base,
              sharedAt: state.updatedAt,
            },
          },
        ];
        await page.goto(url + "/#/work?project=p1&id=a1");
        await page.reload();
        await page.getByRole("button", { name: "Load shared diff" }).waitFor();
        assert.equal(artifactReads, 0);
        await page.getByRole("button", { name: "Load shared diff" }).click();
        await page.getByText(/The shared diff changed/).waitFor();
        assert.equal(
          await page
            .getByRole("region", { name: /Shared source diff/ })
            .count(),
          0,
        );
        await page.getByRole("button", { name: "Load shared diff" }).click();
        await page
          .getByRole("region", { name: /Shared source diff/ })
          .waitFor();
        assert.match(
          await page
            .getByRole("region", { name: /Shared source diff/ })
            .innerText(),
          /^\+<img/,
        );
        assert.equal(
          await page
            .getByRole("region", { name: /Shared source diff/ })
            .locator("img")
            .count(),
          0,
        );
        await noOverflow();
        assert.deepEqual(errors, []);
      } finally {
        await c.close();
      }
    });
  }
});
