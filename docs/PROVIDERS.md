# GitHub and Sentry setup

GitHub supplies issue and revision identity throughout delivery. Sentry is optional for capturing production failures. For the fixture checkout walkthrough, the included provider messages let you exercise both paths locally. [Checkout walkthrough](../examples/checkout/README.md)

## GitHub App

Register an App and install it on a deliberately selected test repository with issues enabled. To publish issues, grant Issues read/write and the mandatory Metadata read permission. Subscribe to Issues and keep the installation lifecycle webhook enabled. Configure the public HTTPS webhook URL as `https://bugsnitch.com/hooks/github` when deploying on that domain. Set the App client ID in `GITHUB_APP_CLIENT_ID`, path to its private RSA PEM file in `GITHUB_APP_KEY_FILE`, and shared webhook secret in `GITHUB_WEBHOOK_SECRET`. Mount the key read-only for the service account. Keep the installation ID, numeric repository ID, and canonical owner/name together. [GitHub App installation authentication](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/generating-an-installation-access-token-for-a-github-app)

Give the installation the permissions needed by the workflows you enable. Bugsnitch requests a repository-scoped token for each operation.

| Operation | GitHub App repository permissions requested |
| --- | --- |
| Connect the repository | Metadata read |
| Read an issue or publish an approved issue | Issues read for a read, Issues write for publication |
| Inspect PR checks and reviews | Contents, Issues, Pull requests, Checks and Commit statuses read |
| Open a draft for a published candidate branch | Contents and Issues read, Pull requests write |
| Inspect protected queue rules | Contents, Pull requests, Administration, Merge queues and Checks read |
| Authorize protected queue delivery | Contents, Pull requests and Administration read, Merge queues and Checks write |

