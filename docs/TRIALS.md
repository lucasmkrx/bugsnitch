# Implementation trial reports

## Stock and timer trials, November 23, 2026

| Trial and issue | Issue source | Implementation | Review | Local validator | Outcome |
| --- | --- | --- | --- | --- | --- |
| Stock ledger · `fixture/stock-ledger#1` | Fixture GitHub response | Live model, `gpt-6.2-sol` | Live model, separate `gpt-6.2-sol` turn | Exit 0 at `6b069fd64f82` | Local candidate ready |
| Countdown repair · `fixture/timer-rehearsal#1` | Fixture GitHub response | Scripted replacement | Scripted review | Exit 0 at `6e4fea13476b` | Local candidate ready |
| Countdown negative control · `fixture/timer-rehearsal#1` | Fixture GitHub response | Scripted incorrect replacement | Did not run | Exit 1 at `a8a37ffed10a` | Failed validation |

All three harnesses supplied the issue response in process and ran the registered Node validator in a real Git worktree. The stock harness used the signed-in Codex adapter for implementation and review. An independent replay check also passed 4,665 comparisons on the stock candidate. The countdown harness supplied the model outputs directly. In the negative control, the failing validator stopped execution before review.

These November 23 runs used an earlier runner and measured local candidate delivery. The November 24 paired controls exercised additional feature and interruption cases with scripted workers. The checkout exercise separately recorded browser evidence on a served build. [Paired controls](FACTORY_EVAL.md#local-product-probes-november-24-2026) · [Checkout acceptance](../examples/checkout/README.md)

## Report another trial

From the Bugsnitch checkout, build the CLI and its matching journal helper with `make runner`. Read the attempt using its original profile (set `BUGSNITCH_HOME` if it used a custom directory). Supply the sources used by the trial harness. This example uses a fixture issue with a live implementation and a live review.

```sh
bin/bugsnitch local-show --id ATTEMPT_ID |
  python3 scripts/trial-report.py - --issue-source fixture \
    --implementation-source live-model --review-source live-model
```

Use `github` for an issue read from GitHub or `fixture` for a supplied reply. Specify `live-model` or `scripted` independently for the implementation and review responses. Leave a source unspecified when you cannot establish it. The report then labels it `unknown`. A fixture can supply plausible URLs, model names and turn IDs, so these labels come from the operator's knowledge of the harness.

The JSON report gives the issue, attempt ID, candidate revision, validator exit code and recorded stages. The summary selects the implementation, validation and review artifacts for the reported candidate revision, including after a correction. Inspect the full `local-show` output for individual review passes and feedback. A missing artifact is marked `no_artifact`. A failed validator that stopped the run before review yields `not_run` for review. An implementation trial reports **Local candidate ready** when the runner completes wrap-up with a candidate revision. Staging verification and issue closure require their own evidence.
