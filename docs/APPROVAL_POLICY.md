# Repository approval policies

Commit `.bugsnitch/approvals.json` on the trusted target branch. Run `approval-check` on local Git objects to see what the policy requires for one pull request. Run `approval-preview` to simulate the policy against labeled prior changes.

```json
{
  "schema": 1,
  "policyOwners": ["platform-lead"],
  "default": {
    "reviewDepth": "quick",
    "owners": [],
    "checks": [{"name":"build","appId":17}]
  },
  "rules": [
    {
      "name": "payments",
      "paths": ["src/payments/**", "**/settlement*.go"],
      "reviewDepth": "deep",
      "owners": ["payments-lead"],
      "checks": [{"name":"checkout","appId":19}]
    }
  ]
}
```

Replace the example actors, check names and numeric GitHub App IDs with the identities that produce and approve checks on your repository. Each matching rule adds required owners and checks. The deepest matched review level applies, in increasing order `quick`, `standard`, `deep`. Every named owner must approve. The author cannot provide their own required owner approval or independent review. Each required check needs exactly one current successful result with the configured name and App ID. An observer should first resolve superseded check runs and dismissed approvals using the forge.

Patterns are case-sensitive repository paths. A single `*` matches within one component. `**` as an entire component matches zero or more directories. Renames include both the deleted and added names. Policy changes additionally require all predecessor policy owners. Missing or unsupported policy, absent observations, unknown object revisions and a candidate behind or diverged from the selected target produce a hold.

The runner reads the path rules from the base of the diff it reviews. Quick review checks the change against the requested behavior and validator. Standard review also examines interactions with existing behavior and missing acceptance coverage. Deep review adds a second independent model thread to examine boundary inputs, retries, compatibility and the regression test. It receives the original diff and validation evidence without the first reviewer's conclusions. A blocking finding returns the candidate for correction. The local attempt retains both review artifacts and their exact validation reference. Set `reviewDepth` in the registered project configuration to require a minimum depth. Projects without a policy or override use standard review.

## Check one change

Supply full 40-character commit SHAs. The policy comes from the exact target commit’s Git object. The working copy and the PR's proposed policy edits have no role in this check.

```sh
bin/bugsnitch approval-check --repo /path/to/repository \
  --base "$TARGET_SHA" --head "$CANDIDATE_SHA" --evidence /private/path/evidence.json
```

The evidence JSON is one observer’s snapshot of the PR at that base and head. `reference` identifies a retained receipt. `approvers` contains the named people with current approvals.

```json
{
  "base": "aaaaaaaabbbbbbbbccccccccddddddddeeeeeeee",
  "head": "1111111122222222333333334444444455555555",
  "reference": "provider-observations/77/attempt-4",
  "author": "developer",
  "reviewer": "independent-reviewer",
  "reviewDepth": "deep",
  "reviewConclusion": "pass",
  "checks": [
    {"name":"build","appId":17,"conclusion":"success"},
    {"name":"checkout","appId":19,"conclusion":"success"}
  ],
  "approvers": ["payments-lead"]
}
```

