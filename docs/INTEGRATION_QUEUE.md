# Pull-request inspection

The dashboard's pull-request inventory contains owner-selected inspections. The service can obtain a separate installation token granting one repository read-only access to Pull requests, Issues, Checks, Commit statuses and Contents. Grant these additional read permissions to the App installation before inspecting PRs. Open an existing project with an active GitHub mapping and use the owner API to inspect a numbered PR and its canonical issue. Repositories must match the numeric installation mapping and the configured full name.

```http
POST /api/v1/projects/{project}/pulls/{number}/refresh
Content-Type: application/json

{"issueNumber":12,"version":0}
```

The first inspection takes version 0. Future refreshes use the presented version. The snapshot includes the PR head, current target branch tip, PR and issue state, issue requirements digest, review decision, and count and rollup status of checks on that exact head. A missing rollup, zero reported checks or check results from another commit yields unknown status. The API returns observed timestamps and flags stale snapshots. [GitHub pull-request fields](https://docs.github.com/en/graphql/reference/pulls) · [GitHub check rollups](https://docs.github.com/en/graphql/reference/commits)

An owner with a retained artifact may record its SHA-256 digest and kind, either `independent_review` or `combined_validation`, through `POST /api/v1/projects/{project}/pulls/{number}/evidence` with `version`, `kind` and `reference`. The reference identifies an owner attestation. Retain the referenced artifact and its execution transcript outside this inspector. The server refreshes GitHub first and rejects a changed subject. The version binds the specific head, tested target, ticket contents, plan version, project settings and observed check/review state. A change preserves historical receipts and removes them from current evidence.

Automatic integration is unavailable. The inspector does not have a merge or enqueue operation. An enforceable integration path requires configured branch/queue protections, required CI on the combined candidate, verified dependency and policy gates, current scoped decisions and review findings, and knowledge of deployment side effects. A successful rollup on the PR head cannot establish that its combination with another candidate passes. GitHub's merge queue can validate grouped changes against the latest target when its required checks run on merge groups. Eligibility depends on repository ownership, visibility and plan. [GitHub merge queues](https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/configuring-pull-request-merges/managing-a-merge-queue)

Protocol fixtures cover repository-scoped read permissions, missing checks, and a check rollup bound to the wrong commit. The PostgreSQL test `TestPostgresInvalidateEvidenceOnSubjectChange` changes the head, base, issue requirements, and project version independently and exercises the stale-write conflict. Run `go test ./internal/github ./internal/integration` with `BUGSNITCH_TEST_DATABASE_URL` pointing to a disposable PostgreSQL database for the persistence test. Actual private-repository access, queue settings and a live merge remain unverified.
