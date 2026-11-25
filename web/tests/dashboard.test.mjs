// SPDX-License-Identifier: MPL-2.0
// Browser protocol fixtures. All records in this suite are synthetic.
import test from "node:test";
import assert from "node:assert/strict";
import { createServer } from "node:http";
import { readFile, mkdir } from "node:fs/promises";
import { resolve, extname } from "node:path";
import { chromium } from "playwright-core";
import { createServer as createViteServer } from "vite";

const staticRoot = resolve("dist");
const types = {
  ".html": "text/html",
  ".js": "text/javascript",
  ".css": "text/css",
  ".svg": "image/svg+xml",
  ".jpg": "image/jpeg",
  ".png": "image/png",
  ".woff2": "font/woff2",
};
async function staticHost() {
  const server = createServer(async (req, res) => {
    const path = new URL(req.url, "http://local").pathname;
    const entry =
      path === "/"
        ? "/index.html"
        : path === "/app" || path === "/app/"
          ? "/app/index.html"
          : path;
    const file = resolve(staticRoot, "." + entry);
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

async function expectHomepageLogos(page) {
  const logos = page.locator(".wordmark img");
  assert.equal(await logos.count(), 2);
  for (const logo of await logos.all()) {
    await logo.evaluate((image) => image.decode());
    assert.ok(await logo.isVisible());
  }
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
  const { server, url: origin } = await staticHost();
  const url = origin + "/app";
  t.after(() => server.close());
  const browser = await chromium.launch(
    process.env.BUGSNITCH_TEST_CHROME
      ? { executablePath: process.env.BUGSNITCH_TEST_CHROME, headless: true }
      : { channel: "chrome", headless: true },
  );
  t.after(() => browser.close());
  await t.test("invited operators sign in with an emailed code", async (t) => {
    const page = await browser.newPage();
    t.after(() => page.close());
    let signedIn = false;
    await page.route(
      "https://www.google.com/recaptcha/enterprise.js?render=fixture",
      (route) =>
        route.fulfill({
          contentType: "text/javascript",
          body: `window.grecaptcha = { enterprise: {
          ready: (callback) => callback(),
          execute: async (key, options) => {
            if (key !== "fixture" || options.action !== "email_sign_in") throw Error("Unexpected CAPTCHA request");
            return "fixture-captcha-token";
          },
        }};`,
        }),
    );
    await page.route("**/api/v1/**", (route) => {
      const request = route.request();
      switch (new URL(request.url()).pathname) {
        case "/api/v1/login/email":
          return route.fulfill({ json: { siteKey: "fixture" } });
        case "/api/v1/login/email/start":
          assert.deepEqual(request.postDataJSON(), {
            email: "operator@example.test",
            captchaToken: "fixture-captcha-token",
          });
          return route.fulfill({ json: { challenge: "fixture-challenge" } });
        case "/api/v1/login/email/verify": {
          const body = request.postDataJSON();
          assert.equal(body.challenge, "fixture-challenge");
          if (body.code !== "123456")
            return route.fulfill({
              status: 401,
              json: { error: "Invalid or expired sign-in code" },
            });
          signedIn = true;
          return route.fulfill({ json: { authenticated: true } });
        }
        case "/api/v1/state":
          return signedIn
            ? route.fulfill({
                json: {
                  ...fixture(),
                  viewer: { id: "op", role: "operator", name: "Operator" },
                },
              })
            : route.fulfill({
                status: 401,
                json: { error: "Sign in to Bugsnitch" },
              });
        default:
          return route.fulfill({ status: 404, json: {} });
      }
    });
    await page.goto(url);
    await page.getByLabel("Email address").fill("operator@example.test");
    await page.getByRole("button", { name: "Email a code" }).click();
    await page.getByLabel("Sign-in code").fill("000000");
    await page.getByRole("button", { name: "Sign in", exact: true }).click();
    await page
      .getByRole("alert")
      .filter({ hasText: "Invalid or expired sign-in code" })
      .waitFor();
    await page.getByLabel("Sign-in code").fill("123456");
    await page.getByRole("button", { name: "Sign in", exact: true }).click();
    await page
      .getByRole("heading", { name: "Overview", exact: true })
      .waitFor();
  });
  await t.test(
    "a stale draft approval can be withdrawn and renewed on the exact candidate",
    async (t) => {
      const page = await browser.newPage();
      t.after(() => page.close());
      const state = fixture(),
        id = "c".repeat(32),
        base = "a".repeat(40),
        head = "b".repeat(40);
      state.projects[0].paused = false;
      state.attempts = [
        {
          id,
          projectId: "p1",
          repository: project.repository,
          machine: "Builder",
          issueNumber: 7,
          phase: "wrap_up",
          state: "completed",
          baseSha: base,
          candidateSha: head,
          version: 8,
          connection: "connected",
          updatedAt: state.updatedAt,
        },
      ];
      const intent = {
        attemptId: id,
        projectId: "p1",
        candidateSha: head,
        baseSha: base,
        target: "staging",
        status: "awaiting_publication",
        reportVersion: 7,
        policyVersion: 1,
        approvalVersion: 1,
        updatedAt: state.updatedAt,
      };
      state.draftPRs = [intent];
      const writes = [];
      await page.route("**/api/v1/state", (r) => r.fulfill({ json: state }));
      await page.route("**/draft-pr", (r) => {
        const req = r.request().postDataJSON();
        writes.push(req);
        intent.status =
          req.delivery === "withdraw" ? "withdrawn" : "awaiting_publication";
        intent.approvalVersion++;
        intent.reportVersion = req.version;
        intent.target = req.target;
        return r.fulfill({ json: intent });
      });
      await page.goto(url + "/#/work?project=p1&id=" + id);
      await page.getByText(/The attempt version changed/).waitFor();
      await page
        .getByRole("button", { name: "Withdraw publication approval" })
        .click();
      await page.getByLabel("Target branch").fill("staging");
      await page
        .getByRole("button", { name: /Review.*(draft|approval)/i })
        .click();
      await page
        .getByRole("dialog")
        .getByRole("button", { name: "Request draft PR" })
        .click();
      await page
        .getByText(
          "bugsnitch local-ship --id " +
            id +
            " --version 8 --candidate " +
            head +
            " --target staging",
          { exact: true },
        )
        .waitFor();
      assert.deepEqual(
        writes.map((x) => [
          x.delivery,
          x.version,
          x.approvalVersion,
          x.candidateSha,
        ]),
        [
          ["withdraw", 7, 1, head],
          ["local_ship", 8, 2, head],
        ],
      );
    },
  );
  await t.test(
    "owner connects a repository, scopes access, budgets turns and orders releases",
    async (t) => {
      const page = await browser.newPage({
        viewport: { width: 390, height: 844 },
      });
      t.after(() => page.close());
      const state = fixture();
      state.viewer = { id: "owner", role: "owner", name: "Owner" };
      state.budget = { usedTurns: 2, monthlyTurns: null };
      state.projects.push({
        ...project,
        id: "p2",
        name: "Payments",
        repository: "example/payments",
      });
      state.pullRequests = state.projects.map((p, i) => ({
        projectId: p.id,
        number: i + 10,
        state: "OPEN",
        head: String(i + 1).repeat(40),
        target: "main",
      }));
      const members = [],
        writes = [];
      await page.route("**/api/v1/**", (r) => {
        const path = new URL(r.request().url()).pathname.slice(
          "/api/v1/".length,
        );
        if (path === "state") return r.fulfill({ json: state });
        if (path === "login/email")
          return r.fulfill({ json: { siteKey: null } });
        if (path === "members" && r.request().method() === "GET")
          return r.fulfill({ json: members });
        if (path.endsWith("/review-subjects")) return r.fulfill({ json: [] });
        const budget = {
          projectId: "p2",
          version: 0,
          mode: "off",
          thresholdUSD: null,
          month: "2026-11-01",
          estimatedUSD: 2,
          admittedTurns: 2,
          meteredTurns: 1,
          pricedTurns: 1,
          reached: false,
        };
        if (path === "projects/p2/usage")
          return r.fulfill({
            json: {
              monthlyThreshold: budget,
              admittedTurns: 2,
              unmeteredTurns: 1,
              unpricedMeteredTurns: 0,
              turnsOnFailedOrCancelledAttempts: 0,
              models: [
                {
                  model: "test",
                  turns: 1,
                  pricedTurns: 1,
                  inputTokens: 10,
                  outputTokens: 4,
                  estimatedUSD: 2,
                },
              ],
            },
          });
        const data = r.request().postDataJSON();
        writes.push([path, data]);
        if (path === "projects/p2/usage/threshold")
          return r.fulfill({
            json: { ...budget, ...data, version: 1, reached: true },
          });
        if (path === "projects/p1/github")
          state.projects[0].githubConnected = true;
        if (path === "members") {
          members.push({ id: "approver", ...data, revokedAt: null });
          return r.fulfill({ json: { credential: "test-access-key" } });
        }
        if (path === "budget") state.budget.monthlyTurns = data.monthlyTurns;
        if (path === "releases")
          state.releases = [
            {
              id: data.requestId,
              name: data.name,
              environment: data.environment,
              stages: data.stages.map((s) => ({
                projectId: s.projectId,
                number: s.pullNumber,
                status: "pending_merge",
              })),
            },
          ];
        return r.fulfill({ json: { ok: true } });
      });
      await page.goto(url + "/#/settings");
      await page
        .getByRole("button", { name: "Connect repository" })
        .first()
        .click();
      await page.getByText("Installation connected.").waitFor();
      await page.getByLabel("Name", { exact: true }).fill("Release lead");
      await page
        .getByRole("combobox", { name: /^Role/ })
        .selectOption("approver");
      await page.getByLabel("Project access").selectOption("p2");
      await page.getByRole("button", { name: "Create access key" }).click();
      await page
        .getByRole("dialog")
        .getByRole("button", { name: "Done" })
        .click();
      await page.getByLabel(/Monthly turns/).fill("20");
      await page.getByRole("button", { name: "Save turn budget" }).click();
      await page.getByText("Monthly turn budget saved.").waitFor();
      await page.getByLabel("Release name").fill("Payments then storefront");
      await page.getByLabel("Environment", { exact: true }).fill("staging");
      for (const ref of ["p2/11", "p1/10"]) {
        await page
          .getByLabel("Next pull request in release order")
          .selectOption(ref);
        await page.getByRole("button", { name: "Add stage" }).click();
      }
      await page.getByRole("button", { name: "Create release plan" }).click();
      await page
        .getByRole("heading", { name: "Payments then storefront · staging" })
        .waitFor();
      assert.deepEqual(writes.slice(0, 3), [
        ["projects/p1/github", { version: 4 }],
        [
          "members",
          { name: "Release lead", role: "approver", projectId: "p2" },
        ],
        ["budget", { monthlyTurns: 20, version: 1 }],
      ]);
      assert.equal(writes[3][0], "releases");
      assert.deepEqual(writes[3][1].stages, [
        {
          projectId: "p2",
          pullNumber: 11,
          head: "2".repeat(40),
          target: "main",
        },
        {
          projectId: "p1",
          pullNumber: 10,
          head: "1".repeat(40),
          target: "main",
        },
      ]);
      await page.getByLabel("Usage scope").selectOption("p2");
      await page.getByRole("button", { name: "Read usage" }).click();
      await page
        .getByLabel("When the threshold is reached")
        .selectOption("stop");
      await page.getByLabel("Estimated USD per UTC month").fill("1");
      await page.getByRole("button", { name: "Save cost threshold" }).click();
      await page
        .getByText("New paired turns are paused for this scope.")
        .waitFor();
      assert.deepEqual(writes[4], [
        "projects/p2/usage/threshold",
        { mode: "stop", thresholdUSD: 1, version: 0 },
      ]);
    },
  );
  await t.test("the local preview loads the illustrated hero", async (t) => {
    const dev = await createViteServer({
      server: { host: "127.0.0.1", port: 5174, strictPort: false, hmr: false },
    });
    t.after(() => dev.close());
    await dev.listen();
    for (const width of [1440, 360]) {
      const page = await browser.newPage({
        viewport: { width, height: 900 },
        deviceScaleFactor: width === 1440 ? 2 : 1,
      });
      t.after(() => page.close());
      await page.goto(dev.resolvedUrls.local[0]);
      await expectHomepageLogos(page);
      const copy = page.locator(".hero-copy");
      const illustration = page.locator(".hero-illustration");
      const source = await illustration.evaluate(async (image) => {
        await image.decode();
        return new URL(image.currentSrc).pathname;
      });
      assert.equal(
        source,
        width === 1440
          ? "/brand/factory-workflow.jpg"
          : "/brand/factory-workflow-720.jpg",
      );
      const textBounds = await copy.boundingBox();
      const artBounds = await illustration.boundingBox();
      if (width === 1440) {
        assert.ok(artBounds.x >= textBounds.x + textBounds.width);
      } else {
        assert.equal(artBounds.x, textBounds.x);
        assert.ok(artBounds.y >= textBounds.y + textBounds.height);
      }
      const releasePath = page.getByRole("list", {
        name: "Example run from issue to release",
      });
      await releasePath.getByText("Verified build", { exact: true }).waitFor();
      assert.equal(await releasePath.getByRole("listitem").count(), 4);
    }
  });
  await t.test(
    "homepage works on desktop and mobile and opens the workspace",
    async (t) => {
      for (const width of [1440, 360]) {
        const page = await browser.newPage({
          viewport: { width, height: 900 },
          colorScheme: "dark",
          permissions: ["clipboard-read", "clipboard-write"],
        });
        t.after(() => page.close());
        await page.emulateMedia({
          reducedMotion: "no-preference",
        });
        await page.route("**/api/v1/state", (route) =>
          route.fulfill({ json: fixture() }),
        );
        await page.goto(origin);
        await expectHomepageLogos(page);
        await page
          .getByRole("heading", {
            name: "From issue to verified release.",
            level: 1,
          })
          .waitFor();
        await page.evaluate(() => document.fonts.ready);
        assert.deepEqual(
          await page.locator(".site-header .wordmark").evaluate((wordmark) => {
            const style = getComputedStyle(wordmark);
            return [
              style.fontFamily,
              style.fontWeight,
              style.colorScheme,
              style.getPropertyValue("--brand-accent").trim(),
              document.fonts.check('700 24px "IBM Plex Mono"'),
            ];
          }),
          [
            '"IBM Plex Mono", ui-monospace, monospace',
            "700",
            "light",
            "#d40000",
            true,
          ],
        );
        const favicon = await page
          .locator('link[rel="icon"][type="image/svg+xml"]')
          .getAttribute("href");
        const headerIcon = page.locator(".site-header .wordmark img");
        const icon = await headerIcon.getAttribute("src");
        assert.equal(
          (await headerIcon.boundingBox()).width,
          width === 1440 ? 48 : 38,
        );
        assert.equal(favicon, "/brand/favicon.svg");
        if (width === 1440) {
          const [faviconArt, canonicalArt] = await page.evaluate(
            (urls) =>
              Promise.all(
                urls.map(async (url) => {
                  const svg = new DOMParser().parseFromString(
                    await (await fetch(url)).text(),
                    "image/svg+xml",
                  );
                  return [...svg.querySelectorAll("path")].map((path) =>
                    path.getAttribute("d"),
                  );
                }),
              ),
            [favicon, icon],
          );
          assert.equal(canonicalArt.length, 2);
          assert.deepEqual(faviconArt, canonicalArt);
          const cardURL = await page
            .locator('meta[property="og:image"]')
            .getAttribute("content");
          assert.equal(cardURL, "https://bugsnitch.com/brand/social-share.png");
          assert.deepEqual(
            await page.evaluate(async (path) => {
              const response = await fetch(path);
              const bitmap = await createImageBitmap(await response.blob());
              return [
                response.headers.get("content-type"),
                bitmap.width,
                bitmap.height,
              ];
            }, new URL(cardURL).pathname),
            ["image/png", 1200, 630],
          );
        }
        const hero = page.getByRole("region", {
          name: "From issue to verified release.",
        });
        assert.deepEqual(
          await hero.locator("h1").evaluate((heading) => {
            const accent = heading.querySelector("em");
            return [
              accent.textContent,
              getComputedStyle(accent).color,
              getComputedStyle(accent.parentElement).color ===
                getComputedStyle(heading).color,
            ];
          }),
          ["verified", "rgb(212, 0, 0)", true],
        );
        const footer = page.getByRole("contentinfo");
        assert.equal(
          await footer
            .getByRole("navigation", { name: "Product links" })
            .getByRole("link", { name: "Workspace" })
            .getAttribute("href"),
          "/app/",
        );
        await footer.getByRole("link", { name: "Documentation" }).waitFor();
        await hero.getByRole("link", { name: "See a factory run" }).click();
        assert.equal(new URL(page.url()).hash, "#showcase");
        const showcase = page.getByRole("region", {
          name: "Inside a factory run",
        });
        assert.equal(await showcase.locator("video").count(), 0);
        assert.equal(await showcase.getByRole("button").count(), 0);
        const progress = showcase.getByRole("list", {
          name: "Factory run progress",
        });
        const activeStep = progress.locator('[aria-current="step"]');
        const expectStep = (name) =>
          activeStep.and(progress.getByText(name, { exact: true })).waitFor();
        await page.waitForFunction(
          () =>
            Math.abs(
              document
                .querySelector(".walkthrough-viewport")
                .getBoundingClientRect().top,
            ) < 2,
        );
        await expectStep("Plan");
        await showcase.getByText("Invite expired", { exact: true }).waitFor();

        const track = showcase.locator(".walkthrough[data-pinned]");
        await track.waitFor();
        const panel = track.locator(".walkthrough-viewport");
        const runway = track.locator(".walkthrough-runway");
        const panelBounds = await panel.boundingBox();
        assert.ok(Math.abs(panelBounds.y) < 2);
        assert.ok(panelBounds.height >= 899);
        assert.equal(await showcase.getByRole("article").count(), 1);

        const stepDistance = Math.round(
          (await runway.boundingBox()).height / 4,
        );
        const shortScroll = Math.round(stepDistance / 3);
        const transform = await showcase
          .locator("#invite-failure .run-evidence")
          .evaluate((element) => getComputedStyle(element).transform);
        await page.mouse.move(width / 2, 400);
        await page.mouse.wheel(0, shortScroll);
        await page.waitForFunction((initial) => {
          const art = document.querySelector("#invite-failure .run-evidence");
          return getComputedStyle(art).transform !== initial;
        }, transform);
        assert.equal(await activeStep.textContent(), "Plan");
        const toTransition = Math.round(stepDistance * 0.81) - shortScroll;
        await page.mouse.wheel(0, toTransition);
        await page.waitForFunction(() => {
          const offset = (id) =>
            parseFloat(
              document
                .getElementById(id)
                .style.getPropertyValue("--slide-offset"),
            );
          return offset("invite-failure") < 0 && offset("invite-runner") < 100;
        });
        await page.mouse.wheel(0, stepDistance - toTransition);
        await expectStep("Build");
        assert.ok(Math.abs((await panel.boundingBox()).y) < 2);
        await showcase.getByText("Invite bug reproduced").waitFor();
        await page.mouse.wheel(0, -(shortScroll + stepDistance));
        await expectStep("Plan");

        await page.mouse.wheel(0, Math.round(stepDistance * 2.1));
        await expectStep("Approve");
        await showcase
          .getByText("+ expiresAt = issuedAt + ttlMs", { exact: true })
          .waitFor();
        await page.mouse.wheel(0, stepDistance);
        await expectStep("Verify");
        await showcase.getByText("Invite accepted", { exact: true }).waitFor();

        await page.mouse.wheel(0, stepDistance * 2);
        await page.waitForFunction(
          () =>
            document.querySelector("#workflow").getBoundingClientRect().top <
            innerHeight,
        );
        if (width === 360) {
          await page.emulateMedia({ reducedMotion: "reduce" });
          await showcase.getByRole("article").nth(3).waitFor();
          assert.equal(
            await showcase
              .locator(".run-evidence")
              .first()
              .evaluate((element) => getComputedStyle(element).transform),
            "none",
          );
        }

        const workflow = page.getByRole("region", {
          name: "How the factory runs",
        });
        assert.equal(await workflow.getByRole("article").count(), 3);
        await workflow
          .getByRole("link", { name: "Deployment verification guide" })
          .waitFor();
        await page.locator("#evidence > summary").click();
        assert.ok(
          await page.evaluate(
            () => document.documentElement.scrollWidth <= innerWidth,
          ),
        );
        await page
          .getByRole("heading", { name: "Start with one issue" })
          .waitFor();
        assert.equal(
          await page.locator("main > section").last().getAttribute("id"),
          "quickstart",
        );
        await page.getByRole("button", { name: "Copy command" }).click();
        assert.equal(
          await page.evaluate(() => navigator.clipboard.readText()),
          "make checkout-acceptance",
        );
        await page
          .getByRole("banner")
          .getByRole("link", { name: "Workspace", exact: true })
          .click();
        await page
          .getByRole("heading", { name: "Overview", exact: true })
          .waitFor();
        assert.equal(new URL(page.url()).pathname, "/app/");
        assert.equal(
          await page.locator("html").getAttribute("data-theme"),
          "light",
        );
        assert.equal(
          await page
            .locator('link[rel="icon"][type="image/svg+xml"]')
            .getAttribute("href"),
          favicon,
        );
        await page.goto(origin + "/#/plans?project=p1");
        await page
          .getByRole("heading", { name: "Plans", exact: true })
          .first()
          .waitFor();
        assert.equal(new URL(page.url()).hash, "#/plans?project=p1");
      }
    },
  );
  await t.test(
    "slow workspace reads survive the next polling interval",
    async (t) => {
      const page = await browser.newPage();
      t.after(() => page.close());
      await page.clock.install();
      let reads = 0;
      const started = Promise.withResolvers();
      const release = Promise.withResolvers();
      await page.route("**/api/v1/state", async (route) => {
        const state = fixture();
        const read = ++reads;
        if (read === 1) {
          started.resolve();
          await release.promise;
        }
        state.projects[0].name = "Workspace read " + read;
        await route.fulfill({ json: state });
      });
      await page.goto(url);
      await started.promise;
      await page.clock.fastForward(13000);
      release.resolve();
      await page.getByRole("link", { name: /^Workspace read 1 / }).waitFor();
      assert.equal(reads, 1);
      await page.clock.fastForward(13000);
      await page.getByRole("link", { name: /^Workspace read 2 / }).waitFor();
    },
  );
  await t.test(
    "legacy plans render and an acknowledged plan save is not retried",
    async (t) => {
      const page = await browser.newPage();
      t.after(() => page.close());
      const state = fixture();
      const legacy = {
        id: "legacy",
        projectId: "p1",
        title: "Legacy plan",
        version: 1,
        status: "approved",
        executionOrder: [1],
        document: {
          milestone: "First release",
          tickets: [{ number: 1, title: "Legacy ticket", criteria: null }],
        },
      };
      state.plans = [
        legacy,
        {
          ...legacy,
          id: "empty",
          title: "Plan without tickets",
          document: { tickets: null },
        },
      ];
      let rejectWrite = true,
        failRead = false,
        writes = 0;
      await page.route("**/api/v1/state", (route) =>
        failRead
          ? route.fulfill({
              status: 503,
              json: { error: "Snapshot unavailable" },
            })
          : route.fulfill({ json: state }),
      );
      await page.route("**/api/v1/plans", (route) => {
        writes++;
        if (rejectWrite)
          return route.fulfill({
            status: 409,
            json: { error: "Project changed" },
          });
        const { plan } = route.request().postDataJSON();
        state.projects[0].version++;
        state.plans.push({
          ...legacy,
          id: "new",
          title: plan.title,
          document: plan,
          status: "proposed",
        });
        failRead = true;
        return route.fulfill({ status: 201, json: { id: "new" } });
      });
      await page.goto(url + "/#/plans?project=p1");
      await page
        .getByRole("heading", { name: "Plan without tickets" })
        .waitFor();
      await page.getByText("#1 · Legacy ticket").click();
      await page.getByText("No criteria recorded").waitFor();
      await page.getByRole("button", { name: "Draft plan" }).click();
      const editor = page.getByRole("dialog");
      await editor
        .getByLabel("Deliverable", { exact: true })
        .fill("Saved version");
      await editor.getByLabel("Milestone", { exact: true }).fill("Release");
      await editor.getByLabel("GitHub issue number").fill("7");
      await editor.getByLabel("Ticket title").fill("Show acceptance criteria");
      await editor
        .getByLabel(/Acceptance criteria/)
        .fill("Criteria are readable.");
      await editor.getByLabel("Verification method").fill("Browser test");
      await editor.getByRole("button", { name: "Save draft" }).click();
      await editor
        .getByRole("alert")
        .filter({ hasText: "Project changed" })
        .waitFor();
      assert.equal(writes, 1);
      rejectWrite = false;
      await editor.getByRole("button", { name: "Save draft" }).click();
      await editor.waitFor({ state: "hidden" });
      await page
        .getByText(
          /Your request succeeded, but the workspace could not be refreshed/,
        )
        .waitFor();
      assert.equal(
        await page.getByRole("button", { name: "Draft plan" }).isDisabled(),
        true,
      );
      failRead = false;
      await page.getByRole("button", { name: "Refresh", exact: true }).click();
      await page.getByRole("heading", { name: "Saved version" }).waitFor();
      assert.equal(writes, 2);
    },
  );
  await t.test(
    "mobile work shows a version-bound local approval and respects dispatch pause",
    async (t) => {
      const page = await browser.newPage({
        viewport: { width: 360, height: 800 },
      });
      t.after(() => page.close());
      const state = fixture();
      state.projects[0].paused = false;
      const id = "a".repeat(32);
      state.attempts = [
        {
          id,
          projectId: "p1",
          machine: "Builder",
          repository: "example/storefront",
          issueNumber: 7,
          phase: "implement",
          state: "waiting",
          reasonCode: "plan_review_required",
          version: 12,
          baseSha: "b".repeat(40),
          candidateSha: null,
          updatedAt: state.updatedAt,
          connection: "connected",
        },
      ];
      await page.route("**/api/v1/state", (route) =>
        route.fulfill({ json: state }),
      );
      const location = url + "#/work?project=p1&id=" + id;
      await page.goto(location);
      await page.getByRole("heading", { name: "Next action" }).waitFor();
      await page
        .getByText("Review the implementation plan", { exact: true })
        .waitFor();
      await page
        .getByText("bugsnitch local-continue --id " + id + " --version 12", {
          exact: true,
        })
        .waitFor();
      assert.equal(
        await page.evaluate(() => document.documentElement.scrollWidth),
        360,
      );
      state.workspace.stopped = true;
      await page.reload();
      await page
        .getByText("Workspace dispatch is stopped", { exact: true })
        .waitFor();
      assert.equal(await page.getByText(/bugsnitch local-continue/).count(), 0);
    },
  );
  await t.test(
    "only the named approver sees the ticket decision for the current version",
    async (t) => {
      const page = await browser.newPage({
        viewport: { width: 360, height: 800 },
      });
      t.after(() => page.close());
      const state = fixture();
      state.projects[0].paused = false;
      const id = "e".repeat(32);
      const action = {
        attemptId: id,
        version: 7,
        kind: "review_plan",
        disposition: "ask",
        approver: "assigned",
        expiresAt: new Date(Date.now() + 60_000).toISOString(),
      };
      state.attempts = [
        {
          id,
          projectId: "p1",
          machine: "Builder",
          repository: project.repository,
          issueNumber: 7,
          phase: "implement",
          state: "waiting",
          reasonCode: "plan_review_required",
          version: 7,
          baseSha: "b".repeat(40),
          candidateSha: null,
          connection: "connected",
          updatedAt: state.updatedAt,
          nextAction: action,
        },
      ];
      state.viewer = {
        id: "different",
        role: "approver",
        name: "Other approver",
      };
      let request;
      await page.route("**/api/v1/state", (route) =>
        route.fulfill({ json: state }),
      );
      await page.route(
        "**/api/v1/attempts/" + id + "/ticket-decision",
        (route) => {
          request = route.request().postDataJSON();
          action.answer = "approve";
          return route.fulfill({ json: { answer: "approve" } });
        },
      );
      await page.goto(url + "#/work?project=p1&id=" + id);
      await page.getByText(/Requested from assigned/).waitFor();
      const approve = page.getByRole("button", {
        name: "Approve reviewed plan",
      });
      assert.equal(await approve.count(), 0);
      state.viewer.id = "assigned";
      await page.reload();
      await approve.click();
      await approve.waitFor({ state: "hidden" });
      assert.deepEqual(request, { version: 7, answer: "approve" });
    },
  );
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
          if (path.endsWith("/devices/desktop/limits")) {
            assert.deepEqual(req.postDataJSON(), { turns: 4, version: 1 });
            state.devices[0].turnLimit = 4;
            state.limits.version++;
            return route.fulfill({ json: { turns: 4, version: 2 } });
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
          await page.evaluate(() => document.fonts.ready);
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
        state.devices = [
          {
            id: "desktop",
            name: "Local Mac",
            platform: "darwin/arm64",
            projects: ["p1"],
            revoked: false,
          },
        ];
        await page.goto(url + "/#/settings?project=p1");
        await page.reload();
        await page
          .getByRole("heading", { name: "Workspace capacity" })
          .waitFor();
        assert.equal(
          await page.getByRole("button", { name: "Save device limit" }).count(),
          0,
        );
        state.devices[0].turnLimit = 2;
        await page.reload();
        await page.getByLabel("Local Mac simultaneous model turns").fill("4");
        await page.getByRole("button", { name: "Save device limit" }).click();
        await page.getByText("Device limit updated.").waitFor();
        assert.equal(
          await page
            .getByLabel("Local Mac simultaneous model turns")
            .inputValue(),
          "4",
        );
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
        const planPanel = page.locator("section").filter({
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
            phase: "wrap_up",
            state: "completed",
            updatedAt: state.updatedAt,
            baseSha: base,
            candidateSha: sha,
            connection: "disconnected",
            version: 3,
            sharedDiff: {
              digest: "c".repeat(64),
              candidateSha: sha,
              baseSha: base,
              sharedAt: state.updatedAt,
            },
          },
        ];
        await page.goto(url + "/#/work?project=p1");
        await page.reload();
        await page
          .getByRole("textbox", { name: "Search issues and attempts" })
          .fill("Local candidate ready");
        const row = page.getByRole("link", { name: /Local candidate ready/ });
        assert.ok((await row.innerText()).includes(project.repository + " #1"));
        await row.click();
        const issueLink = page
          .getByRole("heading", { name: project.repository + " #1" })
          .getByRole("link");
        assert.equal(
          await issueLink.getAttribute("href"),
          `https://github.com/${project.repository}/issues/1`,
        );
        await page
          .locator(".panel-head")
          .filter({ has: issueLink })
          .getByText("Local candidate ready", { exact: true })
          .waitFor();
        assert.equal(
          await page.getByText("Completed", { exact: true }).count(),
          0,
        );
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
