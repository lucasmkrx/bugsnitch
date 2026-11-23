# Shipping incident fixture

From this directory, run `python3 -m http.server 8086 --bind 127.0.0.1`, open `http://localhost:8086`, and click **Calculate shipping**. An order of exactly $50.00 incurs a $5.00 charge despite the advertised offer. Run `node verify.mjs` to reproduce the failure against the threshold. The assertions cover 4999, 5000 and 5001 cents.

## Local incident-to-repair exercise

Install the project toolchains, PostgreSQL and Chrome. Set `BUGSNITCH_TEST_CHROME` if you use a different Chromium executable. From the Bugsnitch repository root, run

```sh
make checkout-acceptance
```

By default the test starts a private, temporary PostgreSQL instance on a Unix socket and removes it on exit. Set `PGBIN` if your PostgreSQL tools are outside the directory reported by `pg_config --bindir`. To reuse a disposable test database, set `BUGSNITCH_TEST_DATABASE_URL` to its URL. The test role needs permission to create schemas there.

The target builds the packaged helpers, installs the locked browser dependencies and bundles the checkout SDK. It creates a temporary repository, a temporary runner profile and an isolated database schema. A test executable supplies scripted Codex planning, implementation and review responses. GitHub and Sentry use loopback fixtures throughout, so the exercise runs without provider accounts.

Chromium clicks the broken checkout twice. The actual SDK captures each exception and submits an envelope to an intercepted loopback endpoint. A provider fixture supplies the Sentry group, project, installation and signature. The production receiver accepts the deliveries, including a replay, and the PostgreSQL worker groups them. Repeated owner promotion produces one issue through the GitHub App protocol fixture. The selected criteria come from the published offer and the failing regression.

The runner creates a disposable Git worktree, pauses after planning, applies the authorized repair and runs the original regression command. The test checks that the successful validation and independent review belong to the candidate SHA. An ephemeral loopback HTTP service hosts the candidate's static assets, the SDK and a build-specific configuration. A deployment receipt records the SHA and asset manifest digest. Chromium clicks **Calculate shipping** on that build and checks the visible result before submitting positive validation. Redeploying the identical assets requires a fresh browser check before the incident is verified again. A click on the still-running old build preserves that verdict. An injected contradictory report for the fixed build returns it to triage.

## Run the repair with a live local Codex account

First run the acceptance target to prepare the dependencies and confirm the browser and database setup. Export the helper paths from the [test setup](../../README.md#verification-and-operations) and sign in to the supported local Codex runtime with your ChatGPT account. The following opt-in runs the same checkout exercise with real planning, implementation and independent review turns, spending that account's allowance.

```sh
BUGSNITCH_TEST_LIVE_CHECKOUT=1 go test -count=1 -v -timeout 15m \
  -run '^TestLiveCheckoutIncidentRepair$' ./internal/incidents
```

## Connect an instrumented test project

The default configuration works offline. To exercise a real Sentry project that you control, run `npm ci` and `npm run build:sdk` in the disposable copy, then set its public DSN in `config.mjs`. Use a unique release per deployed build. The pinned browser SDK captures a deliberate application signal with a stable grouping key. Run `npm test` to check the fixture's event projection. [SDK release](https://github.com/getsentry/sentry-javascript/releases/tag/11.13.0) · [SDK settings](https://docs.sentry.io/platforms/javascript/configuration/options/)

Map the Sentry installation and project, trigger the calculation twice and inspect the distinct event IDs and grouped incident. Promote the incident to a connected disposable GitHub repository. Set `node verify.mjs` as the approved runner validation command. Complete the repair, deploy the actual fixed revision under a new release identifier and record the positive check from that build. [Provider setup](../../docs/PROVIDERS.md)