Publishing the local candidate branch uses the originating device’s own GitHub credentials. For protected queue delivery, configure the ruleset and merge-group CI for your target. Deployment monitoring uses separate webhook subscriptions and a verifier identity. [PR and queue setup](INTEGRATION_QUEUE.md) · [GitHub deployment monitor](#github-deployment-monitor)

The service signs RS256 JWTs, requests one installation token for precisely the configured repository ID, and checks that ID against its current full name before issuing a write. Installation tokens live in process memory. REST requests pin version `2026-03-10`. The receiver validates the HMAC-SHA256 signature on the original bytes and records the delivery ID in PostgreSQL. Issue webhooks schedule a fresh read. Healthy incident-promoted issues become due for another read after 15 minutes. Planned tickets use the five-minute schedule described under [Planned ticket drift](#planned-ticket-drift). Installation suspension, deletion, and repository removal disable the affected mappings. An owner must review access before re-enabling them. [JWT claims](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/generating-a-json-web-token-jwt-for-a-github-app) · [Signature validation](https://docs.github.com/en/webhooks/using-webhooks/validating-webhook-deliveries) · [Issue API](https://docs.github.com/en/rest/issues/issues)

For dashboard setup, set `BUGSNITCH_GITHUB_INSTALLATION_ID` to the installation trusted by this workspace. In Settings, choose **Connect repository** for the project. Bugsnitch requests a metadata-only token for the selected repository name, verifies its full name and numeric identity with GitHub and records the mapping. A disabled mapping requires explicit reconnection. For operator-managed installations, bind provider identities using a PostgreSQL session. Replace all example values, check the query's row count, and keep these tables inaccessible to runners.

```sql
INSERT INTO github_mappings(workspace_id, project_id, installation_id, repository_id)
SELECT workspace_id,id,12345,98765 FROM projects
WHERE workspace_id='personal' AND repository='example/disposable';
```

In **Incidents**, open the report and choose **Approve issue promotion**. The equivalent owner API submits its current version to `POST /api/v1/projects/{project}/incidents/{id}/promote`. Repeating the same promotion returns the existing intent. Every mutation uses the normal authenticated owner API. The worker persists a reconciliation marker before sending an issue create request. After a lost response or restart it searches open and closed issues for the marker and verifies App authorship. If the outcome stays uncertain, it stops after eight attempts. Inspect the actual repository and `incident_issues.reason`. If the marker exists, restore the intent to `reconciling` with `next_run=now(), attempt_count=0` after resolving access. Resetting an uncertain write to `queued` could create a duplicate. Authentication and definitive rate-limit failures have bounded retries. Revoked access, repository renames, and transfers require operator reconciliation. Promotion runs independently from repair scheduling.

## Sentry

Create an internal Integration Platform integration in the test organization, subscribe to **error.created**, and configure `https://bugsnitch.com/hooks/sentry` for a production deployment. Place its client secret in `SENTRY_CLIENT_SECRET`. Check that the organization’s plan includes the error webhook subscription when configuring the integration. The receiver supports the documented error webhook payload for instrumented browser and backend projects. [Error payload and plan availability](https://docs.sentry.io/integrations/integration-platform/webhooks/errors/) · [Webhook signatures](https://docs.sentry.io/integrations/integration-platform/webhooks/)

```sql
INSERT INTO sentry_mappings(workspace_id, project_id, installation_uuid, provider_project)
SELECT workspace_id,id,'14826316-45c2-4739-a97c-bd2892c10600',1234
FROM projects WHERE workspace_id='personal' AND repository='example/disposable';
```

The service verifies the HMAC over the transient raw body, matches both signed installation and project identities, and returns HTTP 202 only after committing a sanitized inbox record. The projector counts distinct provider event IDs within the mapping. Grouping uses the provider issue ID within the same mapping. The intake limit is 600 new events per mapped project per minute. Each observation retains its timestamp and available environment, release, exception class and up to eight in-app frame basenames and line numbers. These webhook fields cannot establish source-map status, deployed commit, actual user count or reproduction. The individual Sentry events remain the source for further investigation. Manual reports use a v1 fingerprint of normalized observed behavior, expected behavior and reproduction steps. [Sentry grouping](https://docs.sentry.io/concepts/data-management/event-grouping/) · [Debug IDs](https://docs.sentry.io/platforms/javascript/sourcemaps/troubleshooting_js/debug-ids/)

Review the actual SDK version and collection configuration in the connected application before enabling it. Current browser documentation describes collection of headers and additional categories depending on installed integrations. Bugsnitch only receives selected error webhooks. It discards request data, user objects, freeform exception messages, frame contents, variables, attachment data and breadcrumbs before persistence. It strips common credential shapes and URLs from owner-written text. An owner should read freeform reports before promotion, especially for a public repository. No SDK or replay instrumentation is installed into a customer application by this service. [JavaScript SDK collection defaults](https://docs.sentry.io/platforms/javascript/data-management/data-collected/)

## Inbound verification

On 2026-11-22 the Rust listener was checked against the providers' documented signing formats. GitHub sends a SHA-256 HMAC with the `sha256=` prefix in `X-Hub-Signature-256`. Sentry Integration Platform sends a hex SHA-256 HMAC in `Sentry-Hook-Signature`. Each uses its own configured secret and the exact request body. The webhook receiver verifies the signature before JSON decoding, checks the signed installation and project mapping, commits a sanitized record, then returns HTTP 202. The Sentry subscription uses the `error` resource with action `created`. [GitHub delivery validation](https://docs.github.com/en/webhooks/using-webhooks/validating-webhook-deliveries) · [Sentry signatures](https://docs.sentry.io/integrations/integration-platform/webhooks/) · [Sentry error payload](https://docs.sentry.io/integrations/integration-platform/webhooks/errors/)

After `make build`, run `python3 scripts/smoke_inbound.py` with the disposable `PGHOST`, `PGPORT` and `PGUSER` settings from the operations guide. It starts the packaged server and uses a separate database. The test checks signed fixtures, a one-byte payload change, duplicate delivery, unmapped installations, repository removal, and whether a synthetic secret survives Sentry field projection. It also runs a local HTTPS token introspection endpoint, checks the MCP resource audience and tool scopes, and verifies that a redirect never receives the bearer credential. A live delivery from the owner's GitHub App or Sentry installation requires the external setup above. [Test source](../scripts/smoke_inbound.py)

## Environment verification

The authenticated owner may attest check results and deployments using `POST /api/v1/projects/{project}/incidents/{id}/verification`. Supply the current incident version and a unique command ID per receipt. Reusing a command ID with different content returns HTTP 409. Receipts preserve the actor category, evidence description, exact revision, effective timestamp and server receipt time. Include the concrete check or artifact identifier in the evidence field. Timestamps must be real UTC instants already reached.

| kind | Required fields in addition to commandId, version, kind, candidate, at and evidence |
| --- | --- |
| fix | The candidate is the exact revision that passed the applicable check. |
| deployment | environment and build, optionally windowSeconds and minimumExposures |
| validation | environment, build and positive, optionally deploymentId, coverage and exposures |

Each validation is bound to the deployment in effect at the observation time. Supply that deployment’s `commandId` as `deploymentId`. An older client can omit the ID when the artifact identifies a single deployment in that environment. Redeploying the same artifact requires both a fresh validation and its explicit deployment ID. The verdict returns the bound ID with the validation. After upgrading, older validations with an ambiguous deployment history also require a fresh observation.

Read `GET /api/v1/projects/{project}/incidents/{id}/verification?environment=production` for the verdict and its supporting receipts. A verified verdict requires a validated fix, a current deployment of that exact revision, and a successful observation of the affected behavior on that build. Monitoring policies additionally require an attestation of functioning application instrumentation, enough observed exposures, and an observation covering the end of the configured window. A newer rollback changes the current verdict. A recurrence on an older identified build is distinct from contradictory evidence on the fixed build. Missing release or environment can make verification inconclusive. The owner can attest receipts directly or connect the GitHub deployment monitor below for deployment and behavior evidence. Source-map lookups and ancestry proofs for descendant builds still need implementation.

## GitHub deployment monitor

In an incident’s **Environment verification** panel, select the canary environment and check its verdict. After recording a validated fix or inspecting a merged PR for the incident’s linked GitHub issue, open **Connect a GitHub deployment monitor**. Supply the numeric GitHub user ID of the trusted deployment publisher, the dedicated behavior verifier’s GitHub App ID, and its check name for the original defect. Set the minimum observation window and number of measured exposures. Save the one-time read-only gate credential in the promotion job. You can disable the monitor there and revoke its credential. The equivalent owner API is `POST /api/v1/projects/{project}/incidents/{id}/deployment-monitor` with `environment`, `candidate`, `version`, `check`, `deployerId`, `verifierAppId`, `windowSeconds` and `minimumExposures`.

For a merged PR, select its inspected revision in **Fix revision** and supply the trusted GitHub App ID and check name for the original regression test on the integrated code. The API takes additional `pullNumber`, `pullVersion`, `fixCheck` and `fixVerifierAppId` fields. Set `candidate` to the inspected **merge SHA**. Bugsnitch checks the PR’s canonical issue, repository, inspection version and merge commit at registration. It holds promotion pending a fresh independent check on that commit. A change to the issue requirements invalidates this registration. The following is an example for the trusted integrated-regression job, with an exact checkout and a separately maintained test harness matching the owner-selected defect. The publisher uses a Checks-write installation token for the selected verifier App.

```sh
test "$(git rev-parse HEAD)" = "$MERGE_SHA" &&
  "$TRUSTED_CHECKS/checkout-shipping-regression" "$PWD" &&
  jq -n --arg name "$FIX_CHECK_NAME" --arg sha "$MERGE_SHA" --arg id "bugsnitch/fix/v1 $MONITOR" \
    '{name:$name,head_sha:$sha,status:"completed",conclusion:"success",external_id:$id}' \
    > "$RUNNER_TEMP/bugsnitch-integrated-fix.json" &&
  gh api --method POST "repos/$GITHUB_REPOSITORY/check-runs" --input "$RUNNER_TEMP/bugsnitch-integrated-fix.json"
```

1. Subscribe the mapped Bugsnitch GitHub App to **Deployment status** and **Check run** with repository Deployments and Checks read permissions. The receiver authenticates GitHub's signature and installation and repository IDs. The separate verifier App needs Checks write access. Keep its token in a trusted job controlled outside the candidate change. Give the deployment publisher exclusive control of the monitored environment. [GitHub webhooks](https://docs.github.com/en/webhooks/webhook-events-and-payloads#deployment_status) · [Check runs](https://docs.github.com/en/rest/checks/runs#create-a-check-run)
2. Have the publisher create a new GitHub deployment **for every activation**, including redeployments and rollbacks, with the exact commit as `ref`, `auto_merge: false`, the monitored `environment` and `payload: {"bugsnitchBuild":"<artifact SHA-256>"}`. Deploy those bytes and mark the deployment successful using the same publisher identity. Retain both GitHub IDs, the deployment and the successful status. Configure the application’s Sentry release as the artifact digest. Set its served manifest to `{"build":"<artifact SHA-256>","candidate":"<full commit>","deployment":123}`. The trusted probe and this uncached manifest must target the same canary instance or revision-specific endpoint. [GitHub deployments](https://docs.github.com/en/rest/deployments/deployments#create-a-deployment)
3. Run the trusted regression check in the canary environment. The supplied adapter checks the manifest before and after the observation and emits a check-run document. The probe command returns 0 for a passed behavior check, 1 for an observed failure, or another exit status for an unavailable probe. It outputs a small JSON object, for example `{"coverage":true,"exposures":42}` when healthy instrumentation counted 42 actual exposures, or `{}` for behavior-only checks. The publisher authenticates to GitHub as the registered verifier App. The following two commands run sequentially after setting the named workflow variables.

```sh
python3 scripts/github-deployment-probe.py --monitor "$MONITOR" --name "$CHECK_NAME" \
  --manifest "$CANARY_MANIFEST" --candidate "$COMMIT" --build "$ARTIFACT_SHA256" \
  --deployment "$DEPLOYMENT_ID" --status "$DEPLOYMENT_STATUS_ID" -- ./check-shipping \
  > "$RUNNER_TEMP/bugsnitch-check.json" &&
gh api --method POST "repos/$GITHUB_REPOSITORY/check-runs" --input "$RUNNER_TEMP/bugsnitch-check.json"
```

4. Immediately before advancing the rollout, submit the actual current deployment to the gate. Continue on HTTP 200 with `allowed: true`. HTTP 409 includes a reason such as insufficient exposure, a mismatched deployment, or a held environment. A transport failure leaves the decision pending. The CI/CD system executes the rollout. The workspace emergency stop and a later inactive or unsuccessful GitHub status for the same deployment also close the gate. A revised or inconclusive terminal check requires a new successful run. A confirmed behavior failure on the active deployment adds a hold that an owner must acknowledge.

```sh
jq -n --arg candidate "$COMMIT" --arg build "$ARTIFACT_SHA256" \
  --argjson deployment "$DEPLOYMENT_ID" --argjson status "$DEPLOYMENT_STATUS_ID" \
  '{candidate:$candidate,build:$build,deployment:$deployment,status:$status}' |
  curl --fail-with-body --silent --show-error --json @- \
    -H "Authorization: Bearer $BUGSNITCH_GATE_TOKEN" "$BUGSNITCH_ORIGIN/api/v1/deployment-gates/$MONITOR"
```

Each monitor covers its configured incident, fix revision and environment. The gate requires provider-authenticated deployment and validation, functioning instrumentation when requested, adequate exposure, and a matching deployment identity. A confirmed Sentry recurrence on the active build or a failed behavior check places a durable hold on promotion gates for the project and environment. A later redeployment still needs its own passing check. The incident panel lists holds and lets the owner acknowledge an individual hold after review. Removing a hold still requires fresh matching evidence before the gate passes. The gate evaluates recorded observations at request time. The deployment job must read the actual serving identity and serialize competing promotions. The GitHub and Sentry webhook delivery exercises use signed local fixtures.

## Test boundary

Follow the [focused test setup](../README.md#verification-and-operations). `go test ./internal/incidents` exercises signatures, field selection, credential redaction and verification gates. With `BUGSNITCH_TEST_DATABASE_URL` set to a disposable PostgreSQL URL, the same command creates isolated temporary schemas and exercises concurrent signed replay, cross-workspace rejection, scoped App token requests, lost-response recovery, GitHub outage/backoff, out-of-order issue hints, revoked mappings, deployment receipts, rollback and recurrence. A local HTTP transport fixture stands in for GitHub. Live Sentry installation, GitHub App authorization and issue creation require the configured test project and provider credentials.

The shipping-threshold app charges 500 cents at exactly $50, breaking its local regression. Its offline event-projection test passed. The locked checkout dependencies installed in a fresh directory and built the optional browser bundle. Sentry browser 11.13.0 normally includes breadcrumbs, HTTP context, console and session integrations. The fixture disables the default integrations and projects its explicitly captured signal in `beforeSend`. [Pinned SDK implementation](https://github.com/getsentry/sentry-javascript/blob/11.13.0/packages/browser/src/sdk.ts)

The opt-in checkout exercise uses the actual SDK, Chromium, PostgreSQL and a signed-in local Codex worker. It checks replay, promotion, the failing baseline, the reviewed candidate and an actual click on the locally deployed build. It supplies Sentry backend responses and GitHub App messages as protocol fixtures. Delivery to an authorized Sentry test project and creation of a real GitHub ticket still require the owner's configured integrations. [Exercise and run command](../examples/checkout/README.md)

## Planned ticket drift

Once publication links an issue, Bugsnitch keeps its numeric GitHub identity and repository mapping. Work lists these tickets alongside incident-promoted tickets. A background reconciler reads one due canonical issue at a time with a repository-scoped, read-only token. Healthy reads become due after five minutes. A queued webhook schedules an earlier read. Dispatch and new turns require an authoritative read within the last twenty minutes. An inaccessible provider becomes visibly unknown. Issue text contributes to a requirements digest, while the browser receives a sanitized title, state, timestamp and blocker reason.

Submitting a plan records the observed issue generations its owner will review. A substantive signed issue edit or lifecycle event invalidates that generation immediately, including a close followed by a reopen before the next poll. Replaying the same signed payload under a different delivery ID preserves the generation. Periodic reads also detect title, body, state or identity drift. A missing, transferred or inaccessible issue stays bound to its original repository and receives no replacement ticket. Temporary read outages suspend eligibility until a fresh authoritative read. Removing repository access invalidates the associated approval.

After a material change, inspect the current ticket on GitHub, submit a new canonical plan against known issue states and approve the new version. Existing attempts retain their machine ownership. Reported running attempts receive a pause request and future model-turn admission checks the bound generation. Completed prerequisites may be re-attested against a newly reviewed plan even when the canonical issue is closed. Closed tickets cannot start another worker or publish a draft PR. A review that relies on a prior issue version must be refreshed.
