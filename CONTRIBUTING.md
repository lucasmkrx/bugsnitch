# Contributing

Start with the [local setup](README.md#try-it-locally). The bundled `make checkout-acceptance` exercise runs the incident-to-verification workflow with a disposable checkout and local provider fixtures. Use disposable repositories and a separate PostgreSQL database when testing integrations.

Run the checks relevant to your change. The [verification instructions](README.md#verification-and-operations) cover `make test`, `make build` and the database tests. In your pull request, describe the behavior you changed, list the checks you ran and identify any live provider prerequisites. Use redacted or synthetic examples in issues and pull requests.

See [support](SUPPORT.md) for bug reports and feature proposals, and use [private reporting](SECURITY.md) for security vulnerabilities. Contributions to the source follow the [MPL-2.0 license](LICENSE). The name and brand assets have [separate terms](TRADEMARKS.md).
