# Contributing to Bugsnitch

Start with the bundled checkout. It exercises the runner, the packaged service and Chromium against a disposable repository and local provider fixtures. Use a separate PostgreSQL database for integration tests. [Local setup](README.md#try-it-locally) · [Architecture](docs/ARCHITECTURE.md)

| If you change | Check |
| --- | --- |
| Homepage or workspace | Build with `npm --prefix web ci` and `npm --prefix web run build`, then run `npm --prefix web run test:browser` with Chrome installed. Check keyboard navigation and mobile layout. [Dashboard guide](docs/DASHBOARD.md) |
| Runner, service or integrations | Build the matched helpers, run the relevant Go and Rust tests, and exercise the public command or HTTP route. The database setup and environment variables are in the [verification guide](README.md#verification-and-operations). |
| Release or verification behavior | Run `make checkout-acceptance` for the bundled incident workflow. For changes to queue delivery or provider behavior, identify the GitHub protections or provider configuration your test exercised. [PR workflow](docs/INTEGRATION_QUEUE.md) · [Provider fixtures](docs/PROVIDERS.md) |
| Documentation or branding | Check relative links, section anchors and commands against the current checkout. Use the supplied artwork for the logo or wordmark. [Official assets](assets/brand/README.md) |

In your pull request, describe the behavior, list the checks you ran, and identify any live provider prerequisites. Use redacted or synthetic examples. [Support](SUPPORT.md) · [Private security reporting](SECURITY.md) · [Code of conduct](CODE_OF_CONDUCT.md)

Contributions to the source follow the [MPL-2.0 license](LICENSE). The Bugsnitch name and brand assets have [separate terms](TRADEMARKS.md).
