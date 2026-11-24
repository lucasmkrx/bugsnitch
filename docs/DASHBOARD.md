# Workspace dashboard

Run `make dev` from the repository root. Open `http://localhost:8787/app/` to sign in, or use **Open workspace** on the homepage. The local stack saves the owner credential in `.local/dev/owner-token`. Named members sign in with their workspace access key. [Setup guide](../README.md#start-the-dashboard)

Bugsnitch keeps the session in an HTTP-only cookie. The browser holds workspace data in memory and stores its theme preference locally. Hash links preserve the selected page, project, and individual record across reloads.

## First project

1. Choose **Register project** on Overview. Supply a friendly name and a GitHub repository in `owner/repository` form. In **Settings**, use **Connect repository** after configuring the server’s GitHub App installation. [GitHub setup](PROVIDERS.md#github-app)
2. Open **Devices**, choose **Pair device**, enter a computer name, and select the permitted projects. Use the command displayed with the one-time pairing code on the computer. On that computer, use `bugsnitch register` to map the project to its checkout, then `bugsnitch project-add` to select the editable files or directories and trusted validation command. [Runner setup](RUNNER.md#select-a-project)
3. In **Plans**, draft a deliverable and milestone, record uncertainty, and enter a verification method and acceptance criteria for each ticket. **Existing GitHub issues** uses canonical issue numbers. **Propose new GitHub issues** accepts provisional numbers for dependencies. Review the publication separately before authorizing external issue creation. Once GitHub confirms the numbers, the remapped execution plan needs its own approval.
4. Run `bugsnitch next --project PROJECT_ID` on the enrolled computer to inspect eligibility, then `bugsnitch local-run --project PROJECT_ID` to start work. **Work** shows the latest synchronized attempt phase, execution state, machine, base and candidate revisions, and stage evidence. The workspace snapshot includes metadata for explicitly shared diffs. Choose **Load shared diff** to retrieve the matching candidate and base in a keyboard-scrollable region. For a reviewed candidate, authorize the target and run the displayed `local-ship` command on the originating device. The runner publishes the branch and the GitHub App opens the draft PR. You can also request a draft for an already published branch. Pause and stop create control requests. Inspect the latest runner acknowledgment in the attempt.
5. In **Pull requests**, select a repository and enter the pull request and issue numbers. Inspection reads checks, review, candidate and base revisions, and integration blockers through the configured GitHub App. The observation has a timestamp. Refresh it when the candidate or base changes. Attach the matching validation and review artifact digests before signing off. After GitHub reports a merge, record the environment, immutable build and verification digest in **Deployment handover**. [Integration queue](INTEGRATION_QUEUE.md)

## Access and release coordination

In **Settings → Workspace access**, the owner issues a key to a named operator or approver and can restrict it to a single project. Operators can report incidents and refresh pull-request inspections. Approvers can also approve plans, answer decisions, attach PR evidence and sign off on integration and record deployment handovers. Revoking the member removes their access.

Use **Settings → Multi-repository releases** to order inspected PRs for one environment. The protected queue waits for prior stages’ merged handovers and observes deployment holds. **Monthly workspace turn budget** limits new paired model starts. **Model usage and cost** shows measured tokens, missing measurements, and estimates based on the rates you supply. [Release ordering and budget](INTEGRATIONS.md#ordered-releases-and-workspace-budget)

## Reports and decisions

**Report incident** records the observed and expected behavior, with optional reproduction steps. The incident page shows available release information, sanitized stack frames, issue promotion state, and the confirmed issue link. **Check verification** reads the environment-specific verdict and its available receipts.

**Settings → Workspace capacity** controls the total active tickets and simultaneous model turns across enrolled devices. Each enrolled device also has its own model-turn limit on this panel. All three settings accept values from 1 to 32. Each Mac can set a lower local limit with `bugsnitch local-limits`.

**Settings** also shows the project’s effective policy and per-action permissions. Review and confirm permission changes before applying them. Set content retention per project. Shortening the period asks for confirmation because the next cleanup can expire older shared diffs, chat bodies, and incident observations. Workspace emergency stop pauses every project and queues termination requests. Unlocking keeps each project paused until an owner or scoped approver answers its resume decision. Pausing project dispatch holds new assignments. Requesting a resume creates a version-bound decision. On **Decisions**, the browser fetches the latest subject before posting an answer. A network failure keeps an outstanding intent in session storage, including its request ID. Another click first checks the existing answer. Reconnection refreshes the display so the owner or scoped approver can check the recorded answer.

**Project chat** saves owner messages and shows the server’s processing status and reason. Start a model-backed project conversation locally with `bugsnitch manager`. Local artifacts and runtime credentials remain on the registered computer. [Local manager](RUNNER.md#talk-to-the-project-manager-locally)

## Frontend development

The Rust HTTP service serves the Vite production output in `web/dist`, with the public homepage at `/` and the workspace at `/app/`. The local developer script builds and serves the production dashboard and API from one origin. To iterate using Vite, start the service with `BUGSNITCH_ORIGIN=http://127.0.0.1:5173`, run `npm --prefix web run dev`, and use that exact origin. Vite proxies API calls to the service on port 8787. The origin setting preserves the service’s browser mutation checks.

Run `npm --prefix web ci` followed by `npm --prefix web run build` for strict TypeScript checking and a production build. Dependencies are pinned in the lockfile. The workspace query cache uses React's external-store interface with background refresh, reconnect refresh, server-confirmed writes, and stale response suppression. Snapshot reads are scoped to the signed-in workspace and the member’s project access.

## Read-only demo

From the repository root, run `npm --prefix web ci` and `npm --prefix web run demo`. Open `http://127.0.0.1:4174`. This separate loopback server displays labeled synthetic project, attempt, decision, and incident records. It rejects writes and needs no credentials, PostgreSQL connection, or model access. Stop the process with Ctrl-C.

## Browser verification

Run `npm --prefix web run test:browser` after building, with Chrome installed or `BUGSNITCH_TEST_CHROME` pointing to a compatible Chromium executable. The test serves the compiled dashboard over loopback with a synthetic API fixture. It exercises keyboard focus, navigation, overflow, stale decisions, offline and reconnect behavior, explicit request replay, and device pairing at 1440 × 900 and 360 × 800. It also exercises the confirmation steps for policy changes, workspace emergency stop and unlock, and GitHub publication, plus theme persistence after reload. The lost-response checks cover both failure before the write and failure after the server records the answer. The fixture has no credentials or external writes. The service smoke test runs browser journeys against a disposable PostgreSQL-backed service.

The screenshots show the labeled test workspace and synthetic records.

[Desktop overview](screenshots/fixture-overview-1440.png) · [Phone overview](screenshots/fixture-overview-360.png)