The command prints the actual policy revision, policy digest, changed paths, matched rules, required evidence, and reasons for a hold. It exits zero on `would_allow` and nonzero on a hold or input error. For CI use, run a trusted copy of the evaluator, pin the target and candidate using authenticated provider data, and collect the evidence independently of the candidate. An evidence file is a local attestation. The command performs no GitHub mutations. Enforcing the result requires a protected, App-bound check on the actual target or merge group. See [queue authorization](INTEGRATION_QUEUE.md#queue-authorization-boundary).

## Replay historical changes

The workspace owner can label an inspected pre-merge revision and download a case file in Settings → Approval policy history. The export retains the observed base and head, records the outcome evidence and includes attributable current-build recurrences. Run the replay in a checkout containing those Git objects. [Historical approval cases](INTEGRATIONS.md#historical-approval-cases)

To supply your own cases, create a JSON file with schema 1 and a `cases` array. Each case has a unique `id`, immutable `base` and `head`, `cohort` (`calibration` or `holdout`), `label` (`clean`, `regression` or `revert`), and `labelReference` identifying the accepted result, confirmed incident or observed revert. An optional `evidence` object uses the single-change schema above. An omitted observation produces a hold and shows the policy requirements when the Git subjects and policy are available.

```sh
bin/bugsnitch approval-preview --repo /path/to/repository \
  --cases /private/path/labeled-prs.json --policy-revision "$PROPOSED_POLICY_SHA"
```

Without the override, the replay uses each PR’s trusted target revision. With it, ordinary changes are evaluated under the proposed policy. A change that edits the policy is always evaluated using its own predecessor. The report tallies allowed and held changes by cohort and label, and includes the historical outcome reference beside each decision. Keep held-out changes distinct from the examples used to tune policy. A `would_allow` on a confirmed regression or revert identifies a case for investigation. A hold due to missing historical evidence has its own reason.

## Require the policy in protected CI

Use `approval-github` for a required workflow. It reads the live PR, latest meaningful reviews and check runs from GitHub with a read-scoped token. Configure the trusted repository name and numeric ID, allowed target, and a **dedicated independent review App ID** outside the candidate checkout. Enroll each required CI producer by check name and its own numeric App ID in the trusted base policy. Use a protected organization ruleset that requires this exact workflow from a trusted source repository with no bypass actors. A candidate-controlled job with the same display name provides no such guarantee. [GitHub ruleset workflow requirements](https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/managing-rulesets/available-rules-for-rulesets) · [GitHub review observations](https://docs.github.com/en/graphql/reference/pulls) · [Check runs API](https://docs.github.com/en/rest/checks/runs)

The dedicated review App publishes a completed check on the actual PR head for each independently reviewed subject. Its name is `Bugsnitch independent review / LEVEL`. Its external ID binds the repository identity, PR number, target, base, head and depth. On the trusted runner, use the following command to generate a check body from the completed local validation and review artifacts. Supply the repository and PR information independently from the forge. The output can be posted by the trusted App installation to `POST /repos/OWNER/REPO/check-runs`. Keep its signing credentials with the publisher, outside the local validator and candidate workflows. [Create a GitHub check run](https://docs.github.com/en/rest/checks/runs#create-a-check-run)

```sh
bin/bugsnitch approval-review-receipt --id "$ATTEMPT" --version "$VERSION" \
  --repository "$REPOSITORY" --repository-id "$REPOSITORY_ID" --pr "$PR" \
  --target "$TARGET" --base "$BASE_SHA" --head "$HEAD_SHA"
```

The evaluator accepts named owners’ current approvals for the exact PR head and checks push permission. Any current GitHub review blocker holds the PR. It reads required checks on the PR SHA for a pull request, and on the synthetic SHA for a merge group. For a group, GitHub must still report the named PR queued against the enrolled target, the live queue ref must still point to the event SHA, its two ordered parents must be the exact base and reviewed PR head, and its tree must equal the reviewed candidate tree. Enroll a one-PR merge queue with this shape. Other queue commit layouts, truncated observations, generic GitHub Actions App checks and unavailable evidence remain on hold. Before returning a positive result, the adapter repeats the provider observations. GitHub owns the final protected operation.

Install the binary and GitHub CLI on a clean, ephemeral policy runner. The sample workflow fetches objects into a bare repository and executes the installed checker. Configure the repository name, IDs and target as trusted organization settings. A centrally managed required workflow should resolve these registrations using the authenticated repository identity.

```yaml
name: Bugsnitch approval policy
on:
  pull_request:
    types: [opened, reopened, synchronize, ready_for_review, edited]
  merge_group:
    types: [checks_requested]
permissions:
  contents: read
  pull-requests: read
  checks: read
jobs:
  approve:
    runs-on: [self-hosted, ephemeral, bugsnitch-policy]
    timeout-minutes: 15
    steps:
      - name: Evaluate the exact delivery subject
        shell: bash
        env:
          GH_TOKEN: ${{ github.token }}
          ENROLLED_REPOSITORY: ${{ vars.FACTORY_POLICY_REPOSITORY }}
          ENROLLED_REPOSITORY_ID: ${{ vars.FACTORY_POLICY_REPOSITORY_ID }}
          REVIEW_APP_ID: ${{ vars.FACTORY_POLICY_REVIEW_APP_ID }}
          TARGET: ${{ vars.FACTORY_POLICY_TARGET }}
        run: |
          set -euo pipefail
          test "$GITHUB_REPOSITORY" = "$ENROLLED_REPOSITORY"
          objects="$(mktemp -d "$RUNNER_TEMP/policy.XXXXXXXX")"
          git init --bare --quiet "$objects"
          jq -er 'if .merge_group then .merge_group.base_sha, .merge_group.head_sha
                  else .pull_request.base.sha, .pull_request.head.sha end' \
            "$GITHUB_EVENT_PATH" > "$RUNNER_TEMP/policy-revisions"
          while IFS= read -r sha; do
            [[ "$sha" =~ ^[0-9a-f]{40}$ ]]
            git -C "$objects" -c credential.helper='!gh auth git-credential' \
              fetch --no-tags "https://github.com/$ENROLLED_REPOSITORY.git" "$sha"
          done < "$RUNNER_TEMP/policy-revisions"
          /opt/bugsnitch/bin/bugsnitch approval-github --repo "$objects" \
            --event "$GITHUB_EVENT_PATH" --repository "$ENROLLED_REPOSITORY" \
            --repository-id "$ENROLLED_REPOSITORY_ID" --review-app "$REVIEW_APP_ID" \
            --target "$TARGET" --wait 10m
```

Run the job at first in observation mode by leaving it optional in the organization ruleset. Inspect held cases and enable the required workflow once the review publisher, dedicated CI apps and one-member merge-group layout have produced complete observations on the disposable repository. The local protocol tests exercise signed-in read requests, accepted PR and group snapshots, and held cases for retargeting, forks, stale receipts, shared check producers, changed queue refs, extra group members and revoked reviews. [GitHub merge groups and required workflows](https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/configuring-pull-request-merges/managing-a-merge-queue)
