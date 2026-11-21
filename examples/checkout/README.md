# Shipping incident fixture

Copy this directory into a disposable test repository. Run `python3 -m http.server 8086 --bind 127.0.0.1`, open `http://localhost:8086`, and click **Calculate shipping**. An order of exactly $50.00 incurs a $5.00 charge despite the advertised offer. Run `node verify.mjs` to reproduce the failure against the threshold. The assertions cover 4999, 5000 and 5001 cents.

## Local incident-to-repair exercise

From the Bugsnitch repository root, install the locked browser harness and optional checkout SDK, then build the SDK bundle.

```sh
(cd web && npm ci)
(cd examples/checkout && npm ci && npm run build:sdk)
```

Install Chrome or set `BUGSNITCH_TEST_CHROME` to a Chromium executable. Select a disposable PostgreSQL database via `BUGSNITCH_TEST_DATABASE_URL`. The test creates and drops an isolated schema. Sign in to the supported local Codex runtime with your ChatGPT account. The following explicit opt-in spends that account's allowance on real planning, implementation and separate review turns.

```sh
BUGSNITCH_TEST_LIVE_CHECKOUT=1 go test -count=1 -v -timeout 15m \
  -run '^TestLiveCheckoutIncidentRepair$' ./internal/incidents
```

Chromium clicks the broken checkout twice. The actual SDK captures each exception and submits an envelope to an intercepted loopback endpoint. A provider fixture supplies the Sentry group, project, installation and signature. The production receiver accepts the deliveries, including a replay, and the PostgreSQL worker groups them. Repeated owner promotion produces one issue through the GitHub App protocol fixture. The selected criteria come from the published offer and the failing regression.

The runner creates a disposable Git worktree, pauses after planning, applies the authorized repair and runs the original regression command. The test checks that the successful validation and independent review belong to the candidate SHA. An ephemeral loopback HTTP service hosts the candidate's static assets, the SDK and a build-specific configuration. A deployment receipt records the SHA and asset manifest digest. Chromium clicks **Calculate shipping** on that build and checks the visible result before submitting positive validation. A fresh click on the still-running old build preserves the verdict. An injected contradictory report for the fixed build returns it to triage.

This exercise uses live local Chrome, Codex, Git, SQLite and PostgreSQL. The Sentry backend and GitHub App endpoints use protocol fixtures. The GitHub remote identifies the disposable repository and all Git operations are local. The test creates no provider-side issue, PR, or deployment. An actual connected Sentry and GitHub test project are required to prove that external chain.

## Connect an instrumented test project

The default configuration works offline. To exercise a real Sentry project that you control, run `npm ci` and `npm run build:sdk` in the disposable copy, then set its public DSN in `config.mjs`. Use a unique release per deployed build. The pinned browser SDK captures a deliberate application signal with a stable grouping key. Run `npm test` to check the fixture's event projection. [SDK release](https://github.com/getsentry/sentry-javascript/releases/tag/11.13.0) · [SDK settings](https://docs.sentry.io/platforms/javascript/configuration/options/)

Map the Sentry installation and project, trigger the calculation twice and inspect the distinct event IDs and grouped incident. Promote the incident to a connected disposable GitHub repository. Set `node verify.mjs` as the approved runner validation command. Complete the repair, deploy the actual fixed revision under a new release identifier and record the positive check from that build. [Provider setup](../../docs/PROVIDERS.md)
