# Contributing

Read the [charter](PROJECT_CHARTER.md), [architecture](docs/ARCHITECTURE.md) and [requirements](docs/REQUIREMENTS.md) before changes to diagnostics. Use the Go version pinned in `go.mod`. Keep analysis pure and collection read-only.

Run the [validation commands](docs/VALIDATION.md) for your change. Include fixtures for an observed problem, expected healthy behavior, and incomplete coverage when adding a rule. Keep policy, data minimization, and output changes visible in the PR. Use synthetic data and a disposable cluster for fixtures and reports.
