# Incident investigation and release verification

The development baseline is main at `2f31b9381446458d1ada7c12162bc234660ae448`. On November 23, 2026 GitHub reported a public repository, an unpublished v0.1.0 draft, successful default-branch CI, and no open pull request or v0.2 tag. The old validation record's assertion of private repository visibility was incorrect. The v0.1 charter and release are preserved.

## Operator contracts

| Operation | Input | Result and failure behavior |
| --- | --- | --- |
| capture namespace NAME / capture cluster | Startup kubeconfig, optional trusted metrics config, new bundle path | Save a bounded minimized observation with timing, coverage, identity, analyzable evidence and stored v1 results. Required collection loss or explicitly requested history loss exits 2 after writing available evidence. Findings alone allow capture to exit 0. |
| inspect BUNDLE | Local evidence file | Verify and inventory before displaying. No client construction. |
| replay BUNDLE | Verified evidence, captured time | Current compatible analyzer reproduces an investigation. Explicit stored mode reads the original report. Explicit reevaluation records changed interpretation. No credential helper or external request. |
| investigate BUNDLE | Verified evidence, compatible rules | Same investigation as replay. Explore ownership, findings, alternatives and recovery checks. |
| compare BEFORE AFTER | Two independently verified bundles | Classify changes at instance and workload scope. 2 for incomplete/incompatible comparison, 1 for a qualifying regression, 0 otherwise. |
| HTML | Same investigation or comparison used for CLI and JSON | Standalone semantic HTML, author-controlled fixed script, escaped text, print view, optional operator Grafana navigation. |
| stdio MCP | Startup-authorized context, scopes and bounded artifacts | Diagnostic-only tools use the same collector, analyzer and comparison logic. |

New commands take `-o text|json|html` where applicable and `--file` for an exclusive destination. The v1 report, legacy namespace/cluster commands, filtering and gate semantics remain public contracts. Bundle format, investigation schema, comparison schema, rules and producer are versioned separately. A collection covers an interval.

## Decisions and sources

- Store an uncompressed, integrity-checked JSON envelope. Decode a dedicated field allowlist with explicit budgets and reject ambiguous representations. Preserve original observation time and partial coverage. The payload hash establishes byte integrity. Raw API objects, free-form event messages and literal configuration values never form the replay format.
- Infer no causal relationship from a selector alone. Track reference fields, same-namespace constraints and owner UID chains. Object recovery requires continued identity plus relevant inspection. A stable controller can persist through child replacement. [Kubernetes owners and dependents](https://kubernetes.io/docs/concepts/overview/working-with-objects/owners-dependents/) · [Kubernetes names and UIDs](https://kubernetes.io/docs/concepts/overview/working-with-objects/names/)
- Keep the historical provider opt-in and namespace/workload scoped. Separate real scrape timestamps from evaluation steps, preserve missing samples, and suppress unsupported advice. [Prometheus query basics](https://prometheus.io/docs/prometheus/latest/querying/basics/) · [Kubernetes autoscaling](https://kubernetes.io/docs/concepts/workloads/autoscaling/horizontal-pod-autoscale/)
- Render from the shared typed model. Go's contextual escaping applies to untrusted values. Embed only a fixed local script. No remote assets or server are necessary for rendering. [Go html/template](https://pkg.go.dev/html/template) · [Content Security Policy](https://developer.mozilla.org/en-US/docs/Web/HTTP/Guides/CSP)
- MCP accepts scoped opaque handles. Use startup-only configuration, bounded operations, and explicit output limits. [MCP stdio](https://modelcontextprotocol.io/specification/2025-11-25/basic/transports) · [Go rooted filesystem APIs](https://pkg.go.dev/os#Root)

## Prior art

Popeye already offers live read-only resource linting, HTML reports, and Prometheus metrics export. K8sGPT pairs Kubernetes analyzers with optional AI explanations. HolmesGPT investigates through integrations and toolsets. Troubleshoot packages configurable collectors and analyzers for support bundles. KubeDoctor's design uses a fixed allowlist of replayable facts, explicit unknowns, and a conservative before/after gate. These comparisons describe overlapping workflows. No implementation from those tools was imported. [Popeye](https://popeyecli.io/) · [K8sGPT](https://github.com/k8sgpt-ai/k8sgpt) · [HolmesGPT](https://holmesgpt.dev/latest/) · [Troubleshoot](https://troubleshoot.sh/docs/support-bundle/introduction/)

The earlier OpsForge comparison remains in [research](../../RESEARCH.md). Follow the [release charter](RELEASE_CHARTER.md) and [independent design review](DESIGN_REVIEW.md) for acceptance.
