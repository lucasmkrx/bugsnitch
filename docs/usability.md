# Outside-user validation and contribution backlog

No independent user study has been performed for the 0.3.0 workflow. This file prepares the work so observations can be collected without inventing results or asking participants to share private source.

## Session protocol

Ask three developers unfamiliar with Bugsnitch to use the [disposable demo](demo.md), with the checked VSIX installed in their own editor. Include one keyboard-focused participant if available. Allow roughly 15 minutes each. Do not guide the first attempt; record when intervention becomes necessary.

1. From `checkout.js` line 2, find its associated commit and explain what the attribution does and does not establish.
2. Open the historical diff and identify the changed calculation. Find its parent and file history.
3. Browse the known-good tag and return to HEAD; explain whether the checkout changed.
4. Mark good/bad endpoints, inspect the candidate range, add reproduction evidence and export the assessment.
5. Close/reopen the panel and explain what was retained and what was lost.

Record editor/Git/OS versions, time to first useful evidence, task completion, help needed, navigation errors, wording misconceptions and one sentence of feedback. Use anonymous participant IDs. Obtain consent for quoted feedback or recordings. Do not solicit credentials or confidential repositories. A finding is evidence only after an actual session; record failures as well as successes.

## Results template

| Participant | Versions / access needs  | Tasks completed | First evidence time | Intervention / confusion | Suggested change |
| ----------- | ------------------------ | --------------- | ------------------- | ------------------------ | ---------------- |
| Pending     | No participant recruited | Not measured    | Not measured        | Not measured             | Not measured     |

Turn observations into focused issues and retest the fix with the affected task. There are no fabricated testimonials, adoption numbers or hiring outcomes.

## Ready-to-file contribution issues

These are scoped future tasks, not claims that the current release includes them. File them in GitHub only when a maintainer chooses to open them.

| Issue                                               | Starting point                                    | Acceptance criteria                                                                                                                                                    |
| --------------------------------------------------- | ------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Add regression-range wording usability findings     | This protocol, `regression-panel.tsx`, `usage.md` | Include an anonymized observed misunderstanding, update copy, repeat the task, and keep automated labels manual.                                                       |
| Verify SSH/WSL/container extension hosts            | `compatibility.md`, host runner                   | Record actual host/version/configuration, run both commands and native diff, assert no checkout/index mutation; narrow the support statement to measured environments. |
| Add symbol-level history feasibility fixture        | `packages/forensics`, `packages/git`              | Define language/symbol scope and rename/move limits first; add reproducible fixtures and avoid claiming causality.                                                     |
| Profile browser rendering for long loaded prefixes  | Benchmark and graph renderer                      | Measure DOM cost separately from layout on a documented wide fixture; preserve drawn ancestry, keyboard selection and paging if virtualization is justified.           |
| Screen-reader review of graph and evidence workflow | UI components and browser test                    | Record actual assistive technology/version; reproduce a navigation problem, fix semantics and validate keyboard behavior.                                              |

Read [CONTRIBUTING](../CONTRIBUTING.md) for setup, package boundaries, licensing and PR validation. Use [SUPPORT](../SUPPORT.md) for sanitized reports and [SECURITY](../SECURITY.md) for private vulnerability reporting. No response-time SLA is implied.
