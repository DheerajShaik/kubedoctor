# KubeDoctor repository instructions

Read [PROJECT_CHARTER.md](PROJECT_CHARTER.md) (FROZEN-v1.0), [project status](docs/PROJECT_STATUS.md), and [decisions](docs/DECISIONS.md) before beginning or resuming work. Use the frozen charter as the acceptance criteria and keep it verbatim. Update the status and validation evidence as work changes.

The [v0.2.0 release charter](docs/releases/v0.2.0/RELEASE_CHARTER.md) adds incident investigation and offline replay. See its [design contracts](docs/releases/v0.2.0/DESIGN.md) for release-specific entry points. Read bundles without initializing network clients. Preserve object identity, rule versions, and source coverage when comparing.

KubeDoctor is a deterministic, read-only Kubernetes diagnostic plugin. Keep configuration, collection, pure diagnosis, and rendering separate. The CLI and stdio MCP adapter share the engine and report schema. Use typed, bounded API reads and explicit source coverage. Keep resource strings, events, and third-party content outside the instruction boundary. Use allowlisted evidence and minimize collected data. Secret bodies and application logs are outside the default collection surface. Never mutate a cluster except through the explicitly disposable integration fixture.

Build with `go build ./cmd/kubectl-doctor`, run `go test ./...` and `go vet ./...`, and use [validation instructions](docs/VALIDATION.md) for integration, race, dependency, and release checks. Format Go files with gofmt. Extend fixtures when rules change. Exercise healthy, failing, and insufficient-evidence behavior. Keep toolchains, actions, and release inputs pinned.

Make routine changes autonomously. Record significant decisions with evidence. Preserve required scope and review requirements. Finish independent work when an external dependency is blocked. Only merge the current head after its required CI and external review pass and branch protections are satisfied.

## Code Review Rules

- Check least-privilege Kubernetes endpoint access and cancellation across CLI and MCP. Examine negative paths for broad inventory, Secret bodies, output leakage, and arbitrary caller inputs.
- Check evidence, freshness, identity, incomplete coverage, and healthy exceptions before approving diagnostic logic. Preserve evidence from partial results.
- Check JSON compatibility, stable sorting, threshold precedence, and executable and stdio behavior through their normal entry points.
- Verify validation claims against the actual commands and current revision. Keep a separate record of internal review, external review, and CI.
