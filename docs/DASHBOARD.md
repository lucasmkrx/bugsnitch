# Owner dashboard

Start the local stack as described in the [setup guide](../README.md), open the displayed URL, and sign in with the server’s owner token. Bugsnitch keeps the session in an HTTP-only cookie. The browser holds workspace data in memory and stores its theme preference locally. Hash links preserve the selected page, project, and individual record across reloads.

## First project

1. Choose **Register project** on Overview. Supply a friendly name and a GitHub repository in `owner/repository` form. Configure the server’s GitHub App installation separately.
2. Open **Devices**, choose **Pair device**, enter a computer name, and select the permitted projects. Use the command displayed with the one-time pairing code on the computer. Register the local repository path using the [runner guide](RUNNER.md).
3. In **Plans**, draft a deliverable and milestone, record uncertainty, and enter a verification method and acceptance criteria for each ticket. **Existing GitHub issues** uses canonical issue numbers. **Propose new GitHub issues** accepts provisional numbers for dependencies. Review the publication separately before authorizing external issue creation. Once GitHub confirms the numbers, the remapped execution plan needs its own approval.
4. Start authorized work on the registered computer. **Work** shows the latest synchronized attempt phase, execution state, machine, base and candidate revisions, and stage evidence. The workspace snapshot includes metadata for explicitly shared diffs. Choose **Load shared diff** to retrieve the matching candidate and base in a keyboard-scrollable region. A completed, reviewed attempt can request a draft PR after the owner pushes its branch. Pause and stop create control requests. Inspect the latest runner acknowledgment in the attempt.
5. In **Pull requests**, select a repository and enter the pull request and issue numbers. Inspection reads checks, review, candidate and base revisions, and integration blockers through the configured GitHub App. The observation has a timestamp. Refresh it when the candidate or base changes.

## Reports and decisions

**Report incident** records the observed and expected behavior, with optional reproduction steps. The incident page shows available release information, sanitized stack frames, issue promotion state, and the confirmed issue link. **Check verification** reads the environment-specific verdict and its available receipts.

**Settings** shows the project’s effective policy, per-action permissions, and shared workspace capacity. Review and confirm permission changes before applying them. Set content retention per project. Shortening the period asks for confirmation because the next cleanup can expire older shared diffs, chat bodies, and incident observations. Workspace emergency stop pauses every project and queues termination requests. Unlocking keeps each project paused until its owner approves resumption. Pausing project dispatch holds new assignments. Requesting a resume creates a version-bound decision. On **Decisions**, the browser fetches the latest subject before posting an answer. A network failure keeps an outstanding intent in session storage, including its request ID. Another click first checks the existing answer. Reconnection refreshes the display and leaves choices to the owner.

**Project chat** saves owner messages and shows the server’s status and reason. Hosted model execution depends on vendor eligibility. Local artifacts and runtime credentials remain on the registered computer. The dashboard shows stage-level evidence synchronized by that runner.

## Frontend development

The hosted Go service serves the Vite production output in `web/dist`. The local developer script builds and serves the production dashboard and API from one origin. To iterate using Vite, start the service with `BUGSNITCH_ORIGIN=http://127.0.0.1:5173`, run `npm --prefix web run dev`, and use that exact origin. Vite proxies API calls to the service on port 8787. The origin setting preserves the service’s browser mutation checks.

Run `npm --prefix web ci` followed by `npm --prefix web run build` for strict TypeScript checking and a production build. Dependencies are pinned in the lockfile. The workspace query cache uses React's external-store interface with background refresh, reconnect refresh, server-confirmed writes, and stale response suppression. Snapshot reads are scoped to the signed-in owner.

## Read-only demo

From the repository root, run `npm --prefix web ci` and `npm --prefix web run demo`. Open `http://127.0.0.1:4174`. This separate loopback server displays labeled synthetic project, attempt, decision, and incident records. It rejects writes and needs no credentials, PostgreSQL connection, or model access. Stop the process with Ctrl-C.

## Browser verification

Run `npm --prefix web run test:browser` after building, with Chrome installed or `BUGSNITCH_TEST_CHROME` pointing to a compatible Chromium executable. The test serves the compiled dashboard over loopback with a synthetic API fixture. It exercises keyboard focus, navigation, overflow, stale decisions, offline and reconnect behavior, explicit request replay, and device pairing at 1440 × 900 and 360 × 800. It also exercises the confirmation steps for policy changes, workspace emergency stop and unlock, and GitHub publication, plus theme persistence after reload. The lost-response checks cover both failure before the write and failure after the server records the answer. The fixture has no credentials or external writes. The service smoke test runs browser journeys against a disposable PostgreSQL-backed service.

The screenshots show the labeled test workspace and synthetic records.

[Desktop overview](screenshots/fixture-overview-1440.png) · [Phone overview](screenshots/fixture-overview-360.png)
